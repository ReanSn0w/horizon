package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ReanSn0w/horizon/internal/responses"
	"github.com/ReanSn0w/horizon/internal/session"
)

func TestBuiltMemoryPluginLifecycle(t *testing.T) {
	binary := buildBinary(t)
	home, workspace := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "plugins"), 0700); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", filepath.Join(home, "plugins", "horizon-memory"), "./plugins/memory")
	build.Dir = "../.."
	if data, err := build.CombinedOutput(); err != nil {
		t.Fatal(err, string(data))
	}
	var mu sync.Mutex
	agentCalls, summaryCalls := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/responses/compact" {
			var req responses.CompactRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if !strings.Contains(req.Instructions, "WORKSPACE_MEMORY") {
				t.Error("compact lost memory context")
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"compact","output":[{"type":"compaction","id":"c","encrypted_content":"opaque"}]}`)
			return
		}
		var req responses.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.Model == "memory-model" {
			summaryCalls++
			if req.Store || len(req.Tools) != 0 {
				t.Error("summary requested storage or tools")
			}
			writeCompleted(w, 99, message("WORKSPACE_MEMORY"))
			return
		}
		agentCalls++
		tools := false
		for _, tool := range req.Tools {
			tools = tools || tool.Name == "memory__add"
		}
		switch agentCalls {
		case 1:
			if !tools {
				t.Error("memory tools absent")
			}
			writeCompleted(w, 1, []json.RawMessage{json.RawMessage(`{"type":"function_call","call_id":"remember","name":"memory__add","arguments":"{\"scope\":\"workspace\",\"text\":\"Stable workspace fact\"}"}`)})
			return
		case 2:
			if strings.Contains(req.Instructions, "WORKSPACE_MEMORY") {
				t.Error("snapshot changed during turn")
			}
			found := false
			for _, item := range req.Input {
				found = found || strings.Contains(string(item), `\"saved\":true`)
			}
			if !found {
				t.Error("saved result absent")
			}
		case 3:
			if !strings.Contains(req.Instructions, "WORKSPACE_MEMORY") {
				t.Error("next turn missing memory")
			}
		case 4:
			if strings.Contains(req.Instructions, "WORKSPACE_MEMORY") {
				t.Error("workspace memory leaked")
			}
		case 5:
			if tools || strings.Contains(req.Instructions, "WORKSPACE_MEMORY") {
				t.Error("disabled plugin still active")
			}
		}
		writeCompleted(w, agentCalls, message("done"))
	}))
	defer server.Close()
	writeConfig(t, home, server.URL)
	path := filepath.Join(home, "config.yaml")
	base, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	enabled := string(base) + "\nagent_plugins: [memory]\nplugins:\n  memory:\n    threshold: 1\n    model: memory\n"
	enabled = strings.Replace(enabled, "provider:\n", "  memory:\n    model: memory-model\n    compact_threshold: 1000\nprovider:\n", 1)
	if err := os.WriteFile(path, []byte(enabled), 0600); err != nil {
		t.Fatal(err)
	}
	turn := func(dir string) {
		t.Helper()
		out, diag, err := run(binary, dir, "", "--home", home, "resume", "--access", "full", "--mode", "plain", "-m", "hello")
		if err != nil || out != "done\n" {
			t.Fatal(out, diag, err)
		}
	}
	turn(workspace)
	turn(workspace)
	normalized, err := session.NormalizeWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	memoryPath := filepath.Join(home, "memory", "workspaces", session.WorkspaceID(normalized)+".json")
	before, err := os.ReadFile(memoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if out, diag, err := run(binary, workspace, "", "--home", home, "sessions", "compact"); err != nil {
		t.Fatal(out, diag, err)
	}
	after, err := os.ReadFile(memoryPath)
	if err != nil || string(before) != string(after) {
		t.Fatal("session compact wrote memory", err)
	}
	turn(t.TempDir())
	if err := os.WriteFile(path, base, 0600); err != nil {
		t.Fatal(err)
	}
	turn(workspace)
	persisted, err := os.ReadFile(memoryPath)
	if err != nil || string(persisted) != string(after) {
		t.Fatal("disabling removed memory", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if summaryCalls != 1 || agentCalls != 5 {
		t.Fatal(summaryCalls, agentCalls)
	}
}
