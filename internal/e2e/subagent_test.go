package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ReanSn0w/horizon/internal/daemon"
	"github.com/ReanSn0w/horizon/internal/responses"
)

func TestBuiltSubagentModelDelegation(t *testing.T) {
	home := daemonHome(t)
	workspace := t.TempDir()
	binary := buildBinary(t)
	installTestBinary(t, binary, filepath.Join(home, "bin", "horizon"))
	cmd := exec.Command("go", "build", "-o", filepath.Join(home, "bin", "horizon-subagent"), "./plugins/subagent")
	cmd.Dir = "../.."
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("build subagent: %v %s", e, b)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/responses/compact" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"compact","object":"response.compaction","output":[{"type":"compaction","id":"summary","encrypted_content":"opaque"}]}`)
			return
		}
		var req responses.Request
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("invalid request")
			return
		}
		n := int(calls.Add(1))
		parent := false
		childID := ""
		complete := false
		for _, item := range req.Input {
			var value struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
				Type    string          `json:"type"`
				CallID  string          `json:"call_id"`
				Output  string          `json:"output"`
			}
			json.Unmarshal(item, &value)
			if value.Role == "user" && strings.Contains(string(value.Content), "DELEGATE_PARENT") {
				parent = true
			}
			if value.Type == "function_call_output" {
				var output struct {
					OK    bool            `json:"ok"`
					Data  json.RawMessage `json:"data"`
					Error any             `json:"error"`
				}
				json.Unmarshal([]byte(value.Output), &output)
				if !output.OK {
					t.Errorf("tool failed: %s", value.Output)
				}
				if strings.HasPrefix(value.CallID, "spawn-") {
					var child daemon.Task
					json.Unmarshal(output.Data, &child)
					childID = child.ID
				}
				if strings.HasPrefix(value.CallID, "read-") {
					var got struct {
						Task daemon.Task `json:"task"`
						Text string      `json:"text"`
					}
					json.Unmarshal(output.Data, &got)
					if got.Task.State == "succeeded" && got.Text == "leaf result" {
						complete = true
					}
				}
			}
		}
		if !parent {
			writeCompleted(w, n, message("leaf result"))
			return
		}
		if complete {
			writeCompleted(w, n, message("parent collected leaf"))
			return
		}
		name := "subagent__spawn"
		id := fmt.Sprintf("spawn-%d", n)
		args := map[string]any{"message": "LEAF_TASK", "access": "write"}
		if childID != "" {
			name = "subagent__read"
			id = fmt.Sprintf("read-%d", n)
			args = map[string]any{"task_id": childID, "operation_id": "", "offset": 0}
		}
		a, _ := json.Marshal(args)
		item, _ := json.Marshal(map[string]any{"type": "function_call", "call_id": id, "name": name, "arguments": string(a)})
		writeCompleted(w, n, []json.RawMessage{item})
	}))
	defer server.Close()
	writeConfig(t, home, server.URL)
	f, e := os.OpenFile(filepath.Join(home, "config.yaml"), os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	f.WriteString("\nagent_plugins: [subagent]\nplugins:\n  daemon:\n    parallel_per_depth: 1\n")
	f.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- daemon.Start(ctx, home) }()
	defer func() {
		cancel()
		if e := <-done; e != nil {
			t.Error(e)
		}
	}()
	for i := 0; i < 200; i++ {
		if daemon.Hello(ctx, home) == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Parent is a normal foreground Horizon, outside daemon supervision.
	command := exec.CommandContext(ctx, binary, "--home", home, "resume", "--mode", "plain", "-m", "DELEGATE_PARENT")
	command.Dir = workspace
	output, e := command.CombinedOutput()
	if e != nil || strings.TrimSpace(string(output)) != "parent collected leaf" {
		t.Fatalf("external parent: %v %s", e, output)
	}
	before := calls.Load()
	for _, args := range [][]string{{"--home", home, "resume", "-m", ""}, {"--home", home, "--help"}, {"--home", home, "sessions", "compact"}} {
		check := exec.CommandContext(ctx, binary, args...)
		check.Dir = workspace
		out, err := check.CombinedOutput()
		if args[len(args)-1] == "compact" && err != nil {
			t.Fatalf("compact: %v %s", err, out)
		}
	}
	if calls.Load() != before {
		t.Fatal("empty input/help/compact started an agent turn")
	}
	// A daemon-owned parent at capacity must still allow its nested child to run.
	caller := daemon.Caller{Kind: "subagent", Workspace: workspace, Session: "outer-parent", Access: "write"}
	accepted, e := daemon.Call(ctx, home, daemon.Request{ID: "nested", Method: "submit", OperationID: "nested", Caller: caller, Message: "DELEGATE_PARENT"})
	if e != nil {
		t.Fatal(e)
	}
	var task daemon.Task
	json.Unmarshal(accepted.Data, &task)
	for i := 0; i < 500; i++ {
		response, e := daemon.Call(ctx, home, daemon.Request{ID: "read", Method: "read", Caller: caller, TaskID: task.ID})
		if e != nil {
			t.Fatal(e)
		}
		var got struct {
			Task daemon.Task `json:"task"`
			Text string      `json:"text"`
		}
		json.Unmarshal(response.Data, &got)
		if got.Task.State == "succeeded" {
			if got.Text != "parent collected leaf" {
				t.Fatal(got.Text)
			}
			return
		}
		if got.Task.State == "failed" {
			t.Fatal("nested parent failed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("nested child starved")
}
