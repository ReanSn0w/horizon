package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ReanSn0w/horizon/internal/session"
)

func TestSelectedProviderAndSessionCompaction(t *testing.T) {
	binary := buildBinary(t)
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%t", legacy), func(t *testing.T) {
			home, workspace := t.TempDir(), t.TempDir()
			main := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Error("request reached default provider")
				http.Error(w, "wrong provider", 500)
			}))
			defer main.Close()
			var compactCalls atomic.Int32
			other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer other-key" {
					t.Error("wrong provider credential")
				}
				var body struct {
					Model string `json:"model"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "other-model" {
					t.Errorf("model=%s err=%v", body.Model, err)
				}
				if r.URL.Path == "/responses/compact" {
					compactCalls.Add(1)
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"id":"compact-response","output":[{"type":"compaction","id":"compact-item","encrypted_content":"opaque"}]}`)
					return
				}
				if r.URL.Path != "/responses" {
					t.Error(r.URL.Path)
				}
				writeCompleted(w, 1, message("done"))
			}))
			defer other.Close()
			config := fmt.Sprintf(`mode: unit
default_model: main
models:
  main:
    provider: main
    model: main-model
    compact_threshold: 1000
  other:
    provider: other
    model: other-model
    compact_threshold: 1000
providers:
  main:
    url: %s
    key: main-key
  other:
    url: %s
    key: other-key
`, main.URL, other.URL)
			path := filepath.Join(home, "config.yaml")
			if err := os.WriteFile(path, []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			out, diag, err := run(binary, workspace, "", "--home", home, "resume", "--access", "full", "--mode", "plain", "--model", "other", "-m", "hello")
			if err != nil || out != "done\n" {
				t.Fatalf("resume: %v %s %s", err, out, diag)
			}
			store := session.NewStore(home)
			resolved, err := store.ResolveWorkspace(workspace)
			if err != nil {
				t.Fatal(err)
			}
			items, err := store.List(resolved, false)
			if err != nil || len(items) != 1 {
				t.Fatalf("sessions=%v err=%v", items, err)
			}
			value := items[0].Session
			if len(value.Turns) != 1 || value.Turns[0].Model.Provider != "other" {
				t.Fatal("provider was not persisted")
			}
			if legacy {
				value.Turns[0].Model.Provider = ""
				data, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(home, "dialogs", resolved.ID, value.SessionID+".json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				// Changing a profile must not reroute compaction of an existing new session.
				config = strings.Replace(config, "provider: other", "provider: main", 1)
				if err := os.WriteFile(path, []byte(config), 0600); err != nil {
					t.Fatal(err)
				}
			}
			out, diag, err = run(binary, workspace, "", "--home", home, "sessions", "compact", "--id", value.SessionID)
			if err != nil || compactCalls.Load() != 1 {
				t.Fatalf("compact: %v %s %s calls=%d", err, out, diag, compactCalls.Load())
			}
		})
	}
}
