package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ReanSn0w/horizon/internal/daemon"
)

func daemonHome(t *testing.T) string {
	t.Helper()
	h, e := os.MkdirTemp("/tmp", "hde-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(h) })
	h, e = filepath.EvalSymlinks(h)
	if e != nil {
		t.Fatal(e)
	}
	return h
}
func installTestBinary(t *testing.T, source, dest string) {
	t.Helper()
	b, e := os.ReadFile(source)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(filepath.Dir(dest), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(dest, b, 0700); e != nil {
		t.Fatal(e)
	}
}
func TestDaemonBuiltHorizonWorkflow(t *testing.T) {
	home := daemonHome(t)
	workspace := t.TempDir()
	installTestBinary(t, buildBinary(t), filepath.Join(home, "bin", "horizon"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeCompleted(w, 1, message("child result")) }))
	defer server.Close()
	writeConfig(t, home, server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- daemon.Start(ctx, home) }()
	defer func() {
		cancel()
		if e := <-done; e != nil {
			t.Error(e)
		}
	}()
	for i := 0; i < 200; i++ {
		if daemon.Hello(ctx, home) == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	caller := daemon.Caller{Kind: "subagent", Workspace: workspace, Session: "external-parent", Access: "write"}
	call := func(r daemon.Request) daemon.Response {
		t.Helper()
		r.Caller = caller
		v, e := daemon.Call(ctx, home, r)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	spawn := call(daemon.Request{ID: "spawn", Method: "submit", OperationID: "spawn", Message: "answer"})
	var task daemon.Task
	if e := json.Unmarshal(spawn.Data, &task); e != nil {
		t.Fatal(e)
	}
	wait := func(id string) daemon.Task {
		t.Helper()
		for i := 0; i < 500; i++ {
			v := call(daemon.Request{ID: "read", Method: "read", TaskID: id})
			var got struct {
				Task daemon.Task `json:"task"`
				Text string      `json:"text"`
			}
			json.Unmarshal(v.Data, &got)
			if got.Task.State == "succeeded" {
				if got.Text != "child result" {
					t.Fatal(got.Text)
				}
				return got.Task
			}
			if got.Task.State == "failed" || got.Task.State == "unknown" {
				t.Fatalf("task %+v", got.Task)
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("task timeout")
		return daemon.Task{}
	}
	first := wait(task.ID)
	next := call(daemon.Request{ID: "send", Method: "send", TaskID: task.ID, OperationID: "send", Message: "continue"})
	var second daemon.Task
	json.Unmarshal(next.Data, &second)
	second = wait(second.ID)
	if second.Session != first.Session || first.Session == "" {
		t.Fatal("session not reused")
	}
}
