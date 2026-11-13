package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ReanSn0w/horizon/internal/responses"
)

// The provider is scripted: this verifies the nap task can use global session
// commands and memory in an isolated home, without claiming a model will make
// the same editorial choices on every run.
func TestNapTaskOfflinePlumbing(t *testing.T) {
	binary := buildBinary(t)
	home, nap, other := t.TempDir(), t.TempDir(), t.TempDir()
	var mu sync.Mutex
	calls := 0
	var sessionID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var req responses.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		calls++
		if calls == 2 || calls == 3 {
			found := false
			for _, item := range req.Input {
				found = found || strings.Contains(string(item), "function_call_output")
			}
			if !found {
				t.Errorf("step %d did not receive prior command result: %+v", calls, req.Input)
			}
			if calls == 2 {
				seenID := false
				for _, item := range req.Input {
					seenID = seenID || strings.Contains(string(item), strings.TrimSpace(sessionID))
				}
				if !seenID {
					t.Error("global list did not return the other workspace session")
				}
			}
		}
		var output []json.RawMessage
		switch calls {
		case 1:
			output = []json.RawMessage{napTool(t, "list", "shell_exec", map[string]any{"command": "horizon sessions list --all --since 24h --json", "timeout_ms": nil, "max_output_chars": nil})}
		case 2:
			output = []json.RawMessage{napTool(t, "read", "shell_exec", map[string]any{"command": "horizon sessions read --all --id " + strings.TrimSpace(sessionID), "timeout_ms": nil, "max_output_chars": nil})}
		case 3:
			output = []json.RawMessage{napTool(t, "note", "memory__add", map[string]any{"scope": "agent", "text": "Observed repeated request: keep replies concise"})}
		default:
			output = message("done")
		}
		writeCompleted(w, calls, output)
	}))
	defer server.Close()
	writeConfig(t, home, server.URL)
	configPath := filepath.Join(home, "config.yaml")
	base, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, append(base, []byte("\nmemory:\n  enabled: true\n  threshold: 5\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	out, diag, err := run(binary, other, "", "--home", home, "sessions", "create")
	if err != nil {
		t.Fatal(out, diag, err)
	}
	sessionID = out
	template, err := os.ReadFile(filepath.Join("..", "..", "examples", "timers", "nap.md.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	task := string(template)
	for _, required := range []string{"pending.json", "processed.json", "sessions list --all --since 24h --json", "sessions read --all --id ID --turns 8", "memory__add"} {
		if !strings.Contains(task, required) {
			t.Fatalf("nap task missing %q", required)
		}
	}
	if index := strings.Index(task, "\n---\n"); index >= 0 {
		task = task[index+5:]
	}
	for runNumber := 0; runNumber < 2; runNumber++ {
		out, diag, err := run(binary, nap, "", "--home", home, "resume", "--access", "full", "--mode", "plain", "-m", task)
		if err != nil || out != "done\n" {
			t.Fatalf("nap %d: %q %q %v", runNumber, out, diag, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(home, "memory", "agent.json"))
	if err != nil || strings.Count(string(data), "Observed repeated request") != 1 {
		t.Fatalf("memory: %v %q", err, data)
	}
	if calls != 5 {
		t.Fatalf("unexpected response count %d", calls)
	}
	if _, err := os.Stat(filepath.Join(home, "bin", "horizon-telegram")); !os.IsNotExist(err) {
		t.Fatal("telegram plugin appeared in test home")
	}
}

func napTool(t *testing.T, id, name string, args any) json.RawMessage {
	t.Helper()
	arguments, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	item, err := json.Marshal(map[string]any{"type": "function_call", "call_id": id, "name": name, "arguments": string(arguments)})
	if err != nil {
		t.Fatal(err)
	}
	return item
}
