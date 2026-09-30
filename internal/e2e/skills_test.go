package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/responses"
)

func TestBuiltBinarySkillManagementWorkflow(t *testing.T) {
	built := buildBinary(t)
	binary := filepath.Join(t.TempDir(), "horizon's executable")
	data, err := os.ReadFile(built)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, data, 0700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), "agent's home")
	workspace := t.TempDir()
	const id = "review folder"
	target := filepath.Join(home, "skills", id, "SKILL.md")
	creator := filepath.Join(home, "skills", "skill-creator", "SKILL.md")
	for _, dir := range []string{filepath.Dir(creator), filepath.Join(home, "skills", "broken")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	const oldCreator = "---\nname: skill-creator\ndescription: user maintained\n---\nKEEP THIS CUSTOM SKILL\n"
	if err := os.WriteFile(creator, []byte(oldCreator), 0600); err != nil {
		t.Fatal(err)
	}
	// An invalid disabled skill must not stop any agent turn.
	if err := os.WriteFile(filepath.Join(home, "skills", "broken", "SKILL.md"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
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
		active := calls >= 11 && calls <= 14
		if strings.Contains(request.Instructions, "- review-name:") != active {
			t.Errorf("call %d: wrong active snapshot", calls)
		}
		if strings.Contains(request.Instructions, "- skill-creator:") || strings.Contains(request.Instructions, "- broken:") {
			t.Error("disabled skills present in prompt")
		}
		call := func(callID, name string, args any) []json.RawMessage {
			arguments, _ := json.Marshal(args)
			item, _ := json.Marshal(map[string]any{"type": "function_call", "call_id": callID, "name": name, "arguments": string(arguments)})
			return []json.RawMessage{item}
		}
		result := func(id string) (bool, map[string]any) {
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
					var value struct {
						OK   bool
						Data map[string]any
					}
					if err := json.Unmarshal([]byte(item.Output), &value); err != nil {
						t.Error(err)
					}
					return value.OK, value.Data
				}
			}
			t.Errorf("missing result %s", id)
			return false, nil
		}
		shell := func(callID, operation string) []json.RawMessage {
			// Use the command supplied to the model, including executable/home quoting.
			suffix := " skills " + operation + " --id 'SKILL_ID'"
			command := ""
			for _, line := range strings.Split(request.Instructions, "\n") {
				if strings.HasSuffix(line, suffix) {
					command = strings.TrimSuffix(line, "'SKILL_ID'") + "'" + id + "'"
					break
				}
			}
			if command == "" {
				t.Errorf("missing %s management instruction", operation)
			}
			return call(callID, "shell_exec", map[string]any{"command": command, "timeout_ms": 10000, "max_output_chars": 16000})
		}
		checkShell := func(id string, wantExit float64) {
			ok, data := result(id)
			if !ok || data["exit_code"] != wantExit {
				t.Errorf("%s result=%v ok=%t", id, data, ok)
			}
		}
		var output []json.RawMessage
		switch calls {
		case 1:
			output = call("create", "file_create", map[string]any{"path": target, "content": "---\nname: review-name\ndescription: 42\n---\nReview carefully.\n"})
		case 2:
			output = shell("invalid", "validate")
		case 3:
			checkShell("invalid", 1)
			_, data := result("invalid")
			if !strings.Contains(fmt.Sprint(data["stderr"]), "line 3") {
				t.Error("missing actionable validation diagnostic")
			}
			output = call("fix", "file_update", map[string]any{"path": target, "old_text": "description: 42", "new_text": "description: Review Go files"})
		case 4:
			output = shell("valid", "validate")
		case 5:
			checkShell("valid", 0)
			output = shell("disable-created", "disable")
		case 6:
			checkShell("disable-created", 0)
			output = call("read-created", "skill_read", map[string]any{"name": "review-name"})
		case 7:
			if ok, _ := result("read-created"); ok {
				t.Error("new skill leaked into existing snapshot")
			}
			output = message("created and validated")
		case 8:
			output = shell("enable-next", "enable")
		case 9:
			checkShell("enable-next", 0)
			output = call("read-enabled-too-soon", "skill_read", map[string]any{"name": "review-name"})
		case 10:
			if ok, _ := result("read-enabled-too-soon"); ok {
				t.Error("enable changed existing snapshot")
			}
			output = message("enabled for next turn")
		case 11:
			output = call("read-active", "skill_read", map[string]any{"name": "review-name"})
		case 12:
			ok, data := result("read-active")
			if !ok || !strings.Contains(fmt.Sprint(data["content"]), "Review carefully") {
				t.Errorf("active read: %v", data)
			}
			output = shell("disable-active", "disable")
		case 13:
			checkShell("disable-active", 0)
			output = call("read-still-active", "skill_read", map[string]any{"name": "review-name"})
		case 14:
			if ok, _ := result("read-still-active"); !ok {
				t.Error("disable changed existing snapshot")
			}
			output = message("disabled for next turn")
		case 15:
			output = message("disabled")
		default:
			t.Errorf("unexpected request %d", calls)
			http.Error(w, "unexpected", 400)
			return
		}
		writeCompleted(w, calls, output)
	}))
	defer server.Close()
	writeConfig(t, home, server.URL)
	file, err := os.OpenFile(filepath.Join(home, "config.yaml"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\n# retain disabled built-ins\ndisabled_skills: [broken, skill-creator]\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	for _, want := range []string{"created and validated", "enabled for next turn", "disabled for next turn", "disabled"} {
		out, diagnostics, err := run(binary, workspace, "", "--home", home, "--mode", "plain", "-m", "manage skills")
		if err != nil || out != want+"\n" || diagnostics != "" {
			t.Fatalf("out=%q errout=%q err=%v", out, diagnostics, err)
		}
	}
	current, err := os.ReadFile(creator)
	if err != nil || string(current) != oldCreator {
		t.Fatal("bootstrap overwrote user skill")
	}
	cfg, err := config.Load(home)
	if err != nil || len(cfg.DisabledSkills) != 3 {
		t.Fatalf("disabled=%v err=%v", cfg.DisabledSkills, err)
	}
	out, diagnostics, err := run(binary, workspace, "", "--home", home, "skills", "list")
	if err != nil || !strings.Contains(out, "review-name") || !strings.Contains(out, "false") || !strings.Contains(diagnostics, "broken") {
		t.Fatalf("list=%q diagnostics=%q err=%v", out, diagnostics, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 15 {
		t.Fatalf("requests=%d", calls)
	}
}
