package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func timerRequest(workspace, source, op string) Request {
	return Request{
		Caller: Caller{Kind: "adapter", Source: TimerSource, Credential: "timer-test", Access: "write", Workspace: workspace},
		Method: "submit", Mode: FreshTask, SourceKey: source, OperationID: op, Message: "timer task",
	}
}

func TestOperatorManualTimerUsesDurableTimerQueue(t *testing.T) {
	home, workspace := shortHome(t), t.TempDir()
	settings := Defaults()
	settings.Adapters = []string{TimerSource}
	q, err := OpenQueue(home, settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Method: "timer_run", Caller: Caller{Kind: "operator", Workspace: workspace}, OperationID: "timer:manual:reports:abc", SourceKey: "reports", Access: "write", Message: "Inspect reports"}
	first := taskResult(t, q, request)
	if first.Owner != "" || first.Mode != FreshTask || first.SourceKey != "reports" {
		t.Fatalf("task: %+v", first)
	}
	lookup, err := q.Handle(context.Background(), Request{Method: "timer_lookup", Caller: Caller{Kind: "operator"}, OperationID: request.OperationID})
	if err != nil {
		t.Fatal(err)
	}
	if lookup.(*Task).ID != first.ID {
		t.Fatalf("lookup: %+v", lookup)
	}
	if again := taskResult(t, q, request); again.ID != first.ID {
		t.Fatal("manual retry duplicated task")
	}
	request.OperationID = "timer:manual:reports:other"
	if _, err := q.Handle(context.Background(), request); err == nil || err.(*Failure).Code != "timer_busy" {
		t.Fatalf("busy: %v", err)
	}
}

func runTimerQueue(t *testing.T, q *Queue) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- q.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
}

func waitTimerTask(t *testing.T, q *Queue, id, state string) Task {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		q.mu.Lock()
		current := *q.state.Tasks[id]
		q.mu.Unlock()
		if current.State == state {
			return current
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("task %s did not reach %s", id, state)
	return Task{}
}

func TestTimerQueueWorkspaceDeadlineAndQueuedOnlyCancellation(t *testing.T) {
	workspace, other := t.TempDir(), t.TempDir()
	link := filepath.Join(other, "linked")
	if err := os.Symlink(workspace, link); err != nil {
		t.Fatal(err)
	}
	starts := make(chan Task, 8)
	release := make(chan struct{})
	q, err := OpenQueue(shortHome(t), Defaults(), func(ctx context.Context, task Task, _ func(string) error, _ func(string, string)) Outcome {
		starts <- task
		select {
		case <-release:
			return Outcome{Text: "done"}
		case <-ctx.Done():
			return Outcome{Err: ctx.Err()}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	q.adapters[TimerSource] = "timer-test"
	first := taskResult(t, q, timerRequest(workspace, "first", "slot-first"))
	replay := taskResult(t, q, timerRequest(workspace, "first", "slot-first"))
	if first.ID != replay.ID {
		t.Fatal("same slot was submitted twice")
	}
	if _, err := q.Handle(context.Background(), timerRequest(workspace, "first", "second-slot")); err == nil || err.(*Failure).Code != "timer_busy" {
		t.Fatalf("duplicate source: %v", err)
	}
	second := taskResult(t, q, timerRequest(link, "second", "slot-second"))
	expiring := timerRequest(link, "expiring", "slot-expiring")
	deadline := time.Now().Add(150 * time.Millisecond)
	expiring.Deadline = &deadline
	expired := taskResult(t, q, expiring)
	independent := taskResult(t, q, timerRequest(other, "third", "slot-third"))
	runTimerQueue(t, q)

	got := map[string]bool{}
	for range 2 {
		select {
		case running := <-starts:
			got[running.ID] = true
		case <-time.After(3 * time.Second):
			t.Fatal("independent workspace was starved")
		}
	}
	if !got[first.ID] || !got[independent.ID] {
		t.Fatalf("unexpected concurrent tasks: %v", got)
	}
	delegated := taskResult(t, q, request("submit", "delegated"))
	select {
	case running := <-starts:
		if running.ID != delegated.ID {
			t.Fatalf("expected subagent, got %+v", running)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("adapter capacity blocked independent subagent execution")
	}
	cancel := Request{Caller: timerRequest(other, "first", "").Caller, Method: "cancel", QueuedOnly: true}
	cancel.OperationID, cancel.TaskID = "cancel-running", first.ID
	if taskResult(t, q, cancel).State != "running" {
		t.Fatal("queued-only cancellation interrupted running task")
	}
	cancel.OperationID, cancel.TaskID = "cancel-queued", second.ID
	if taskResult(t, q, cancel).State != "cancelled" {
		t.Fatal("queued-only cancellation did not cancel queued task")
	}
	if task := waitTimerTask(t, q, expired.ID, "skipped"); task.Session != "" {
		t.Fatal("expired task allocated a session")
	}
	select {
	case extra := <-starts:
		t.Fatalf("expired or same-workspace task was started: %+v", extra)
	default:
	}
	close(release)
	waitTimerTask(t, q, first.ID, "succeeded")
}

func TestTimerRepeatsBeyondChildLimitWithFreshSessions(t *testing.T) {
	workspace := t.TempDir()
	q, err := OpenQueue(shortHome(t), Defaults(), func(_ context.Context, task Task, set func(string) error, _ func(string, string)) Outcome {
		if task.Session != "" || task.Child != task.ID {
			return Outcome{Err: fmt.Errorf("timer reused an existing child")}
		}
		if err := set("session-" + task.ID); err != nil {
			return Outcome{Err: err}
		}
		file, err := os.OpenFile(filepath.Join(task.Workspace, "seen"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return Outcome{Err: err}
		}
		defer file.Close()
		_, err = fmt.Fprintln(file, task.ID)
		return Outcome{Err: err}
	})
	if err != nil {
		t.Fatal(err)
	}
	q.adapters[TimerSource] = "timer-test"
	runTimerQueue(t, q)
	want := ""
	sessions := map[string]bool{}
	for slot := range Defaults().Children + 3 {
		task := taskResult(t, q, timerRequest(workspace, "recurring", fmt.Sprintf("slot-%d", slot)))
		completed := waitTimerTask(t, q, task.ID, "succeeded")
		if completed.Session == "" || sessions[completed.Session] {
			t.Fatalf("reused session: %+v", completed)
		}
		sessions[completed.Session] = true
		want += task.ID + "\n"
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "seen")); err != nil || string(data) != want {
		t.Fatalf("workspace state lost: %q, %v", data, err)
	}
}

func TestFreshTimerRequestRejectsDialogAndAccessEscalation(t *testing.T) {
	q, err := OpenQueue(shortHome(t), Defaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	q.adapters[TimerSource] = "timer-test"
	for _, alter := range []func(*Request){
		func(r *Request) { r.Caller.Session = "another-session" },
		func(r *Request) { r.Route = "another-route" },
		func(r *Request) { r.Delivery = ActiveDelivery },
		func(r *Request) { r.Channel = true },
		func(r *Request) { r.Author = "another-author" },
		func(r *Request) { r.Caller.Kind = "subagent"; r.Caller.Session = "parent" },
		func(r *Request) { r.Caller.Access = "full"; r.Access = "full" },
		func(r *Request) { r.Mode = "" },
	} {
		req := timerRequest(t.TempDir(), "test", "slot")
		alter(&req)
		if _, err := q.Handle(context.Background(), req); err == nil {
			t.Fatalf("accepted contradictory fields: %+v", req)
		}
	}
}

func TestTimerLookupAfterDaemonRestart(t *testing.T) {
	home, workspace := shortHome(t), t.TempDir()
	q, err := OpenQueue(home, Defaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	q.adapters[TimerSource] = "timer-test"
	request := timerRequest(workspace, "reports", "durable-slot")
	first := taskResult(t, q, request)
	q.state.Tasks[first.ID].State = "running"
	if err := q.save(); err != nil {
		t.Fatal(err)
	}
	q, err = OpenQueue(home, Defaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	q.adapters[TimerSource] = "replacement-credential"
	request.Caller.Credential = "replacement-credential"
	request.Caller.Workspace = home
	request.Method = "lookup"
	recovered := taskResult(t, q, request)
	if recovered.ID != first.ID || recovered.State != "unknown" || recovered.Mode != FreshTask || recovered.SourceKey != "reports" {
		t.Fatalf("recovered task: %+v", recovered)
	}
}

func TestTimerExpiryAfterDispatchRemainsRecoverable(t *testing.T) {
	for _, tc := range []struct{ before, after string }{
		{"running", "skipped"},
		{"cancelled", "cancelled"},
	} {
		t.Run(tc.before, func(t *testing.T) {
			home := shortHome(t)
			q, err := OpenQueue(home, Defaults(), func(context.Context, Task, func(string) error, func(string, string)) Outcome {
				t.Fatal("expired task reached the Horizon runner")
				return Outcome{}
			})
			if err != nil {
				t.Fatal(err)
			}
			q.adapters[TimerSource] = "timer-test"
			req := timerRequest(t.TempDir(), "reports", "past-slot")
			deadline := time.Now().Add(-time.Second)
			req.Deadline = &deadline
			first := taskResult(t, q, req)
			current := q.state.Tasks[first.ID]
			current.State, current.Executing = tc.before, true
			q.execute(context.Background(), *current)
			q, err = OpenQueue(home, Defaults(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := q.state.Tasks[first.ID]; got.State != tc.after || got.Executing || got.Session != "" {
				t.Fatalf("expiry after dispatch was not durably finalized: %+v", got)
			}
		})
	}
}

func TestHorizonRunnerSkipsElapsedTimerBeforeSessionProcess(t *testing.T) {
	home, workspace := shortHome(t), t.TempDir()
	const fixture = `mode: unit
default_model: local
providers:
  local:
    url: http://127.0.0.1:1
    key: fixture
models:
  local:
    provider: local
    model: local
    compact_threshold: 10000
`
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(-time.Second)
	task := Task{Owner: "adapter:timer", Mode: FreshTask, Deadline: &deadline, Workspace: workspace, Access: "read"}
	result := HorizonRunner(home)(context.Background(), task, func(string) error {
		t.Fatal("expired task created a session")
		return nil
	}, func(string, string) {})
	if !errors.Is(result.Err, errTimerWindowElapsed) {
		t.Fatalf("runner attempted to start a process after deadline: %v", result.Err)
	}
}

func TestHorizonRunnerPassesTimerOriginAtCreation(t *testing.T) {
	for _, isTimer := range []bool{false, true} {
		home, workspace := shortHome(t), t.TempDir()
		cfg := "default_model: local\nproviders:\n  local:\n    url: http://127.0.0.1:1\n    key: fixture\nmodels:\n  local:\n    provider: local\n    model: local\n    compact_threshold: 10000\n"
		if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(cfg), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(home, "bin"), 0700); err != nil {
			t.Fatal(err)
		}
		script := `#!/bin/sh
if [ "$3" = sessions ]; then
 printf '%s\n' "$@" > "$HORIZON_HOME/create-args"
 echo aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
else
 cat >/dev/null
 echo '{"type":"turn_completed","data":{"text":"done"}}'
fi
`
		if err := os.WriteFile(filepath.Join(home, "bin", "horizon"), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		task := Task{ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Workspace: workspace, Access: "write", Message: "Task"}
		if isTimer {
			task.Owner = "adapter:timer"
			task.Mode = FreshTask
			task.SourceKey = "nap"
		}
		result := HorizonRunner(home)(context.Background(), task, func(string) error { return nil }, func(string, string) {})
		if result.Err != nil || result.ExitCode != 0 {
			t.Fatal(result)
		}
		args, err := os.ReadFile(filepath.Join(home, "create-args"))
		if err != nil {
			t.Fatal(err)
		}
		want := "--timer-id\nnap\n--timer-task-id\n" + task.ID + "\n"
		if strings.Contains(string(args), want) != isTimer {
			t.Fatal(string(args))
		}
	}
}
