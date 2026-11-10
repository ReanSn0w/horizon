package main

import (
	"context"
	"encoding/json"
	"github.com/ReanSn0w/horizon/internal/daemon"
	"github.com/ReanSn0w/horizon/internal/plugins"
	"github.com/ReanSn0w/horizon/internal/session"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testQueue(t *testing.T) (string, *daemon.Queue) {
	t.Helper()
	h, e := os.MkdirTemp("/tmp", "hst-")
	if e != nil {
		t.Fatal(e)
	}
	h, _ = filepath.EvalSymlinks(h)
	t.Cleanup(func() { os.RemoveAll(h) })
	q, e := daemon.OpenQueue(h, daemon.Defaults(), nil)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- daemon.Serve(ctx, h, q.Handle) }()
	t.Cleanup(func() { cancel(); <-done })
	for i := 0; i < 100; i++ {
		if daemon.Hello(ctx, h) == nil {
			return h, q
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("daemon unavailable")
	return "", nil
}
func hostRequest(home, tool, op, args string) plugins.Request {
	return plugins.Request{Home: home, Workspace: home, WorkspaceID: session.WorkspaceID(home), Access: "write", SessionID: "parent", TurnID: "turn", CallID: op, OperationID: op, Tool: tool, Arguments: json.RawMessage(args)}
}
func TestSubagentOperations(t *testing.T) {
	h, _ := testQueue(t)
	ctx := context.Background()
	r := hostRequest(h, "spawn", "spawn", `{"message":"task","access":"write"}`)
	v, e := invoke(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	var task daemon.Task
	json.Unmarshal(v.(json.RawMessage), &task)
	for _, tc := range []struct{ tool, op, args string }{{"list", "list", `{"cursor":0}`}, {"read", "read", `{"task_id":"","operation_id":"spawn","offset":0}`}, {"send", "send", `{"task_id":"` + task.ID + `","message":"next"}`}, {"wait", "wait", `{"cursor":0,"wait_ms":1}`}, {"cancel", "cancel", `{"task_id":"` + task.ID + `","tree":true}`}} {
		if _, e = invoke(ctx, hostRequest(h, tc.tool, tc.op, tc.args)); e != nil {
			t.Fatal(tc.tool, e)
		}
	}
	again, e := invoke(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	var same daemon.Task
	json.Unmarshal(again.(json.RawMessage), &same)
	if same.ID != task.ID {
		t.Fatal("retry created a second child")
	}
}
func TestRejectInvalidArguments(t *testing.T) {
	for _, tc := range []struct{ tool, args string }{{"spawn", `{"message":"x","access":"write","workspace":"/elsewhere"}`}, {"wait", `{"cursor":0,"wait_ms":30001}`}, {"read", `{"task_id":"x","operation_id":"y","offset":0}`}, {"list", `{"cursor":-1}`}} {
		if _, e := invoke(context.Background(), hostRequest("/tmp", tc.tool, "call", tc.args)); e == nil {
			t.Fatal(tc)
		}
	}
}
