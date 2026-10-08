package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ReanSn0w/horizon/internal/decision"
	"github.com/ReanSn0w/horizon/internal/eventstream"
	"github.com/ReanSn0w/horizon/internal/instructions"
	"github.com/ReanSn0w/horizon/internal/plugins"
	"github.com/ReanSn0w/horizon/internal/responses"
	"github.com/ReanSn0w/horizon/internal/session"
)

type fakeClient struct {
	outputs [][]json.RawMessage
	wait    bool
	calls   int
}

type fakeCompactClient struct {
	response *responses.Response
	err      error
}

type allowCommands struct{}

func (allowCommands) Review(context.Context, decision.Command) (decision.Verdict, error) {
	return decision.Verdict{Allowed: true, ID: "test-decision"}, nil
}

func (f fakeCompactClient) Compact(_ context.Context, _ responses.CompactRequest, before func() error) (*responses.Response, error) {
	if before != nil {
		if err := before(); err != nil {
			return nil, err
		}
	}
	return f.response, f.err
}

func (f *fakeClient) Stream(ctx context.Context, _ responses.Request, before func() error, _ func(responses.Event)) (*responses.Response, error) {
	if err := before(); err != nil {
		return nil, err
	}
	f.calls++
	if f.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if len(f.outputs) == 0 {
		return nil, errors.New("unexpected request")
	}
	output := f.outputs[0]
	f.outputs = f.outputs[1:]
	return &responses.Response{Status: "completed", Output: output}, nil
}

func TestRequestBudgetAllowsFinalResponseAndStopsAfterTools(t *testing.T) {
	final := json.RawMessage(`{"type":"message","content":[{"type":"output_text","text":"done"}]}`)
	runtime, cleanup := testRuntime(t, &fakeClient{outputs: [][]json.RawMessage{{final}}})
	defer cleanup()
	runtime.MaxRequests = 1
	result, err := runtime.Run(context.Background(), "finish")
	if err != nil || result.Text != "done" {
		t.Fatalf("last allowed final response: result=%+v err=%v", result, err)
	}

	call := json.RawMessage(`{"type":"function_call","call_id":"call-1","name":"skill_read","arguments":"{\"name\":\"missing\"}"}`)
	client := &fakeClient{outputs: [][]json.RawMessage{{call}}}
	runtime, cleanup = testRuntime(t, client)
	defer cleanup()
	runtime.MaxRequests = 1
	_, err = runtime.Run(context.Background(), "inspect")
	var runtimeError *Error
	if !errors.As(err, &runtimeError) || runtimeError.Code != "request_limit_exceeded" {
		t.Fatalf("tool on last request error = %v", err)
	}
	stored, loadErr := runtime.Locked.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	turn := stored.Turns[len(stored.Turns)-1]
	if len(turn.ToolCalls) != 1 || turn.ToolCalls[0].ResultState != session.ToolResultKnown {
		t.Fatalf("last-request tool result was not saved: %+v", turn.ToolCalls)
	}
}

func TestRuntimeDistinguishesCancellationAndTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runtime, cleanup := testRuntime(t, &fakeClient{wait: true})
	defer cleanup()
	var events []eventstream.Event
	runtime.Publish = func(event eventstream.Event) { events = append(events, event) }
	_, err := runtime.Run(ctx, "cancel")
	assertRuntimeCode(t, err, "turn_cancelled")
	stored, _ := runtime.Locked.Load()
	if stored.Turns[len(stored.Turns)-1].Status != session.StatusCancelled {
		t.Fatalf("cancelled turn status = %s", stored.Turns[len(stored.Turns)-1].Status)
	}
	if len(events) != 2 || events[0].Type != "turn_started" || events[1].Type != "turn_cancelled" {
		t.Fatalf("cancel event sequence = %+v", events)
	}

	runtime, cleanup = testRuntime(t, &fakeClient{wait: true})
	defer cleanup()
	runtime.MaxDuration = time.Millisecond
	_, err = runtime.Run(context.Background(), "timeout")
	assertRuntimeCode(t, err, "turn_timeout")
}

func TestTurnFinalization(t *testing.T) {
	final := json.RawMessage(`{"type":"message","content":[{"type":"output_text","text":"done"}]}`)
	for _, tc := range []struct {
		name       string
		client     *fakeClient
		cancel     bool
		maxTime    time.Duration
		wantError  string
		finalize   bool
		failFinish bool
	}{
		{name: "success", client: &fakeClient{outputs: [][]json.RawMessage{{final}}}, finalize: true},
		{name: "legacy", client: &fakeClient{outputs: [][]json.RawMessage{{final}}}},
		{name: "failure", client: &fakeClient{}, wantError: "model_request_failed", finalize: true},
		{name: "cancel", client: &fakeClient{wait: true}, cancel: true, wantError: "turn_cancelled", finalize: true},
		{name: "timeout", client: &fakeClient{wait: true}, maxTime: time.Millisecond, wantError: "turn_timeout", finalize: true},
		{name: "finalize failure", client: &fakeClient{outputs: [][]json.RawMessage{{final}}}, finalize: true, failFinish: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime, cleanup := testRuntime(t, tc.client)
			defer cleanup()
			runtime.Access = "full"
			runtime.MaxDuration = tc.maxTime
			marker := filepath.Join(t.TempDir(), "finalize.json")
			script := "#!/bin/sh\ncase \"$1\" in\nhorizon-plugin-agent-finalize) cat > '" + marker + "'; "
			if tc.failFinish {
				script += "echo '{\"ok\":false,\"error\":{\"code\":\"offline\",\"message\":\"offline\"}}';;\n"
			} else {
				script += "echo '{\"ok\":true,\"data\":{}}';;\n"
			}
			script += "esac\n"
			path := filepath.Join(t.TempDir(), "plugin")
			if err := os.WriteFile(path, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			effect := ""
			if tc.finalize {
				effect = "unrestricted"
			}
			runtime.Extensions = []plugins.Extension{{Name: "test", Path: path, Request: plugins.Request{Home: runtime.Store.Home, Workspace: runtime.Workspace.Dir, WorkspaceID: runtime.Workspace.ID}, Description: plugins.Description{FinalizeEffect: effect}}}
			var events []eventstream.Event
			runtime.Publish = func(event eventstream.Event) { events = append(events, event) }
			ctx := context.Background()
			if tc.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			result, err := runtime.Run(ctx, "test")
			if tc.wantError == "" {
				if err != nil || result.Text != "done" {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			} else if tc.wantError == "model_request_failed" {
				if err == nil {
					t.Fatal("expected model error")
				}
			} else {
				assertRuntimeCode(t, err, tc.wantError)
			}
			data, readErr := os.ReadFile(marker)
			if !tc.finalize {
				if !os.IsNotExist(readErr) {
					t.Fatalf("legacy plugin finalized: %v", readErr)
				}
				return
			}
			if readErr != nil {
				t.Fatal(readErr)
			}
			var request plugins.Request
			if err := plugins.Decode(data, &request); err != nil {
				t.Fatal(err)
			}
			if request.Home != runtime.Store.Home || request.Workspace != runtime.Workspace.Dir || request.WorkspaceID != runtime.Workspace.ID || request.SessionID != runtime.Session.SessionID || request.TurnID == "" || request.Access != "full" || request.OperationID == "" {
				t.Fatalf("finalize context = %+v", request)
			}
			failEvents := 0
			for _, event := range events {
				if event.Type == "plugin_finalize_failed" {
					failEvents++
				}
			}
			if (failEvents == 1) != tc.failFinish {
				t.Fatalf("finalize failure events=%d", failEvents)
			}
		})
	}
}

func TestAutomaticCompactionPrunesOnlyWorkingWindowAfterSuccess(t *testing.T) {
	compact := json.RawMessage(`{"type":"compaction","id":"cmp-1","encrypted_content":"opaque"}`)
	final := json.RawMessage(`{"type":"message","id":"msg-1","content":[{"type":"output_text","text":"done"}]}`)
	runtime, cleanup := testRuntime(t, &fakeClient{outputs: [][]json.RawMessage{{compact, final}}})
	defer cleanup()
	if _, err := runtime.Run(context.Background(), "long task"); err != nil {
		t.Fatal(err)
	}
	stored, err := runtime.Locked.Load()
	if err != nil {
		t.Fatal(err)
	}
	turn := stored.Turns[len(stored.Turns)-1]
	if len(turn.APIItems) != 2 {
		t.Fatalf("full journal lost items: %s", turn.APIItems)
	}
	if len(stored.Compactions) != 1 || stored.Compactions[0].ID != "cmp-1" || len(stored.Compactions[0].Items) != 2 {
		t.Fatalf("automatic compact was not committed: %+v", stored.Compactions)
	}
	if len(stored.Checkpoints[len(stored.Checkpoints)-1].Items) != 2 {
		t.Fatalf("checkpoint was not pruned: %s", stored.Checkpoints[len(stored.Checkpoints)-1].Items)
	}
}

func TestManualCompactionReplacesWindowWithoutCreatingTurn(t *testing.T) {
	final := json.RawMessage(`{"type":"message","id":"msg-1","content":[{"type":"output_text","text":"done"}]}`)
	runtime, cleanup := testRuntime(t, &fakeClient{outputs: [][]json.RawMessage{{final}}})
	defer cleanup()
	if _, err := runtime.Run(context.Background(), "task"); err != nil {
		t.Fatal(err)
	}
	before, err := runtime.Locked.Load()
	if err != nil {
		t.Fatal(err)
	}
	window := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":"task"}`),
		json.RawMessage(`{"type":"compaction","id":"manual-1","encrypted_content":"opaque"}`),
	}
	result, err := Compact(context.Background(), fakeCompactClient{response: &responses.Response{Output: window}}, runtime.Locked, before, "fresh instructions", 0, nil)
	if err != nil || !result.Compacted {
		t.Fatalf("manual compact result=%+v err=%v", result, err)
	}
	after, err := runtime.Locked.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Turns) != len(before.Turns) || !after.LastAccessedAt.Equal(before.LastAccessedAt) {
		t.Fatalf("manual compact changed user history metadata: before=%+v after=%+v", before, after)
	}
	if len(after.Compactions) != 1 || len(after.Compactions[0].Items) != len(window) {
		t.Fatalf("manual compact window = %+v", after.Compactions)
	}
	_, _, next, ok := SuccessfulWindow(after)
	if !ok || len(next) != 2 || !bytes.Contains(next[1], []byte(`"id": "manual-1"`)) {
		t.Fatalf("next resume does not use manual window: %s", next)
	}
}

func TestRuntimeHTTPToolChainPersistsOpaqueItems(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		var body responses.Request
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if body.ParallelToolCalls || len(body.Tools) != 4 || len(body.ContextManagement) != 1 {
			t.Errorf("runtime request contract: tools=%d parallel=%t context=%+v", len(body.Tools), body.ParallelToolCalls, body.ContextManagement)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		var output []json.RawMessage
		if requests == 1 {
			output = []json.RawMessage{
				json.RawMessage(`{"type":"reasoning","id":"reason-1","encrypted_content":"opaque","future":{"kept":true}}`),
				json.RawMessage(`{"type":"function_call","call_id":"create-1","name":"shell_exec","arguments":"{\"command\":\"printf hello > created.txt\",\"timeout_ms\":null,\"max_output_chars\":null}"}`),
				json.RawMessage(`{"type":"function_call","call_id":"read-1","name":"shell_exec","arguments":"{\"command\":\"cat created.txt\",\"timeout_ms\":null,\"max_output_chars\":null}"}`),
			}
		} else {
			functionOutputs := 0
			for _, item := range body.Input {
				var header struct {
					Type string `json:"type"`
				}
				_ = json.Unmarshal(item, &header)
				if header.Type == "function_call_output" {
					functionOutputs++
				}
			}
			if functionOutputs != 2 {
				t.Errorf("second request has %d function outputs", functionOutputs)
			}
			output = []json.RawMessage{json.RawMessage(`{"type":"message","id":"final-1","content":[{"type":"output_text","text":"finished"}]}`)}
		}
		encoded, _ := json.Marshal(responses.Response{ID: fmt.Sprintf("r%d", requests), Status: "completed", Output: output})
		fmt.Fprintf(writer, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", encoded)
	}))
	defer server.Close()
	client := &responses.Client{ResponsesURL: server.URL, APIKey: "secret", MaxAttempts: 1}
	runtime, cleanup := testRuntime(t, client)
	defer cleanup()
	result, err := runtime.Run(context.Background(), "create then read")
	if err != nil || result.Text != "finished" || requests != 2 {
		t.Fatalf("runtime result=%+v requests=%d err=%v", result, requests, err)
	}
	if data, err := os.ReadFile(filepath.Join(runtime.Workspace.Dir, "created.txt")); err != nil || string(data) != "hello" {
		t.Fatalf("tool chain file=%q err=%v", data, err)
	}
	stored, err := runtime.Locked.Load()
	if err != nil {
		t.Fatal(err)
	}
	items := stored.Turns[len(stored.Turns)-1].APIItems
	if len(items) != 4 || !bytes.Contains(items[0], []byte(`"future"`)) || !bytes.Contains(items[0], []byte(`"kept": true`)) {
		t.Fatalf("runtime journal did not preserve output: %s", items)
	}
}

func TestBuildInputKeepsFailedHistoryWithoutInventingUnknownResult(t *testing.T) {
	now := time.Now().UTC()
	value := &session.Session{Turns: []session.Turn{{
		ID: "failed-1", Status: session.StatusFailed, StartedAt: now, CompletedAt: &now,
		Messages: []session.Message{{Role: "user", Text: "change it"}},
		APIItems: []json.RawMessage{
			json.RawMessage(`{"type":"reasoning","id":"reason-1","encrypted_content":"opaque"}`),
			json.RawMessage(`{"type":"function_call","call_id":"unknown-1","name":"shell_exec","arguments":"{\"command\":\"touch marker\",\"timeout_ms\":null,\"max_output_chars\":null}"}`),
		},
		ToolCalls: []session.ToolCall{{CallID: "unknown-1", Name: "shell_exec", Arguments: json.RawMessage(`{"command":"touch marker","timeout_ms":null,"max_output_chars":null}`), ResultState: session.ToolResultUnknown}},
		Error:     &session.TurnError{Code: "process_interrupted", Message: "stopped"},
	}}}
	input, err := BuildInput(value)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(input)
	if bytes.Contains(encoded, []byte("function_call_output")) || bytes.Contains(encoded, []byte(`"call_id":"unknown-1"`)) || bytes.Contains(encoded, []byte("reason-1")) {
		t.Fatalf("unknown call was represented as a completed API pair: %s", encoded)
	}
	if !bytes.Contains(encoded, []byte("unknown outcome")) || !bytes.Contains(encoded, []byte("inspect the actual workspace state")) {
		t.Fatalf("unknown outcome warning missing: %s", encoded)
	}
}

func TestBuildInputRetainsCompletedLegacyFileToolCall(t *testing.T) {
	now := time.Now().UTC()
	call := json.RawMessage(`{"type":"function_call","call_id":"legacy-1","name":"file_read","arguments":"{\"path\":\"note.txt\"}"}`)
	result := json.RawMessage(`{"ok":true,"data":{"content":"old"}}`)
	value := &session.Session{Turns: []session.Turn{{
		ID: "legacy", Status: session.StatusFailed, StartedAt: now, CompletedAt: &now,
		Messages:  []session.Message{{Role: "user", Text: "read note"}},
		APIItems:  []json.RawMessage{call},
		ToolCalls: []session.ToolCall{{CallID: "legacy-1", Name: "file_read", Arguments: json.RawMessage(`{"path":"note.txt"}`), ResultState: session.ToolResultKnown, Result: result}},
	}}}
	input, err := BuildInput(value)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(input)
	if !bytes.Contains(encoded, []byte(`file_read`)) || !bytes.Contains(encoded, []byte(`function_call_output`)) || !bytes.Contains(encoded, []byte(`legacy-1`)) {
		t.Fatalf("legacy history was lost: %s", encoded)
	}
}

func TestBuildInputDoesNotPromoteCompactionFromFailedTurn(t *testing.T) {
	now := time.Now().UTC()
	value := &session.Session{Turns: []session.Turn{{
		ID: "failed-compact", Status: session.StatusFailed, StartedAt: now, CompletedAt: &now,
		Messages: []session.Message{{Role: "user", Text: "continue"}},
		APIItems: []json.RawMessage{
			json.RawMessage(`{"type":"compaction","id":"must-not-use","encrypted_content":"opaque"}`),
			json.RawMessage(`{"type":"message","content":[{"type":"output_text","text":"partial"}]}`),
		},
		Error: &session.TurnError{Code: "later_failure", Message: "failed after compact"},
	}}}
	input, err := BuildInput(value)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(input)
	if bytes.Contains(encoded, []byte("must-not-use")) || !bytes.Contains(encoded, []byte("partial")) {
		t.Fatalf("failed compaction handling = %s", encoded)
	}
}

func assertRuntimeCode(t *testing.T, err error, code string) {
	t.Helper()
	var runtimeError *Error
	if !errors.As(err, &runtimeError) || runtimeError.Code != code {
		t.Fatalf("runtime error = %v; want %s", err, code)
	}
}

func testRuntime(t *testing.T, client ResponseClient) (*Runtime, func()) {
	t.Helper()
	home, workspaceDir := t.TempDir(), t.TempDir()
	store := session.NewStore(home)
	workspace, err := store.ResolveWorkspace(workspaceDir)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(workspace)
	if err != nil {
		t.Fatal(err)
	}
	locked, value, err := store.AcquireForResume(workspace, created.SessionID, "test")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := instructions.Build(home, workspace.Dir, "test instructions")
	if err != nil {
		locked.Close()
		t.Fatal(err)
	}
	runtime := &Runtime{
		Client: client, Locked: locked, Store: store, Workspace: workspace, Session: value,
		Access: "write", Reviewer: allowCommands{},
		ProfileName: "test", Profile: session.ModelProfile{Name: "test", Model: "test-model", CompactThreshold: 1000},
		Instructions: snapshot,
	}
	return runtime, func() { _ = locked.Close() }
}

func TestFailedTurnSummaryWarnsAboutUnfinishedManagedProcess(t *testing.T) {
	turn := session.Turn{ID: "turn", Status: session.StatusFailed, ToolCalls: []session.ToolCall{{
		Name: "shell_exec", ResultState: session.ToolResultKnown,
		Result: json.RawMessage(`{"ok":true,"data":{"status":"running","process_id":"proc_test"}}`),
	}}}
	if summary := failedTurnSummary(turn); !bytes.Contains([]byte(summary), []byte("final outcome is unknown")) {
		t.Fatalf("missing unfinished-process warning: %s", summary)
	}
	turn.ToolCalls = append(turn.ToolCalls, session.ToolCall{
		Name: "shell_wait", ResultState: session.ToolResultKnown,
		Result: json.RawMessage(`{"ok":true,"data":{"status":"completed","process_id":"proc_test"}}`),
	})
	if summary := failedTurnSummary(turn); bytes.Contains([]byte(summary), []byte("final outcome is unknown")) {
		t.Fatalf("completed process still warned: %s", summary)
	}
}

func TestRuntimeWaitsForManagedProcessBeforeFinalAnswer(t *testing.T) {
	call := json.RawMessage(`{"type":"function_call","call_id":"long-1","name":"shell_exec","arguments":"{\"command\":\"printf start; sleep 1; printf end\",\"timeout_ms\":null,\"max_output_chars\":null,\"yield_time_ms\":1}"}`)
	independent := json.RawMessage(`{"type":"function_call","call_id":"skill-1","name":"skill_read","arguments":"{\"name\":\"missing\"}"}`)
	premature := json.RawMessage(`{"type":"message","content":[{"type":"output_text","text":"too early"}]}`)
	final := json.RawMessage(`{"type":"message","content":[{"type":"output_text","text":"finished"}]}`)
	client := &fakeClient{outputs: [][]json.RawMessage{{call}, {independent}, {premature}, {final}}}
	runtime, cleanup := testRuntime(t, client)
	defer cleanup()
	runtime.Access = "full"
	result, err := runtime.Run(context.Background(), "run command")
	if err != nil || result.Text != "finished" || client.calls != 4 {
		t.Fatalf("result=%+v calls=%d err=%v", result, client.calls, err)
	}
	stored, err := runtime.Locked.Load()
	if err != nil {
		t.Fatal(err)
	}
	turn := stored.Turns[len(stored.Turns)-1]
	var toolResult struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if len(turn.ToolCalls) != 2 || json.Unmarshal(turn.ToolCalls[0].Result, &toolResult) != nil || len(turn.Messages) != 2 || turn.Messages[1].Text != "finished" || toolResult.Data.Status != "running" {
		t.Fatalf("messages=%+v tool_count=%d first_status=%q", turn.Messages, len(turn.ToolCalls), toolResult.Data.Status)
	}
}

func TestRuntimeRequestLimitStopsManagedProcess(t *testing.T) {
	call := json.RawMessage(`{"type":"function_call","call_id":"long-limit","name":"shell_exec","arguments":"{\"command\":\"sleep 2; touch should-not-exist\",\"timeout_ms\":null,\"max_output_chars\":null,\"yield_time_ms\":1}"}`)
	client := &fakeClient{outputs: [][]json.RawMessage{{call}}}
	runtime, cleanup := testRuntime(t, client)
	defer cleanup()
	runtime.Access = "full"
	runtime.MaxRequests = 1
	_, err := runtime.Run(context.Background(), "start then stop")
	var runtimeError *Error
	if !errors.As(err, &runtimeError) || runtimeError.Code != "request_limit_exceeded" {
		t.Fatalf("limit error = %v", err)
	}
	time.Sleep(2200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(runtime.Workspace.Dir, "should-not-exist")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed process was not stopped: %v", err)
	}
}

func TestRuntimeDeadlineStopsManagedProcess(t *testing.T) {
	call := json.RawMessage(`{"type":"function_call","call_id":"long-timeout","name":"shell_exec","arguments":"{\"command\":\"sleep 2; touch should-not-exist\",\"timeout_ms\":null,\"max_output_chars\":null,\"yield_time_ms\":1}"}`)
	premature := json.RawMessage(`{"type":"message","content":[{"type":"output_text","text":"too early"}]}`)
	runtime, cleanup := testRuntime(t, &fakeClient{outputs: [][]json.RawMessage{{call}, {premature}}})
	defer cleanup()
	runtime.Access = "full"
	runtime.MaxDuration = 50 * time.Millisecond
	_, err := runtime.Run(context.Background(), "start then time out")
	var runtimeError *Error
	if !errors.As(err, &runtimeError) || runtimeError.Code != "turn_timeout" {
		t.Fatalf("deadline error = %v", err)
	}
	time.Sleep(2200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(runtime.Workspace.Dir, "should-not-exist")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed process survived turn deadline: %v", err)
	}
}
