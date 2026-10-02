package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ReanSn0w/horizon/internal/plugins"
	"github.com/ReanSn0w/horizon/internal/session"
)

func fixture(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	workspace, err := session.NormalizeWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	configuration := `mode: unit
default_model: test
models:
  test:
    model: model
    compact_threshold: 1000
provider:
  url: https://unused.test/v1
  key: fake
`
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	home, err = session.NormalizeWorkspace(home)
	if err != nil {
		t.Fatal(err)
	}
	return home, workspace
}
func TestProtocolAndImmutableContext(t *testing.T) {
	home, workspace := fixture(t)
	cfg, s, err := configuration(home)
	if err != nil {
		t.Fatal(err)
	}
	_ = cfg
	m := newStore(home, workspace, s)
	request := plugins.Request{Home: home, Workspace: workspace, WorkspaceID: session.WorkspaceID(workspace), Access: "write", OperationID: "operation"}
	invoke := func(op string) plugins.Reply {
		data, _ := json.Marshal(request)
		var out, diag bytes.Buffer
		if code := run(context.Background(), []string{"horizon-plugin-agent-" + op}, bytes.NewReader(data), &out, &diag); code != 0 {
			t.Fatalf("%d %s", code, diag.String())
		}
		var reply plugins.Reply
		if err := plugins.Decode(out.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
		return reply
	}
	for count := 0; count <= 3; count++ {
		reply := invoke("context")
		var blocks []plugins.Block
		if err := plugins.Decode(reply.Data, &blocks); err != nil || !reply.OK || len(blocks) != count {
			t.Fatal(reply, blocks, err)
		}
		if count < 3 {
			scope := []string{"agent", "user", "workspace"}[count]
			if _, _, err := m.add(context.Background(), scope, "durable "+scope, scope); err != nil {
				t.Fatal(err)
			}
		}
	}
	snapshot := invoke("context")
	request.Tool = "add"
	request.Arguments = json.RawMessage(`{"scope":"user","text":"another fact"}`)
	reply := invoke("tool")
	if !reply.OK || !bytes.Contains(reply.Data, []byte(`"saved":true`)) {
		t.Fatal(reply)
	}
	if bytes.Contains(snapshot.Data, []byte("another fact")) {
		t.Fatal("snapshot mutated")
	}
	if !bytes.Contains(invoke("context").Data, []byte("another fact")) {
		t.Fatal("new snapshot missing note")
	}
	request.Access = "read"
	request.OperationID = "forbidden"
	if reply := invoke("tool"); reply.OK {
		t.Fatal("read wrote")
	}
	if reply := invoke("maintain"); reply.OK {
		t.Fatal("read maintained")
	}
	request.Tool = "read"
	request.Arguments = json.RawMessage(`{"scope":"user"}`)
	if reply := invoke("tool"); !reply.OK {
		t.Fatal(reply)
	}
}
func TestContextLimitDoesNotChangeStore(t *testing.T) {
	m := newStore(t.TempDir(), t.TempDir(), testSettings(t))
	m.settings.ContextLimit = 256
	ctx := context.Background()
	for _, scope := range []string{"agent", "user", "workspace"} {
		if _, _, err := m.add(ctx, scope, strings.Repeat("x", 2000), scope); err != nil {
			t.Fatal(err)
		}
	}
	blocks, err := m.context()
	if err != nil || len(blocks) != 3 {
		t.Fatal(blocks, err)
	}
	total := 0
	for _, block := range blocks {
		total += len(block.Text)
	}
	if total > 256 {
		t.Fatal("context budget exceeded", total)
	}
	if !strings.Contains(blocks[0].Text, "пропущена") {
		t.Fatal(blocks)
	}
	for _, scope := range []string{"agent", "user", "workspace"} {
		s, err := m.read(scope)
		if err != nil || len(s.Pending[0].Text) != 2000 {
			t.Fatal(s, err)
		}
	}
}
func TestSavedAddSurvivesCompactionFailure(t *testing.T) {
	m := newStore(t.TempDir(), t.TempDir(), testSettings(t))
	m.settings.Threshold = 1
	result, err := addAndCompact(context.Background(), m, "user", "save me", "id", func(context.Context, string, string, []note) (string, error) { return "", context.DeadlineExceeded })
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result)
	if !bytes.Contains(data, []byte(`"saved":true`)) || !bytes.Contains(data, []byte(`"status":"error"`)) {
		t.Fatal(string(data))
	}
	s, err := m.read("user")
	if err != nil || len(s.Pending) != 1 {
		t.Fatal(s, err)
	}
}
func TestCLIInheritedReadAndWatcherCancellation(t *testing.T) {
	home, workspace := fixture(t)
	t.Setenv("HORIZON_HOME", home)
	t.Setenv("HORIZON_INHERITED_ACCESS", "read")
	var out, diag bytes.Buffer
	if code := run(context.Background(), []string{"add", "--scope", "user", "--text", "forbidden", "--workspace", workspace}, nil, &out, &diag); code != 2 {
		t.Fatal(code, out.String(), diag.String())
	}
	if _, err := os.Stat(filepath.Join(home, "memory")); !os.IsNotExist(err) {
		t.Fatal("denied command created files")
	}
	t.Setenv("HORIZON_INHERITED_ACCESS", "")
	m := newStore(home, workspace, testSettings(t))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := watch(ctx, m, time.Second, func(context.Context, string, string, []note) (string, error) {
		t.Error("empty watcher invoked model")
		return "", nil
	}, &diag); err == nil {
		t.Fatal("watch did not cancel")
	}
	if _, err := os.Stat(filepath.Join(home, "memory")); !os.IsNotExist(err) {
		t.Fatal("empty watcher created files")
	}
}
