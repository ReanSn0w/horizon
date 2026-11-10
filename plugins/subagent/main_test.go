package main

import (
	"bytes"
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

func TestMetadataAndHelpWithoutHome(t *testing.T) {
	t.Setenv("HORIZON_HOME", t.TempDir()+"/absent")
	for _, arg := range []string{"--help", "horizon-plugin-metadata"} {
		var out, diag bytes.Buffer
		if code := run(context.Background(), []string{arg}, nil, &out, &diag); code != 0 {
			t.Fatal(code, diag.String())
		}
		if arg == "horizon-plugin-metadata" {
			var m plugins.Metadata
			if json.Unmarshal(out.Bytes(), &m) != nil || m.AgentProtocolVersion != 1 {
				t.Fatal(out.String())
			}
		}
	}
}
func TestToolSchemas(t *testing.T) {
	d := description()
	if len(d.Tools) != 6 {
		t.Fatal(len(d.Tools))
	}
	for _, tool := range d.Tools {
		if err := plugins.CheckSchema(tool.Parameters); err != nil {
			t.Fatal(tool.Name, err)
		}
		if tool.Name == "spawn" || tool.Name == "send" || tool.Name == "cancel" {
			if plugins.Allowed("read", tool.Effect) {
				t.Fatal("read mutation")
			}
		}
	}
}

func TestPreparationHandshake(t *testing.T) {
	home, e := os.MkdirTemp("/tmp", "hs-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(home)
	home, _ = filepath.EvalSymlinks(home)
	r := plugins.Request{Home: home, Workspace: home, WorkspaceID: session.WorkspaceID(home), Access: "read", OperationID: "prepare"}
	data, _ := json.Marshal(r)
	var out bytes.Buffer
	protocol(context.Background(), "describe", home, bytes.NewReader(data), &out)
	var reply plugins.Reply
	json.Unmarshal(out.Bytes(), &reply)
	if reply.OK {
		t.Fatal("unavailable daemon ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- daemon.Serve(ctx, home, nil) }()
	defer func() { cancel(); <-done }()
	for i := 0; i < 100; i++ {
		if daemon.Hello(ctx, home) == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, op := range []string{"describe", "context"} {
		out.Reset()
		protocol(ctx, op, home, bytes.NewReader(data), &out)
		reply = plugins.Reply{}
		json.Unmarshal(out.Bytes(), &reply)
		if !reply.OK {
			t.Fatal(out.String())
		}
	}
	if _, e := os.Stat(filepath.Join(home, "daemon")); !os.IsNotExist(e) {
		t.Fatal("preparation wrote persistent state")
	}
}
