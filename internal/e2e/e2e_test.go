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
	"testing"
	"time"

	"github.com/ReanSn0w/horizon/internal/responses"
	"github.com/ReanSn0w/horizon/internal/session"
	"github.com/creack/pty"
)

func TestBuiltBinaryRepositoryWorkflow(t *testing.T) {
	binary := buildBinary(t)
	workspace, home := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("LOCAL_SENTINEL"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "skills", "review"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "skills", "review", "SKILL.md"), []byte("---\nname: review\ndescription: SKILL_SENTINEL\n---\nReview carefully.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	responseCalls := 0
	compactSeenOnResume := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if request.URL.Path == "/responses/compact" {
			writer.Header().Set("Content-Type", "application/json")
			fmt.Fprint(writer, `{"id":"manual-response","object":"response.compaction","output":[{"role":"user","content":"retained"},{"type":"compaction","id":"manual-compact","encrypted_content":"opaque"}]}`)
			return
		}
		responseCalls++
		var body responses.Request
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if !strings.Contains(body.Instructions, "LOCAL_SENTINEL") || !strings.Contains(body.Instructions, "SKILL_SENTINEL") {
			t.Errorf("request %d lost instructions: %q", responseCalls, body.Instructions)
		}
		var output []json.RawMessage
		switch responseCalls {
		case 1:
			if body.Model != "model-fast" {
				t.Errorf("selected model = %q", body.Model)
			}
			output = []json.RawMessage{json.RawMessage(`{"type":"function_call","call_id":"read","name":"file_read","arguments":"{\"path\":\"note.txt\",\"start_line\":1,\"line_count\":20}"}`)}
		case 2:
			output = []json.RawMessage{json.RawMessage(`{"type":"function_call","call_id":"update","name":"file_update","arguments":"{\"path\":\"note.txt\",\"old_text\":\"old\\n\",\"new_text\":\"new\\n\"}"}`)}
		case 3:
			output = []json.RawMessage{json.RawMessage(`{"type":"function_call","call_id":"shell","name":"shell_exec","arguments":"{\"command\":\"cat note.txt\",\"timeout_ms\":10000,\"max_output_chars\":16000}"}`)}
		case 4:
			output = message("done")
		case 5:
			if body.Model != "model-code" {
				t.Errorf("second-turn model = %q", body.Model)
			}
			output = message("continued")
		default:
			for _, item := range body.Input {
				var header struct {
					Type string `json:"type"`
					ID   string `json:"id"`
				}
				_ = json.Unmarshal(item, &header)
				compactSeenOnResume = compactSeenOnResume || header.Type == "compaction" && header.ID == "manual-compact"
			}
			output = message("after compact")
		}
		writeCompleted(writer, responseCalls, output)
	}))
	defer server.Close()
	writeConfig(t, home, server.URL)

	stdout, stderr, err := run(binary, workspace, "", "--home", home, "resume", "--model", "fast", "-m", "update note")
	if err != nil || stdout != "done\n" || !strings.Contains(stderr, "file_read: note.txt") || !strings.Contains(stderr, "shell_exec: cat note.txt") {
		t.Fatalf("first resume stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "note.txt")); err != nil || string(data) != "new\n" {
		t.Fatalf("repository change=%q err=%v", data, err)
	}

	answerPath := filepath.Join(t.TempDir(), "answer.txt")
	answer, err := os.Create(answerPath)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "--home", home, "resume")
	command.Dir = workspace
	command.Stdin = strings.NewReader("continue via stdin\n")
	command.Stdout = answer
	var redirectedProgress bytes.Buffer
	command.Stderr = &redirectedProgress
	err = command.Run()
	_ = answer.Close()
	if err != nil {
		t.Fatalf("stdin resume: %v: %s", err, redirectedProgress.Bytes())
	}
	answerData, _ := os.ReadFile(answerPath)
	if string(answerData) != "continued\n" || strings.Contains(string(answerData), "done") || redirectedProgress.Len() == 0 {
		t.Fatalf("redirected answer=%q progress=%q", answerData, redirectedProgress.Bytes())
	}

	store := session.NewStore(home)
	resolved, _ := store.ResolveWorkspace(workspace)
	items, err := store.List(resolved, false)
	if err != nil || len(items) != 1 || len(items[0].Session.Turns) != 2 {
		t.Fatalf("stored session items=%+v err=%v", items, err)
	}
	sessionID := items[0].Session.SessionID
	locked, err := store.LockSession(resolved, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, busyErr := run(binary, workspace, "", "--home", home, "resume", "--session", sessionID, "-m", "busy")
	_ = locked.Close()
	if busyErr == nil {
		t.Fatal("second resume entered a locked session")
	}

	stdout, _, err = run(binary, workspace, "", "--home", home, "sessions", "compact", "--id", sessionID)
	if err != nil || !strings.Contains(stdout, "сжата") {
		t.Fatalf("manual compact stdout=%q err=%v", stdout, err)
	}
	stdout, _, err = run(binary, workspace, "", "--home", home, "resume", "--session", sessionID, "-m", "after compact")
	mu.Lock()
	seenCompact := compactSeenOnResume
	mu.Unlock()
	if err != nil || stdout != "after compact\n" || !seenCompact {
		t.Fatalf("resume after compact stdout=%q compact=%t err=%v", stdout, seenCompact, err)
	}

	forkID, _, err := run(binary, workspace, "", "--home", home, "sessions", "create", "--fork", sessionID)
	if err != nil {
		t.Fatal(err)
	}
	forkID = strings.TrimSpace(forkID)
	if _, _, err := run(binary, workspace, "", "--home", home, "sessions", "delete", "--id", sessionID); err != nil {
		t.Fatal(err)
	}
	history, _, err := run(binary, workspace, "", "--home", home, "sessions", "read", "--id", forkID, "--turns", "4")
	if err != nil || !strings.Contains(history, "continued") || !strings.Contains(history, "after compact") {
		t.Fatalf("fork history=%q err=%v", history, err)
	}
}

func TestBuiltBinaryInteractiveEditor(t *testing.T) {
	binary := buildBinary(t)
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", cancel), func(t *testing.T) {
			workspace, home := t.TempDir(), t.TempDir()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writeCompleted(writer, 1, message("interactive done"))
			}))
			defer server.Close()
			writeConfig(t, home, server.URL)
			command := exec.Command(binary, "--home", home)
			command.Dir = workspace
			terminal, err := pty.Start(command)
			if err != nil {
				t.Fatal(err)
			}
			defer terminal.Close()
			var output bytes.Buffer
			ready := make(chan struct{})
			doneReading := make(chan struct{})
			go func() {
				buffer := make([]byte, 2048)
				announced := false
				for {
					count, readErr := terminal.Read(buffer)
					if count > 0 {
						output.Write(buffer[:count])
						if !announced && strings.Contains(output.String(), "Horizon") {
							announced = true
							close(ready)
						}
					}
					if readErr != nil {
						close(doneReading)
						return
					}
				}
			}()
			select {
			case <-ready:
			case <-time.After(5 * time.Second):
				_ = command.Process.Kill()
				t.Fatal("interactive editor did not start")
			}
			if cancel {
				_, _ = terminal.Write([]byte{3})
			} else {
				_, _ = terminal.Write([]byte("первая\x0aвторx\x7fая\x1b[A\x1b[B\r"))
			}
			wait := make(chan error, 1)
			go func() { wait <- command.Wait() }()
			select {
			case err := <-wait:
				if cancel {
					if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 130 {
						t.Fatalf("cancel: %v", err)
					}
				} else if err != nil {
					t.Fatalf("interactive binary: %v: %s", err, output.Bytes())
				}
			case <-time.After(10 * time.Second):
				_ = command.Process.Kill()
				t.Fatal("interactive binary did not exit")
			}
			<-doneReading
			if cancel {
				return
			}
			store := session.NewStore(home)
			resolved, _ := store.ResolveWorkspace(workspace)
			items, err := store.List(resolved, false)
			if err != nil || len(items) != 1 || items[0].Session.Turns[0].Messages[0].Text != "первая\nвторая" {
				t.Fatalf("interactive message items=%+v err=%v output=%q", items, err, output.Bytes())
			}
		})
	}
}

func buildBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "horizon")
	command := exec.Command("go", "build", "-o", binary, "../..")
	command.Dir = filepath.Join("..", "e2e")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build Horizon: %v: %s", err, output)
	}
	return binary
}

func writeConfig(t *testing.T, home, endpoint string) {
	t.Helper()
	config := fmt.Sprintf(`mode: unit
default_model: coding
models:
  coding:
    model: model-code
    compact_threshold: 1000
  fast:
    model: model-fast
    reasoning: low
    compact_threshold: 2000
provider:
  url: %s
  key: secret
decision:
  provider:
    url: %s
    key: decision-secret
  model: typesafe/jev-1.13
limits:
  max_model_requests: 16
  max_turn_duration: 30s
`, endpoint, endpoint)
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
}

func message(text string) []json.RawMessage {
	data, _ := json.Marshal(map[string]any{
		"type":    "message",
		"content": []any{map[string]any{"type": "output_text", "text": text}},
	})
	return []json.RawMessage{data}
}

func writeCompleted(writer http.ResponseWriter, number int, output []json.RawMessage) {
	writer.Header().Set("Content-Type", "text/event-stream")
	response, _ := json.Marshal(responses.Response{ID: fmt.Sprintf("response-%d", number), Status: "completed", Output: output})
	fmt.Fprintf(writer, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", response)
}

func run(binary, workspace, stdin string, arguments ...string) (string, string, error) {
	command := exec.Command(binary, arguments...)
	command.Dir = workspace
	command.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	return stdout.String(), stderr.String(), err
}

func TestBuiltBinaryOutputModes(t *testing.T) {
	binary := buildBinary(t)
	for _, mode := range []string{"text", "plain", "jsonl"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fail=%t", mode, fail), func(t *testing.T) {
				home, workspace := t.TempDir(), t.TempDir()
				os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("hello"), 0600)
				var count int
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					count++
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"DELTA_SENTINEL\"}\n\n")
					if fail {
						return
					}
					output := message("FINAL_SENTINEL")
					if count == 1 {
						output = []json.RawMessage{json.RawMessage(`{"type":"function_call","call_id":"read","name":"file_read","arguments":"{\"path\":\"note.txt\",\"start_line\":1,\"line_count\":20}"}`)}
					}
					response, _ := json.Marshal(responses.Response{ID: fmt.Sprint(count), Status: "completed", Output: output, Usage: json.RawMessage(`{"total_tokens":12}`)})
					fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", response)
				}))
				defer server.Close()
				writeConfig(t, home, server.URL)
				stdout, stderr, err := run(binary, workspace, "read note", "--home", home, "--mode", mode)
				if fail {
					if err == nil || stderr == "" || (mode != "jsonl" && stdout != "") {
						t.Fatalf("failure: %q %q %v", stdout, stderr, err)
					}
					return
				}
				if err != nil {
					t.Fatalf("%q %q %v", stdout, stderr, err)
				}
				if mode == "jsonl" {
					if stderr != "" || !strings.Contains(stdout, "DELTA_SENTINEL") || !strings.Contains(stdout, "total_tokens") {
						t.Fatalf("%q %q", stdout, stderr)
					}
				} else {
					if stdout != "FINAL_SENTINEL\n" || strings.Contains(stderr, "DELTA_SENTINEL") || strings.Contains(stderr, "usage:") {
						t.Fatalf("%q %q", stdout, stderr)
					}
					if mode == "plain" && stderr != "" {
						t.Fatalf("plain stderr=%q", stderr)
					}
					if mode == "text" && !strings.Contains(stderr, "✓ file_read") {
						t.Fatalf("missing tools: %q", stderr)
					}
				}
			})
		}
	}
}

func TestBuiltBinaryInitializationAndSkillCreation(t *testing.T) {
	binary := buildBinary(t)
	home := filepath.Join(t.TempDir(), "custom-home")
	workspace := t.TempDir()
	stdout, stderr, err := run(binary, workspace, "do not consume", "--home", home, "--mode", "plain")
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 2 || stdout != "" || !strings.Contains(stderr, "horizon init") {
		t.Fatalf("first launch: %q %q %v", stdout, stderr, err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("first launch created home: %v", err)
	}
	stdout, stderr, err = run(binary, workspace, "", "--home", home, "init")
	if err != nil || stderr != "" || !strings.Contains(stdout, filepath.Join(home, "config.yaml")) || !strings.Contains(stdout, filepath.Join(home, "AGENTS.md")) {
		t.Fatalf("init: %q %q %v", stdout, stderr, err)
	}
	for _, path := range []string{"config.yaml", "AGENTS.md", "dialogs", "skills/skill-creator/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(home, path)); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(home, "dialogs"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("first launch created sessions: %v %v", entries, err)
	}
	const content = "---\nname: review-go\ndescription: Review Go changes.\n---\nRead project instructions and inspect the diff.\n"
	target := filepath.Join(home, "skills", "review-go", "SKILL.md")
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		var request responses.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, "bad request", 400)
			return
		}
		if !strings.Contains(request.Instructions, "- skill-creator:") {
			t.Error("bundled skill absent from catalog")
		}
		call := func(id, name string, args any) []json.RawMessage {
			arguments, _ := json.Marshal(args)
			item, _ := json.Marshal(map[string]any{"type": "function_call", "call_id": id, "name": name, "arguments": string(arguments)})
			return []json.RawMessage{item}
		}
		toolResult := func(id string) map[string]any {
			for _, raw := range request.Input {
				var item struct {
					Type, Output string
					CallID       string `json:"call_id"`
				}
				if err := json.Unmarshal(raw, &item); err != nil {
					t.Error(err)
					continue
				}
				if item.Type == "function_call_output" && item.CallID == id {
					var result struct {
						OK   bool
						Data map[string]any
					}
					if err := json.Unmarshal([]byte(item.Output), &result); err != nil || !result.OK {
						t.Errorf("tool %s result=%s err=%v", id, item.Output, err)
					}
					return result.Data
				}
			}
			t.Errorf("missing tool result %s", id)
			return nil
		}
		var output []json.RawMessage
		switch calls {
		case 1:
			output = call("read-creator", "skill_read", map[string]any{"name": "skill-creator"})
		case 2:
			data := toolResult("read-creator")
			if data["base_dir"] != filepath.Join(home, "skills", "skill-creator") || data["content"] == "" {
				t.Errorf("creator result=%v", data)
			}
			output = call("create-skill", "file_create", map[string]any{"path": target, "content": content})
		case 3:
			toolResult("create-skill")
			if strings.Contains(request.Instructions, "- review-go:") {
				t.Error("snapshot changed during turn")
			}
			output = message("skill created")
		case 4:
			if !strings.Contains(request.Instructions, "- review-go:") {
				t.Error("new skill absent on next turn")
			}
			output = call("read-new", "skill_read", map[string]any{"name": "review-go"})
		case 5:
			data := toolResult("read-new")
			if data["base_dir"] != filepath.Dir(target) || !strings.Contains(fmt.Sprint(data["content"]), "inspect the diff") {
				t.Errorf("new skill=%v", data)
			}
			output = message("skill available")
		default:
			t.Errorf("unexpected request %d", calls)
			http.Error(w, "unexpected", 400)
			return
		}
		writeCompleted(w, calls, output)
	}))
	defer server.Close()
	writeConfig(t, home, server.URL)
	for _, want := range []string{"skill created", "skill available"} {
		stdout, stderr, err = run(binary, workspace, "create or use the skill", "--home", home, "--mode", "plain")
		if err != nil || stdout != want+"\n" || stderr != "" {
			t.Fatalf("configured launch: %q %q %v", stdout, stderr, err)
		}
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != content {
		t.Fatalf("created skill=%q err=%v", data, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 5 {
		t.Fatalf("requests=%d", calls)
	}
}
