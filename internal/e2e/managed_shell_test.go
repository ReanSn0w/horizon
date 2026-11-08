package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestBuiltBinaryManagedShellProcess(t *testing.T) {
	binary := buildBinary(t)
	home, workspace := t.TempDir(), t.TempDir()
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests++
		var body struct {
			Input []json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		var output []json.RawMessage
		switch requests {
		case 1:
			output = []json.RawMessage{managedCall(t, "start", "shell_exec", map[string]any{
				"command": "printf start; sleep 1; printf end", "timeout_ms": nil,
				"max_output_chars": nil, "yield_time_ms": 1,
			})}
		case 2:
			var processID string
			for _, item := range body.Input {
				var call struct {
					Type   string `json:"type"`
					CallID string `json:"call_id"`
					Output string `json:"output"`
				}
				if json.Unmarshal(item, &call) == nil && call.Type == "function_call_output" && call.CallID == "start" {
					var result struct {
						Data struct {
							ProcessID string `json:"process_id"`
							Status    string `json:"status"`
						} `json:"data"`
					}
					_ = json.Unmarshal([]byte(call.Output), &result)
					if result.Data.Status != "running" {
						t.Errorf("start returned %s", call.Output)
					}
					processID = result.Data.ProcessID
				}
			}
			if processID == "" {
				t.Error("missing process ID in start result")
			}
			output = []json.RawMessage{managedCall(t, "wait", "shell_wait", map[string]any{"process_id": processID, "wait_ms": 2000})}
		case 3:
			found := false
			for _, item := range body.Input {
				var call struct {
					Type   string `json:"type"`
					CallID string `json:"call_id"`
					Output string `json:"output"`
				}
				if json.Unmarshal(item, &call) == nil && call.Type == "function_call_output" && call.CallID == "wait" && strings.Contains(call.Output, `"status":"completed"`) && strings.Contains(call.Output, "end") {
					found = true
				}
			}
			if !found {
				t.Error("model did not receive final process output")
			}
			output = message("finished")
		default:
			t.Errorf("unexpected model request %d", requests)
			output = message("unexpected")
		}
		writeCompleted(writer, requests, output)
	}))
	defer server.Close()
	writeConfig(t, home, server.URL)
	stdout, stderr, err := run(binary, workspace, "", "--home", home, "resume", "--mode", "jsonl", "--access", "full", "-m", "run long command")
	if err != nil || stderr != "" || requests != 3 {
		t.Fatalf("resume err=%v stderr=%q requests=%d stdout=%q", err, stderr, requests, stdout)
	}
	for _, marker := range []string{`"type":"tool_completed"`, `"status":"running"`, `"status":"completed"`, `"text":"finished"`} {
		if !strings.Contains(stdout, marker) {
			t.Fatalf("jsonl missing %s: %s", marker, stdout)
		}
	}
}

func managedCall(t *testing.T, callID, name string, arguments any) json.RawMessage {
	t.Helper()
	args, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	item, err := json.Marshal(map[string]any{"type": "function_call", "call_id": callID, "name": name, "arguments": string(args)})
	if err != nil {
		t.Fatal(err)
	}
	return item
}
