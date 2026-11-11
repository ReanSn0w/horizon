package daemon

import (
	"context"
	"fmt"
	"testing"
	"time"
)

type runningTestTask struct {
	task    Task
	release chan struct{}
}

func nextTestTask(t *testing.T, started <-chan runningTestTask) runningTestTask {
	t.Helper()
	select {
	case got := <-started:
		return got
	case <-time.After(3 * time.Second):
		t.Fatal("scheduler did not start a task")
		return runningTestTask{}
	}
}

func TestAdapterDialogsFairAtLimitAndChildStillRuns(t *testing.T) {
	settings := Defaults()
	settings.Parallel = 1 // Adapter capacity is independent of per-depth slots.
	started := make(chan runningTestTask, 32)
	runner := func(ctx context.Context, task Task, set func(string) error, _ func(string, string)) Outcome {
		if err := set(task.Child); err != nil {
			return Outcome{Err: err}
		}
		release := make(chan struct{})
		started <- runningTestTask{task: task, release: release}
		select {
		case <-ctx.Done():
			return Outcome{Err: ctx.Err()}
		case <-release:
			return Outcome{Text: "done"}
		}
	}
	q, err := OpenQueue(shortHome(t), settings, runner)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		r := activeRequest(q, fmt.Sprintf("noisy-%d", i), "noisy", "legacy queued task")
		r.Delivery = ""
		taskResult(t, q, r)
	}
	for i := 1; i < 20; i++ {
		route := fmt.Sprintf("dialog-%02d", i)
		first := taskResult(t, q, activeRequest(q, route+"-first", route, "first message"))
		second := taskResult(t, q, activeRequest(q, route+"-second", route, "second message"))
		if first.ID != second.ID {
			t.Fatalf("expected a single queued turn for %s", route)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- q.Run(ctx) }()
	defer func() { cancel(); <-done }()
	first := nextTestTask(t, started)
	second := nextTestTask(t, started)
	if first.task.Route != "noisy" {
		first, second = second, first
	}
	if first.task.Route != "noisy" || second.task.Route != "dialog-01" {
		t.Fatalf("initial dialog dispatch = %q, %q", first.task.Route, second.task.Route)
	}
	q.mu.Lock()
	active := len(q.running)
	q.mu.Unlock()
	if active != 2 {
		t.Fatalf("active adapter limit was not respected: %d", active)
	}

	nested := request("submit", "nested-from-active-adapter")
	nested.Caller.Session = second.task.Child
	child := taskResult(t, q, nested)
	got := nextTestTask(t, started)
	if got.task.ID != child.ID || got.task.Depth != 2 {
		t.Fatalf("adapter limit consumed the descendant's slot: %+v", got.task)
	}
	close(got.release)

	close(first.release)
	for i := 2; i < 20; i++ {
		next := nextTestTask(t, started)
		want := fmt.Sprintf("dialog-%02d", i)
		if next.task.Route != want {
			t.Fatalf("old backlog monopolized slots: got %s want %s", next.task.Route, want)
		}
		close(next.release)
	}
	noisyAgain := nextTestTask(t, started)
	if noisyAgain.task.Route != "noisy" {
		t.Fatalf("scheduler did not return to the waiting dialog: %+v", noisyAgain.task)
	}
	close(noisyAgain.release)
	close(second.release)
}

func TestActiveDialogLimitsValidate(t *testing.T) {
	settings := Defaults()
	if settings.ActiveDialogs != 2 || settings.Validate() != nil {
		t.Fatalf("default settings: %+v", settings)
	}
	for _, limit := range []int{0, -1, 129} {
		settings.ActiveDialogs = limit
		if settings.Validate() == nil {
			t.Fatalf("accepted invalid active dialog limit %d", limit)
		}
	}
}
