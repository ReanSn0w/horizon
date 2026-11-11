package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ReanSn0w/horizon/internal/turninput"
)

func activeRequest(q *Queue, op, route, message string) Request {
	const source, credential = "test", "registered-adapter"
	q.adapters[source] = credential
	return Request{Method: "submit", OperationID: op, Delivery: ActiveDelivery, Route: route,
		Message: message, Author: "sender", Caller: Caller{Kind: "adapter", Source: source,
			Credential: credential, Workspace: "/tmp", Access: "write"}}
}

func inputRequest(task *Task) Request {
	return Request{Method: "input_next", TaskID: task.ID, Route: task.Route, TurnID: "test-turn",
		Caller: Caller{Kind: "turn_input", Credential: "worker-credential", Workspace: task.Workspace, Session: task.Session}}
}

func checkFailure(t *testing.T, q *Queue, r Request, code string) {
	t.Helper()
	_, err := q.Handle(context.Background(), r)
	var f *Failure
	if !errors.As(err, &f) || f.Code != code {
		t.Fatalf("request %s returned %v, expected %s", r.Method, err, code)
	}
}

func TestActiveInputStoredIncludedAndCloseBoundary(t *testing.T) {
	q, err := OpenQueue(shortHome(t), Defaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	r := activeRequest(q, "message-1", "chat", "first")
	first := taskResult(t, q, r)
	r2 := activeRequest(q, "message-2", "chat", "second")
	r2.Author = "another sender"
	second := taskResult(t, q, r2)
	if first.ID != second.ID || first.Input.Status != "stored" || second.Input.Sequence <= first.Input.Sequence {
		t.Fatalf("queued input was not batched: %+v %+v", first, second)
	}
	lookup := Request{Method: "lookup", Caller: r.Caller, OperationID: r.OperationID}
	if got := taskResult(t, q, lookup); *got.Input != *first.Input {
		t.Fatalf("lost response lookup returned %+v", got.Input)
	}
	if got := taskResult(t, q, r); got.Input.ID != first.Input.ID {
		t.Fatalf("retry created a different input: %+v", got)
	}
	changed := r
	changed.Author = "changed sender"
	checkFailure(t, q, changed, "operation_conflict")
	changed = activeRequest(q, "route-conflict", r.Route, "wrong workspace")
	changed.Caller.Workspace = q.home
	checkFailure(t, q, changed, "route_conflict")
	changed = activeRequest(q, "channel-conflict", r.Route, "wrong channel")
	changed.Channel = true
	checkFailure(t, q, changed, "route_conflict")

	task := q.state.Tasks[first.ID]
	task.State, task.Session = "running", "session"
	q.inputCredentials[task.ID] = "worker-credential"
	read := inputRequest(task)
	wrong := read
	wrong.Caller.Session = "other-session"
	checkFailure(t, q, wrong, "access_denied")
	wrong = read
	wrong.Route = "other-route"
	checkFailure(t, q, wrong, "access_denied")
	wrong = read
	wrong.Caller.Credential = r.Caller.Credential
	checkFailure(t, q, wrong, "access_denied")

	third := taskResult(t, q, activeRequest(q, "message-3", "chat", "during request"))
	if third.ID != task.ID {
		t.Fatalf("live input used another task: %+v", third)
	}
	for _, expected := range []*Task{first, second, third} {
		v, err := q.Handle(context.Background(), read)
		if err != nil {
			t.Fatal(err)
		}
		msg := v.(*turninput.Message)
		if msg.ID != expected.Input.ID || msg.Sequence != expected.Input.Sequence || msg.Text == "" ||
			(expected == second && msg.Author != r2.Author) {
			t.Fatalf("input order or author lost: %+v", msg)
		}
		ack := read
		ack.Method, ack.InputID = "input_ack", msg.ID
		if _, err := q.Handle(context.Background(), ack); err != nil {
			t.Fatal(err)
		}
		if _, err := q.Handle(context.Background(), ack); err != nil {
			t.Fatalf("lost acknowledgment cannot be repeated: %v", err)
		}
	}
	if got := taskResult(t, q, lookup); got.Input.Status != "included" || got.Input.Session != "session" || got.Input.Turn != read.TurnID {
		t.Fatalf("lookup omitted inclusion data: %+v", got.Input)
	}
	read.Method = "input_close"
	if data, err := q.Handle(context.Background(), read); err != nil || data != nil {
		t.Fatalf("close empty input = %v, %v", data, err)
	}
	late := taskResult(t, q, activeRequest(q, "message-4", "chat", "after close"))
	if late.ID == task.ID || late.Child != task.Child || late.Session != task.Session || late.State != "queued" {
		t.Fatalf("late input did not go to the next turn of the same session: %+v", late)
	}
}

func TestActiveInputLimitsPersistenceAndRestart(t *testing.T) {
	home := shortHome(t)
	settings := Defaults()
	q, err := OpenQueue(home, settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := activeRequest(q, "first", "chat", "one")
	first := taskResult(t, q, r)
	task := q.state.Tasks[first.ID]
	task.State, task.Session = "running", "session"
	q.inputCredentials[task.ID] = "worker-credential"
	q.write = func(string, []byte) error { return errors.New("disk full") }
	r2 := activeRequest(q, "write-failure", "chat", "not accepted")
	checkFailure(t, q, r2, "storage_error")
	checkFailure(t, q, Request{Method: "lookup", Caller: r2.Caller, OperationID: r2.OperationID}, "not_found")
	ack := inputRequest(task)
	ack.Method, ack.InputID = "input_ack", first.Input.ID
	checkFailure(t, q, ack, "storage_error")
	lookup := Request{Method: "lookup", Caller: r.Caller, OperationID: r.OperationID}
	if got := taskResult(t, q, lookup); got.Input.Status != "stored" {
		t.Fatalf("failed ack reported inclusion: %+v", got.Input)
	}
	q.write = atomicState
	for i := 1; i < MaxPendingInputs; i++ {
		taskResult(t, q, activeRequest(q, fmt.Sprintf("fill-%d", i), "chat", "keep all messages"))
	}
	checkFailure(t, q, activeRequest(q, "overflow", "chat", "one more"), "input_full")

	q, err = OpenQueue(home, settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	activeRequest(q, "restore-adapter", "chat", "register")
	if got := taskResult(t, q, lookup); got.State != "unknown" || got.Input.Status != "stored" {
		t.Fatalf("restart acknowledged input or retried a running task: %+v", got)
	}
	checkFailure(t, q, inputRequest(q.state.Tasks[first.ID]), "access_denied")
	resolve := Request{Method: "resolve_input", OperationID: "confirmed-by-operator", Caller: r.Caller, TaskID: first.ID}
	taskResult(t, q, resolve)
	got := taskResult(t, q, lookup)
	if got.ID == first.ID || got.Child != first.Child || got.State != "queued" || got.Input.ID != first.Input.ID || !q.state.Tasks[first.ID].InputResolved {
		t.Fatalf("explicit resolution did not unblock stored input: %+v", got)
	}
	if err := q.state.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonStateVersionCompatibility(t *testing.T) {
	h := shortHome(t)
	path := filepath.Join(h, "daemon", "state.json")
	if err := atomicState(path, []byte(`{"version":1,"tasks":{},"operations":{},"events":[]}`)); err != nil {
		t.Fatal(err)
	}
	q, err := OpenQueue(h, Defaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	taskResult(t, q, activeRequest(q, "after-migration", "chat", "hello"))
	if _, err := OpenQueue(h, Defaults(), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":123,"tasks":{},"operations":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenQueue(h, Defaults(), nil); err == nil || err.Error() != "unsupported daemon state version 123" {
		t.Fatalf("unsupported version: %v", err)
	}
}

func TestConcurrentCloseAndActiveSubmitRetainInput(t *testing.T) {
	q, err := OpenQueue(shortHome(t), Defaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		route := fmt.Sprintf("race-%d", i)
		start := taskResult(t, q, activeRequest(q, route+"-initial", route, "initial"))
		task := q.state.Tasks[start.ID]
		task.State, task.Session = "running", "session-"+route
		q.inputCredentials[task.ID] = "worker-credential"
		ack := inputRequest(task)
		ack.Method, ack.InputID = "input_ack", start.Input.ID
		if _, err := q.Handle(context.Background(), ack); err != nil {
			t.Fatal(err)
		}
		closeRequest := inputRequest(task)
		closeRequest.Method = "input_close"
		newInput := activeRequest(q, route+"-late", route, "racing message")
		var closeReply, submitReply any
		var closeErr, submitErr error
		var race sync.WaitGroup
		race.Add(2)
		go func() { defer race.Done(); closeReply, closeErr = q.Handle(context.Background(), closeRequest) }()
		go func() { defer race.Done(); submitReply, submitErr = q.Handle(context.Background(), newInput) }()
		race.Wait()
		if closeErr != nil || submitErr != nil {
			t.Fatalf("close race failed: %v, %v", closeErr, submitErr)
		}
		receipt := submitReply.(*Task)
		if receipt.ID == start.ID {
			if message, ok := closeReply.(*turninput.Message); !ok || message.ID != receipt.Input.ID {
				t.Fatalf("closing turn missed a message assigned to it: %v, %+v", closeReply, receipt)
			}
		} else if closeReply != nil || receipt.Child != start.Child || receipt.Session != task.Session {
			t.Fatalf("late message did not enter the same session: %v, %+v", closeReply, receipt)
		}
	}
}

func TestCancelledWorkerThatHadNotExitedRestartsAsUnknown(t *testing.T) {
	home := shortHome(t)
	q, err := OpenQueue(home, Defaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	r := activeRequest(q, "stored-message", "chat", "retained")
	original := taskResult(t, q, r)
	task := q.state.Tasks[original.ID]
	task.State, task.Executing = "running", true
	cancelRequest := r
	cancelRequest.Method, cancelRequest.TaskID, cancelRequest.OperationID, cancelRequest.Delivery = "cancel", task.ID, "cancel", ""
	taskResult(t, q, cancelRequest)
	resolve := Request{Method: "resolve_input", OperationID: "operator-resolve", TaskID: task.ID, Caller: Caller{Kind: "operator"}}
	checkFailure(t, q, resolve, "invalid_state")

	q, err = OpenQueue(home, Defaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if q.state.Tasks[task.ID].State != "unknown" || q.state.Tasks[task.ID].Executing {
		t.Fatalf("in-flight cancellation was falsely resolved after a crash: %+v", q.state.Tasks[task.ID])
	}
	taskResult(t, q, resolve)
	lookup := Request{Method: "lookup", OperationID: r.OperationID, Caller: r.Caller}
	q.adapters[r.Caller.Source] = r.Caller.Credential
	if got := taskResult(t, q, lookup); got.ID == task.ID || got.Input.Status != "stored" || got.Input.ID != original.Input.ID {
		t.Fatalf("operator resolution lost the retained input: %+v", got)
	}
}
