package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestBuiltGatewayTelegramWorkflow(t *testing.T) {
	binary := buildBinary(t)
	home, cwd := t.TempDir(), t.TempDir()
	var mu sync.Mutex
	modelCalls, decisionCalls, polls := 0, 0, 0
	sent := []map[string]any{}
	updates := []map[string]any{}
	for i, entry := range []struct {
		chat, author     int64
		kind, name, text string
	}{
		{2, 2, "private", "Stranger", "PRIVATE_SECRET_SHOULD_NOT_LEAK"},
		{1, 1, "private", "Owner", "owner-input"},
		{-10, 3, "supergroup", "Test group", "group-input"},
		{-10, 3, "supergroup", "Test group", "queued-input"},
	} {
		chat := map[string]any{"id": entry.chat, "type": entry.kind}
		if entry.kind == "private" {
			chat["first_name"] = entry.name
		} else {
			chat["title"] = entry.name
		}
		updates = append(updates, map[string]any{"update_id": i + 1, "message": map[string]any{
			"message_id": i + 1, "date": 1700000000 + i, "chat": chat,
			"from": map[string]any{"id": entry.author, "first_name": "Participant"}, "text": entry.text,
		}})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, "bad request", 400)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/bottest-token/getMe":
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"id": 99, "is_bot": true, "username": "test_bot"}})
		case "/bottest-token/getWebhookInfo":
			fmt.Fprint(w, `{"ok":true,"result":{"url":""}}`)
		case "/bottest-token/getUpdates":
			polls++
			batch := []map[string]any{}
			for _, u := range updates {
				if float64(u["update_id"].(int)) >= body["offset"].(float64) {
					batch = append(batch, u)
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": batch})
		case "/bottest-token/sendMessage":
			if body["text"] != "generated-response" || body["parse_mode"] != nil {
				t.Errorf("unexpected Telegram message: %v", body)
			}
			sent = append(sent, body)
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 100 + len(sent)}})
		case "/responses":
			encoded, _ := json.Marshal(body)
			if strings.Contains(string(encoded), "PRIVATE_SECRET_SHOULD_NOT_LEAK") {
				t.Error("foreign private chat leaked into model context")
			}
			modelCalls++
			writeCompleted(w, modelCalls, message("generated-response"))
		case "/alpha/decisions":
			decisionCalls++
			encoded, _ := json.Marshal(body)
			if strings.Contains(string(encoded), "owner-input") || strings.Contains(string(encoded), "PRIVATE_SECRET_SHOULD_NOT_LEAK") {
				t.Error("cross-chat data in decision context")
			}
			if decisionCalls == 2 && !strings.Contains(string(encoded), "generated-response") {
				t.Error("queued evaluation lost previous bot reply")
			}
			fmt.Fprint(w, `{"id":"test-decision","answers":{"should_reply":{"type":"noul","noul":0.9}}}`)
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	if err := os.MkdirAll(filepath.Join(home, "plugins"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, plugin := range []string{"decision", "gateway"} {
		args := []string{"build", "-o", filepath.Join(home, "plugins", "horizon-"+plugin)}
		if plugin == "gateway" {
			args = append(args, "-ldflags", "-X main.telegramEndpoint="+server.URL)
		}
		args = append(args, "./plugins/"+plugin)
		cmd := exec.Command("go", args...)
		cmd.Dir = "../.."
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v %s", plugin, err, output)
		}
	}
	for _, args := range [][]string{{"gateway", "--help"}, {"gateway", "send", "--help"}} {
		out, diag, err := run(binary, cwd, "", append([]string{"--home", home}, args...)...)
		if err != nil || diag != "" || !strings.Contains(out, "Usage:") {
			t.Fatalf("help %v: %v %q %q", args, err, out, diag)
		}
	}
	if out, diag, err := run(binary, cwd, "", "--home", home, "init"); err != nil {
		t.Fatalf("init %v %s %s", err, out, diag)
	}
	cfg := fmt.Sprintf(`mode: unit
provider:
  url: %s
  key: local-key
default_model: chat
models:
  chat:
    model: model-test
    compact_threshold: 100000
decision:
  provider:
    url: %s
    key: local-key
  model: jev
plugins:
  gateway:
    telegram:
      bot_token: test-token
      owner_user_id: 1
    group_defaults:
      owner_only: false
      response_mode: conversation
`, server.URL, server.URL)
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	if out, diag, err := run(binary, cwd, "", "--home", home, "gateway", "init"); err != nil {
		t.Fatalf("gateway init %v %s %s", err, out, diag)
	}
	start := func() func() {
		cmd := exec.Command(binary, "--home", home, "gateway", "start")
		cmd.Dir = cwd
		cmd.Env = filteredEnv("HORIZON_INHERITED_ACCESS")
		var diagnostics bytes.Buffer
		cmd.Stderr = &diagnostics
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		stopped := false
		stop := func() {
			if stopped {
				return
			}
			stopped = true
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case err := <-done:
				if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 143 {
					t.Errorf("gateway shutdown: %v %s", err, diagnostics.String())
				}
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				<-done
				t.Errorf("gateway did not stop: %s", diagnostics.String())
			}
		}
		t.Cleanup(stop)
		return stop
	}
	waitFor := func(condition func() bool) {
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if condition() {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("gateway workflow timed out")
	}
	stop := start()
	waitFor(func() bool { mu.Lock(); defer mu.Unlock(); return len(sent) == 3 })
	out, diag, err := run(binary, cwd, "", "--home", home, "gateway", "list", "--json")
	var registry struct {
		Chats []struct {
			ID        string `json:"chat_id"`
			Name      string `json:"name"`
			Workspace string `json:"workspace"`
			Session   string `json:"session_id"`
		}
	}
	if err != nil || diag != "" || json.Unmarshal([]byte(out), &registry) != nil || len(registry.Chats) != 2 {
		t.Fatalf("registry: %v %s %s", err, out, diag)
	}
	if registry.Chats[0].Name != "Test group" || registry.Chats[1].Name != "Owner" || registry.Chats[0].Session == registry.Chats[1].Session || registry.Chats[0].Workspace == registry.Chats[1].Workspace {
		t.Fatalf("chat bindings: %+v", registry.Chats)
	}
	original, _ := json.Marshal(registry)
	if out, diag, err := run(binary, cwd, "", "--home", home, "gateway", "send", "--chat", "-10", "-m", "manual-instruction", "--wait", "--json"); err != nil || !strings.Contains(out, `"status":"sent"`) {
		t.Fatalf("manual send: %v %s %s", err, out, diag)
	}
	mu.Lock()
	if modelCalls != 4 || decisionCalls != 2 {
		t.Errorf("calls: models=%d decisions=%d", modelCalls, decisionCalls)
	}
	mu.Unlock()
	if out, diag, err := run(binary, cwd, "", "--home", home, "gateway", "start"); err == nil || !strings.Contains(diag, "already running") {
		t.Fatalf("duplicate start: %v %s %s", err, out, diag)
	}
	stop()
	stop = start()
	mu.Lock()
	before := polls
	mu.Unlock()
	waitFor(func() bool { mu.Lock(); defer mu.Unlock(); return polls > before+1 })
	out, diag, err = run(binary, cwd, "", "--home", home, "gateway", "list", "--json")
	if err != nil || json.Unmarshal([]byte(out), &registry) != nil {
		t.Fatalf("restored list: %v %s %s", err, out, diag)
	}
	restored, _ := json.Marshal(registry)
	if !bytes.Equal(original, restored) {
		t.Errorf("chat bindings changed on restart: %s %s", original, restored)
	}
	stop()
	mu.Lock()
	defer mu.Unlock()
	if modelCalls != 4 || len(sent) != 4 {
		t.Errorf("restart repeated effects: models=%d sends=%d", modelCalls, len(sent))
	}
}
