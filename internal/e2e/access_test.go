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
)

func TestBuiltBinaryAccessAndJevGate(t *testing.T) {
	binary := buildBinary(t)
	for _, tc := range []struct {
		name, requested, inherited, command, access, decision string
		allowed                                               bool
	}{
		{"default write reads", "", "", "cat note.txt", "write", "allow", true},
		{"read reads", "read", "", "cat note.txt", "read", "allow", true},
		{"read rejects write", "read", "", "touch marker", "read", "deny", false},
		{"write permits home", "write", "", "touch \"$HORIZON_HOME/marker\"", "write", "allow", true},
		{"write rejects outside", "write", "", "touch %OUTSIDE%", "write", "deny", false},
		{"full permits outside", "full", "", "touch %OUTSIDE%", "full", "allow", true},
		{"inherited read overrides full", "full", "read", "touch marker", "read", "deny", false},
		{"inherited full overrides read", "read", "full", "printf full", "full", "allow", true},
		{"provider error fails closed", "write", "", "touch marker", "write", "error", false},
		{"incomplete decision fails closed", "write", "", "touch marker", "write", "incomplete", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace, home := t.TempDir(), t.TempDir()
			outside := filepath.Join(t.TempDir(), "marker")
			commandText := strings.ReplaceAll(tc.command, "%OUTSIDE%", shellQuote(outside))
			resolvedWorkspace, err := filepath.EvalSymlinks(workspace)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("hello"), 0600); err != nil {
				t.Fatal(err)
			}
			decisionCalls := 0
			decisionServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				decisionCalls++
				var request struct {
					State     map[string]string          `json:"state"`
					Questions map[string]json.RawMessage `json:"questions"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if len(request.State) != 3 || request.State["command"] != commandText || request.State["horizon_home"] != home || request.State["workspace"] != resolvedWorkspace || len(request.Questions) != 2 {
					t.Errorf("review context = %+v", request.State)
				}
				if tc.decision == "error" {
					http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
					return
				}
				answers := map[string]any{}
				for id := range request.Questions {
					probability := 0.01
					if tc.decision == "deny" && (tc.access == "read" && id == "file_write" || tc.access == "write" && id == "outside_write") {
						probability = 0.99
					}
					answers[id] = map[string]any{"type": "noul", "noul": probability}
				}
				if tc.decision == "incomplete" {
					delete(answers, "outside_write")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "test-decision", "answers": answers})
			}))
			defer decisionServer.Close()
			responseCalls := 0
			var toolOutput string
			responseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				responseCalls++
				var request responses.Request
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if responseCalls == 1 {
					args, _ := json.Marshal(map[string]any{"command": commandText, "timeout_ms": 10000, "max_output_chars": 16000})
					call, _ := json.Marshal(map[string]any{"type": "function_call", "call_id": "probe", "name": "shell_exec", "arguments": string(args)})
					writeCompleted(w, responseCalls, []json.RawMessage{call})
					return
				}
				for _, item := range request.Input {
					var value struct{ Type, Output string }
					_ = json.Unmarshal(item, &value)
					if value.Type == "function_call_output" {
						toolOutput = value.Output
					}
				}
				writeCompleted(w, responseCalls, message("done"))
			}))
			defer responseServer.Close()
			config := fmt.Sprintf("mode: unit\ndefault_model: coding\nmodels:\n  coding:\n    model: test-model\n    compact_threshold: 1000\nprovider:\n  url: %s\n  key: test-key\ndecision:\n  provider:\n    url: %s\n    key: decision-key\n  model: typesafe/jev-1.13\n", responseServer.URL, decisionServer.URL)
			if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"--home", home, "resume", "--mode", "plain", "-m", "test command"}
			if tc.requested != "" {
				args = append(args, "--access", tc.requested)
			}
			command := exec.Command(binary, args...)
			command.Dir = workspace
			command.Env = filteredEnv("HORIZON_INHERITED_ACCESS")
			if tc.inherited != "" {
				command.Env = append(command.Env, "HORIZON_INHERITED_ACCESS="+tc.inherited)
			}
			output, err := command.CombinedOutput()
			wantDecisions := 1
			if tc.access == "full" {
				wantDecisions = 0
			}
			if err != nil || strings.TrimSpace(string(output)) != "done" || decisionCalls != wantDecisions || responseCalls != 2 {
				t.Fatalf("CLI output=%q err=%v decisions=%d responses=%d", output, err, decisionCalls, responseCalls)
			}
			if tc.allowed && !strings.Contains(toolOutput, `"ok":true`) || !tc.allowed && !strings.Contains(toolOutput, `"ok":false`) {
				t.Fatalf("tool output = %s", toolOutput)
			}
			if tc.access == "full" && strings.Contains(toolOutput, `"decision_id"`) {
				t.Fatalf("full access reported Jev decision: %s", toolOutput)
			}
			if _, err := os.Stat(filepath.Join(workspace, "marker")); err == nil && !tc.allowed {
				t.Fatal("rejected command created marker")
			}
			if tc.name == "write rejects outside" || tc.name == "full permits outside" {
				_, err := os.Stat(outside)
				if (err == nil) != tc.allowed {
					t.Fatalf("outside write: allowed=%t err=%v", tc.allowed, err)
				}
			}
			if tc.name == "write permits home" {
				if _, err := os.Stat(filepath.Join(home, "marker")); err != nil {
					t.Fatalf("home write missing: %v", err)
				}
			}
		})
	}
}

func filteredEnv(exclude string) []string {
	var result []string
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, exclude+"=") {
			result = append(result, item)
		}
	}
	return result
}

func TestBuiltBinaryNestedHorizonInheritsAccess(t *testing.T) {
	for _, tc := range []struct{ name, childFlag string }{{"no flag", ""}, {"conflicting flag", " --access full"}} {
		t.Run(tc.name, func(t *testing.T) {
			testNestedHorizonInheritsAccess(t, tc.childFlag)
		})
	}
}

func testNestedHorizonInheritsAccess(t *testing.T, childFlag string) {
	binary := buildBinary(t)
	workspace, home := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var reviewed []string
	decisionServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			State     map[string]string          `json:"state"`
			Questions map[string]json.RawMessage `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		mu.Lock()
		reviewed = append(reviewed, request.State["command"])
		mu.Unlock()
		answers := map[string]any{}
		for id := range request.Questions {
			answers[id] = map[string]any{"type": "noul", "noul": 0.01}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "nested-decision", "answers": answers})
	}))
	defer decisionServer.Close()
	var calls int
	var childResult string
	responseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		callNumber := calls
		mu.Unlock()
		var request responses.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		toolCall := func(command string) []json.RawMessage {
			args, _ := json.Marshal(map[string]any{"command": command, "timeout_ms": 10000, "max_output_chars": 16000})
			value, _ := json.Marshal(map[string]any{"type": "function_call", "call_id": fmt.Sprintf("call-%d", callNumber), "name": "shell_exec", "arguments": string(args)})
			return []json.RawMessage{value}
		}
		switch callNumber {
		case 1:
			writeCompleted(w, callNumber, toolCall("cd sub && horizon resume"+childFlag+" --mode plain -m inner"))
		case 2:
			writeCompleted(w, callNumber, toolCall("printf '%s' \"$HORIZON_INHERITED_ACCESS\""))
		case 3:
			for _, item := range request.Input {
				var value struct{ Type, Output string }
				_ = json.Unmarshal(item, &value)
				if value.Type == "function_call_output" {
					mu.Lock()
					childResult = value.Output
					mu.Unlock()
				}
			}
			writeCompleted(w, callNumber, message("child done"))
		default:
			writeCompleted(w, callNumber, message("parent done"))
		}
	}))
	defer responseServer.Close()
	config := fmt.Sprintf("mode: unit\ndefault_model: coding\nmodels:\n  coding:\n    model: test-model\n    compact_threshold: 1000\nprovider:\n  url: %s\n  key: test-key\ndecision:\n  provider:\n    url: %s\n    key: decision-key\n  model: typesafe/jev-1.13\n", responseServer.URL, decisionServer.URL)
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "--home", home, "resume", "--access", "write", "--mode", "plain", "-m", "outer")
	command.Dir = workspace
	command.Env = filteredEnv("HORIZON_INHERITED_ACCESS")
	output, err := command.CombinedOutput()
	mu.Lock()
	defer mu.Unlock()
	if err != nil || strings.TrimSpace(string(output)) != "parent done" || calls != 4 || len(reviewed) != 2 || !strings.HasPrefix(reviewed[0], "cd sub && horizon resume") || reviewed[1] != "printf '%s' \"$HORIZON_INHERITED_ACCESS\"" || !strings.Contains(childResult, `"stdout":"write"`) {
		t.Fatalf("nested result: output=%q err=%v calls=%d reviewed=%q child=%s", output, err, calls, reviewed, childResult)
	}
}
