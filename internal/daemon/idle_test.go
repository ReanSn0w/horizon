package daemon

import (
	"context"
	"testing"
)

func TestIdleTaskSharesDialogLeaseAndCancelsOnInput(t *testing.T) {
	q, err := OpenQueue(shortHome(t), Defaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	first := activeRequest(q, "first", "chat", "first input")
	first.Channel = true
	accepted := taskResult(t, q, first)
	root := q.state.Tasks[accepted.ID]
	root.State = "succeeded"
	root.Session = "session"
	in := q.state.Inputs[root.InputIDs[0]]
	in.Receipt.Status = "included"
	in.Receipt.Session = root.Session
	in.Receipt.Turn = "first-turn"
	in.Author, in.Text = "", ""
	idle := Request{Method: "idle_compact", Caller: first.Caller, Route: "chat", OperationID: "idle"}
	compact := taskResult(t, q, idle)
	if !compact.IdleCompact || compact.Child != root.Child || compact.Session != root.Session {
		t.Fatalf("compaction missing dialog lease: %#v", compact)
	}
	q.state.Tasks[compact.ID].State = "running"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.running[compact.ID] = cancel
	idle.OperationID = "idle-second"
	checkFailure(t, q, idle, "dialog_busy")
	second := first
	second.OperationID = "second"
	second.Message = "new input while compacting"
	successor := taskResult(t, q, second)
	if successor.Child != root.Child || successor.Session != root.Session || successor.State != "queued" {
		t.Fatalf("input missing dialog lease: %#v", successor)
	}
	if ctx.Err() == nil || q.state.Tasks[compact.ID].State != "cancelled" || q.running[compact.ID] == nil {
		t.Fatal("new input did not cancel compaction and retain its lease until exit")
	}
}
