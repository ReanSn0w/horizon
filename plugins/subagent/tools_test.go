package main

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/ReanSn0w/horizon/internal/daemon"
	"github.com/ReanSn0w/horizon/internal/plugins"
	"github.com/ReanSn0w/horizon/internal/session"
	"net"
	"os"
	"path/filepath"
	"strings"
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

func TestAccessAndForeignTask(t *testing.T) {
	h, _ := testQueue(t)
	ctx := context.Background()
	r := hostRequest(h, "spawn", "first", `{"message":"task","access":"full"}`)
	r.Access = "read"
	if _, e := invoke(ctx, r); e == nil {
		t.Fatal("read spawn")
	}
	r.Access = "write"
	if _, e := invoke(ctx, r); e == nil {
		t.Fatal("escalation")
	}
	r.Access = "full"
	v, e := invoke(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	var task daemon.Task
	json.Unmarshal(v.(json.RawMessage), &task)
	send := hostRequest(h, "send", "next", `{"task_id":"`+task.ID+`","message":"next"}`)
	v, e = invoke(ctx, send)
	if e != nil {
		t.Fatal(e)
	}
	var next daemon.Task
	json.Unmarshal(v.(json.RawMessage), &next)
	if next.Access != "write" {
		t.Fatal("send retained full access")
	}
	read := hostRequest(h, "read", "read", `{"task_id":"`+task.ID+`","operation_id":"","offset":0}`)
	read.SessionID = "other"
	if _, e = invoke(ctx, read); e == nil {
		t.Fatal("foreign task readable")
	}
}
func TestUnknownResponseIncludesRecoveryID(t *testing.T) {
	h, e := os.MkdirTemp("/tmp", "hsu-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(h)
	h, _ = filepath.EvalSymlinks(h)
	os.MkdirAll(filepath.Join(h, "run"), 0700)
	listener, e := net.Listen("unix", daemon.Socket(h))
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	queue, err := daemon.OpenQueue(h, daemon.Defaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var accepted daemon.Request
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, e := listener.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		line, _ := bufio.NewReader(c).ReadBytes('\n')
		json.Unmarshal(line, &accepted)
		if _, err := queue.Handle(context.Background(), accepted); err != nil {
			t.Error(err)
		}
	}()
	_, e = invoke(context.Background(), hostRequest(h, "spawn", "lost-call", `{"message":"task","access":"write"}`))
	<-done
	if e == nil || !strings.Contains(e.Error(), "operation_id=lost-call") {
		t.Fatal(e)
	}
	found, err := queue.Handle(context.Background(), daemon.Request{Method: "lookup", Caller: accepted.Caller, OperationID: accepted.OperationID})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := queue.Handle(context.Background(), accepted)
	if err != nil || retry.(*daemon.Task).ID != found.(*daemon.Task).ID {
		t.Fatal("lost response caused duplicate", err)
	}
}
