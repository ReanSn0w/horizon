package daemon

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

	"github.com/ReanSn0w/horizon/internal/responses"
	"github.com/ReanSn0w/horizon/internal/session"
)

type observedModelRequest struct {
	body    responses.Request
	release chan struct{}
}

func TestActiveInputBuiltHorizonWithDelayedProviderAndTool(t *testing.T) {
	home := shortHome(t)
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", filepath.Join(home, "bin", "horizon"), ".")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Horizon: %v: %s", err, out)
	}
	var callNumber atomic.Int32
	seen := make(chan observedModelRequest, 6)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body responses.Request
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode model request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		n := callNumber.Add(1)
		request := observedModelRequest{body: body, release: make(chan struct{})}
		select {
		case seen <- request:
		case <-r.Context().Done():
			return
		}
		select {
		case <-request.release:
		case <-r.Context().Done():
			return
		}
		output := []json.RawMessage{
			json.RawMessage(`{"type":"message","content":[{"type":"output_text","text":"answer"}]}`),
		}
		if n == 1 {
			output = []json.RawMessage{json.RawMessage(`{"type":"function_call","call_id":"tool-marker","name":"shell_exec","arguments":"{\"command\":\"touch tool-started; while [ ! -f tool-release ]; do sleep 0.02; done; printf marker\",\"timeout_ms\":10000,\"max_output_chars\":null}"}`)}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		data, _ := json.Marshal(responses.Response{ID: fmt.Sprintf("request-%d", n), Status: "completed", Output: output})
		fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", data)
	}))
	defer provider.Close()
	config := fmt.Sprintf(`mode: unit
default_model: local
providers:
  local:
    url: %s
    key: fixture
models:
  local:
    provider: local
    model: local
    compact_threshold: 10000
limits:
  max_model_requests: 8
  max_turn_duration: 20s
`, provider.URL)
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	settings, err := LoadSettings(home)
	if err != nil || settings.ActiveDialogs != 2 {
		t.Fatalf("legacy config did not receive the default adapter limit: %+v %v", settings, err)
	}
	q, err := OpenQueue(home, settings, HorizonRunner(home))
	if err != nil {
		t.Fatal(err)
	}
	adapter := Caller{Kind: "adapter", Source: "fixture", Credential: "fixture-only", Workspace: workspace, Access: "full"}
	q.adapters[adapter.Source] = adapter.Credential
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	serverDone := make(chan error, 1)
	go func() { serverDone <- Serve(ctx, home, q.Handle) }()
	defer func() {
		cancel()
		if err := <-serverDone; err != nil {
			t.Error(err)
		}
	}()
	for daemonReady := false; !daemonReady; {
		if err := ctx.Err(); err != nil {
			t.Fatal("daemon handshake timed out")
		}
		reply, err := Call(ctx, home, Request{ID: "hello", Method: "hello"})
		if err == nil {
			if !strings.Contains(string(reply.Data), ActiveInputCapability) {
				t.Fatalf("daemon hello omitted active input: %s", reply.Data)
			}
			daemonReady = true
		} else {
			time.Sleep(10 * time.Millisecond)
		}
	}
	call := func(request Request, result any) {
		t.Helper()
		request.Caller = adapter
		response, err := Call(ctx, home, request)
		if err != nil {
			t.Fatalf("daemon %s: %v", request.Method, err)
		}
		if err := json.Unmarshal(response.Data, result); err != nil {
			t.Fatal(err)
		}
	}
	submit := func(op, author, text string) Task {
		t.Helper()
		var task Task
		call(Request{ID: op, Method: "submit", OperationID: op, Delivery: ActiveDelivery, Route: "chat",
			Author: author, Message: text}, &task)
		if task.Input == nil {
			t.Fatalf("missing input receipt: %+v", task)
		}
		return task
	}
	lookup := func(op string) Task {
		t.Helper()
		var task Task
		call(Request{ID: "lookup", Method: "lookup", OperationID: op}, &task)
		return task
	}
	waitRequest := func() observedModelRequest {
		t.Helper()
		select {
		case r := <-seen:
			return r
		case <-ctx.Done():
			t.Fatal("provider did not receive request")
			return observedModelRequest{}
		}
	}
	waitSucceeded := func(taskID string) Task {
		t.Helper()
		for ctx.Err() == nil {
			var got struct {
				Task Task   `json:"task"`
				Text string `json:"text"`
			}
			call(Request{ID: "read", Method: "read", TaskID: taskID}, &got)
			switch got.Task.State {
			case "succeeded":
				if got.Text != "answer" {
					t.Fatalf("wrong result %q", got.Text)
				}
				return got.Task
			case "failed", "cancelled", "unknown":
				t.Fatalf("worker did not succeed: %+v", got.Task)
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("worker completion timed out")
		return Task{}
	}
	assertContainsInOrder := func(items []json.RawMessage, want ...string) {
		t.Helper()
		last := -1
		encoded, _ := json.Marshal(items)
		for _, part := range want {
			pos := strings.Index(string(encoded), part)
			if pos <= last {
				t.Fatalf("provider did not receive %q in order: %s", part, encoded)
			}
			last = pos
		}
	}

	first := submit("first", "author one", "first-message")
	queued := submit("queued", "author two", "queued-message")
	if first.ID != queued.ID || queued.Input.Status != "stored" {
		t.Fatalf("queued input was not batched: %+v %+v", first, queued)
	}
	queueDone := make(chan error, 1)
	go func() { queueDone <- q.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-queueDone; err != nil {
			t.Error(err)
		}
	}()
	initial := waitRequest()
	assertContainsInOrder(initial.body.Input, "author one", "first-message", "author two", "queued-message")
	duringSSE := submit("stream", "author three", "during-stream")
	if duringSSE.ID != first.ID || duringSSE.Input.Status != "stored" {
		t.Fatalf("message during live response: %+v", duringSSE)
	}
	close(initial.release)
	for {
		if _, err := os.Stat(filepath.Join(workspace, "tool-started")); err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("tool did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	duringTool := submit("tool", "author four", "during-tool")
	if duringTool.ID != first.ID || duringTool.Input.Status != "stored" || lookup("stream").Input.Status != "stored" {
		t.Fatalf("unfinished tool changed input inclusion: %+v", duringTool)
	}
	if err := os.WriteFile(filepath.Join(workspace, "tool-release"), []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	afterTool := waitRequest()
	assertContainsInOrder(afterTool.body.Input, "tool-marker", "function_call_output", "during-stream", "during-tool")
	beforeClose := submit("closing", "author five", "before-close")
	if beforeClose.ID != first.ID {
		t.Fatalf("pending finalization created another worker: %+v", beforeClose)
	}
	close(afterTool.release)
	continued := waitRequest()
	assertContainsInOrder(continued.body.Input, "during-tool", "before-close")
	close(continued.release)
	firstTurn := waitSucceeded(first.ID)
	for _, op := range []string{"first", "queued", "stream", "tool", "closing"} {
		got := lookup(op)
		if got.Input.Status != "included" || got.Input.Turn != firstTurn.Turn || got.Input.Session != firstTurn.Session {
			t.Fatalf("missing included receipt for %s: %+v", op, got.Input)
		}
	}
	afterClose := submit("closed", "author six", "after-close")
	if afterClose.ID == first.ID || afterClose.Session != firstTurn.Session {
		t.Fatalf("late input failed to reuse route session: %+v", afterClose)
	}
	next := waitRequest()
	assertContainsInOrder(next.body.Input, "before-close", "after-close")
	close(next.release)
	waitSucceeded(afterClose.ID)

	store := session.NewStore(home)
	resolved, err := store.ResolveWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	history, err := store.ReadForWorkspace(resolved, firstTurn.Session)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Turns) != 2 || len(history.Turns[0].ToolCalls) != 1 || len(history.Turns[0].Messages) != 6 ||
		history.Turns[0].Messages[2].Inbound.AfterToolCalls != 1 || history.Turns[0].Messages[3].Inbound.AfterToolCalls != 1 {
		t.Fatalf("unexpected durable turn history: %+v", history.Turns)
	}
}
