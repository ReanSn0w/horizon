package daemon

import (
	"testing"

	"github.com/ReanSn0w/horizon/internal/session"
)

func TestOfflineImportedInputsStayBeforeNewInput(t *testing.T) {
	home := shortHome(t)
	workspace, err := session.NormalizeWorkspace(home)
	if err != nil {
		t.Fatal(err)
	}
	dialog := ImportedDialog{Route: "chat", Workspace: workspace, Session: "existing-session", Access: "write", Channel: true,
		Jobs: []ImportedJob{
			{OperationID: "first", State: "queued", Author: "first sender", Message: "first old input"},
			{OperationID: "second", State: "queued", Author: "second sender", Message: "second old input"},
		}}
	for range 2 {
		if err := ImportDialogs(home, "test", Defaults(), []ImportedDialog{dialog}); err != nil {
			t.Fatal(err)
		}
	}
	q, err := OpenQueue(home, Defaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	r := activeRequest(q, "new", "chat", "new input")
	r.Caller.Workspace = workspace
	r.Channel = true
	accepted := taskResult(t, q, r)
	task := q.state.Tasks[accepted.ID]
	if len(task.InputIDs) != 3 || task.Session != "existing-session" {
		t.Fatalf("import or route binding = %#v", task)
	}
	for i, want := range []string{"first old input", "second old input", "new input"} {
		if got := q.state.Inputs[task.InputIDs[i]].Text; got != want {
			t.Errorf("input %d = %q, want %q", i, got, want)
		}
	}
}
