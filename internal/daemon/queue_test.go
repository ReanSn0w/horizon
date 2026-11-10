package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func request(method, op string) Request {
	return Request{Method: method, OperationID: op, Caller: Caller{Kind: "subagent", Workspace: "/tmp", Session: "parent", Access: "write"}, Message: "task"}
}
func taskResult(t *testing.T, q *Queue, r Request) *Task {
	t.Helper()
	v, e := q.Handle(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	return v.(*Task)
}
func TestQueueIdempotencyAndRecovery(t *testing.T) {
	h := shortHome(t)
	q, e := OpenQueue(h, Defaults(), nil)
	if e != nil {
		t.Fatal(e)
	}
	r := request("submit", "one")
	a := taskResult(t, q, r)
	b := taskResult(t, q, r)
	if a.ID != b.ID {
		t.Fatal("duplicate")
	}
	r.Message = "changed"
	if _, e = q.Handle(context.Background(), r); e == nil {
		t.Fatal("conflict accepted")
	}
	q.state.Tasks[a.ID].State = "running"
	q.save()
	q, e = OpenQueue(h, Defaults(), nil)
	if e != nil {
		t.Fatal(e)
	}
	if q.state.Tasks[a.ID].State != "unknown" {
		t.Fatal("restarted job")
	}
	q.write = func(string, []byte) error { return errors.New("disk full") }
	if _, e = q.Handle(context.Background(), request("submit", "two")); e == nil {
		t.Fatal("accepted without persistence")
	}
	if len(q.state.Tasks) != 1 {
		t.Fatal("failed mutation retained")
	}
}
func TestQueueAccessAndOwnership(t *testing.T) {
	q, _ := OpenQueue(shortHome(t), Defaults(), nil)
	r := request("submit", "one")
	r.Caller.Access = "read"
	if _, e := q.Handle(context.Background(), r); e == nil {
		t.Fatal("read mutation")
	}
	r.Caller.Access = "write"
	r.Access = "full"
	if _, e := q.Handle(context.Background(), r); e == nil {
		t.Fatal("escalation")
	}
	r.Access = ""
	a := taskResult(t, q, r)
	r.Method = "read"
	r.TaskID = a.ID
	r.Caller.Session = "stranger"
	if _, e := q.Handle(context.Background(), r); e == nil {
		t.Fatal("foreign task")
	}
}
func TestQueuePerDepthProgressAndSerialTurns(t *testing.T) {
	s := Defaults()
	s.Parallel = 1
	started := make(chan Task, 8)
	release := make(chan struct{})
	runner := func(ctx context.Context, t Task, set func(string) error, stage func(string, string)) Outcome {
		if e := set(t.Child); e != nil {
			return Outcome{Err: e}
		}
		started <- t
		select {
		case <-ctx.Done():
			return Outcome{Err: ctx.Err()}
		case <-release:
			return Outcome{Text: "done"}
		}
	}
	q, _ := OpenQueue(shortHome(t), s, runner)
	parent := taskResult(t, q, request("submit", "one"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- q.Run(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("parent not started")
	}
	r := request("send", "next")
	r.TaskID = parent.ID
	taskResult(t, q, r)
	r = request("submit", "nested")
	r.Caller.Session = parent.Child
	child := taskResult(t, q, r)
	if child.Depth != 2 {
		t.Fatal(child.Depth)
	}
	select {
	case got := <-started:
		if got.ID != child.ID {
			t.Fatal("session overlap")
		}
	case <-time.After(time.Second):
		t.Fatal("child starved")
	}
	close(release)
	select {
	case next := <-started:
		if next.Child != parent.Child {
			t.Fatal(next)
		}
	case <-time.After(time.Second):
		t.Fatal("next turn not started")
	}
}
func TestQueueCancelTreeAndPaging(t *testing.T) {
	q, _ := OpenQueue(shortHome(t), Defaults(), nil)
	a := taskResult(t, q, request("submit", "one"))
	q.state.Tasks[a.ID].Session = "child-session"
	r := request("submit", "child")
	r.Caller.Session = "child-session"
	b := taskResult(t, q, r)
	r = request("cancel", "cancel")
	r.TaskID = a.ID
	r.Tree = true
	taskResult(t, q, r)
	if q.state.Tasks[b.ID].State != "cancelled" {
		t.Fatal("descendant survived")
	}
	for i := 0; i < 1030; i++ {
		q.event(q.state.Tasks[a.ID], "stage")
	}
	r = request("wait", "")
	if _, e := q.Handle(context.Background(), r); e == nil {
		t.Fatal("expired cursor accepted")
	}
	q.state.Tasks[a.ID].Result = "hello"
	r = request("read", "")
	r.TaskID = a.ID
	v, e := q.Handle(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(v)
	if len(raw) == 0 {
		t.Fatal("missing result")
	}
}

func TestDepthAndQueueLimits(t *testing.T) {
	s := Defaults()
	s.Depth = 1
	s.Queue = 1
	q, _ := OpenQueue(shortHome(t), s, nil)
	a := taskResult(t, q, request("submit", "one"))
	if _, e := q.Handle(context.Background(), request("submit", "two")); e == nil {
		t.Fatal("queue exceeded")
	}
	q.state.Tasks[a.ID].State = "succeeded"
	q.state.Tasks[a.ID].Session = "child"
	r := request("submit", "nested")
	r.Caller.Session = "child"
	if _, e := q.Handle(context.Background(), r); e == nil {
		t.Fatal("depth exceeded")
	}
}
