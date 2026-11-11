package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ReanSn0w/horizon/internal/session"
)

// MigrationBarrier prevents queue startup while an offline import and its
// adapter state/configuration are being committed separately.
func MigrationBarrier(home string) string {
	return filepath.Join(home, "run", "offline-migration")
}

type ImportedDialog struct {
	Route     string
	Workspace string
	Session   string
	Access    string
	Channel   bool
	Jobs      []ImportedJob
}

type ImportedJob struct {
	OperationID string
	State       string
	Author      string
	Message     string
}

// ImportDialogs appends immutable route bindings and selected pending work.
// The caller must hold the daemon singleton and maintenance locks. Import uses
// existing daemon transactions and never writes session histories.
func ImportDialogs(home, source string, settings Settings, dialogs []ImportedDialog) error {
	if !adapterName.MatchString(source) {
		return errors.New("invalid adapter source for offline import")
	}
	if len(dialogs) == 0 {
		return nil
	}
	q, err := OpenQueue(home, settings, nil)
	if err != nil {
		return err
	}
	owner := "adapter:" + source
	return q.transaction(func() error {
		for _, dialog := range dialogs {
			if err := q.importDialog(source, owner, dialog); err != nil {
				return err
			}
		}
		return q.state.validate()
	})
}

func (q *Queue) importDialog(source, owner string, dialog ImportedDialog) error {
	workspace, err := session.NormalizeWorkspace(dialog.Workspace)
	if err != nil || workspace != dialog.Workspace || dialog.Route == "" || len(dialog.Route) > 512 ||
		level(dialog.Access) == 0 || dialog.Session != "" && !sessionID.MatchString(dialog.Session) {
		return errors.New("invalid imported daemon dialog")
	}
	var root *Task
	for _, task := range q.state.Tasks {
		if task.Owner == owner && task.Route == dialog.Route && task.ActiveInput && task.ID == task.Child {
			root = task
			break
		}
	}
	if root == nil {
		root = q.newActiveTask(owner, dialog.Workspace, dialog.Access, dialog.Route, dialog.Channel, nil)
		root.Session = dialog.Session
		root.State = "succeeded"
		root.InputClosed = true
		q.event(root, "imported_dialog")
	}
	if root.Session != dialog.Session || root.Workspace != dialog.Workspace || root.Channel != dialog.Channel {
		return fmt.Errorf("cannot overwrite a different daemon route binding for %s", dialog.Route)
	}
	for _, job := range dialog.Jobs {
		if err := q.importJob(source, root, dialog.Access, job); err != nil {
			return err
		}
	}
	return nil
}

func (q *Queue) importJob(source string, root *Task, access string, job ImportedJob) error {
	if job.OperationID == "" || len(job.OperationID) > 512 || job.State != "queued" && job.State != "unknown" ||
		job.State == "queued" && (strings.TrimSpace(job.Message) == "" || len(job.Message) > MaxInputBytes ||
			strings.TrimSpace(job.Author) == "" || len(job.Author) > 512) {
		return errors.New("invalid imported daemon job")
	}
	payload, err := json.Marshal(struct {
		Route  string
		Access string
		Job    ImportedJob
	}{root.Route, access, job})
	if err != nil {
		return err
	}
	hash := sha256.Sum256(payload)
	digest := hex.EncodeToString(hash[:])
	key := root.Owner + ":" + job.OperationID
	if op, exists := q.state.Operations[key]; exists {
		if op.Hash != digest {
			return errors.New("imported operation ID conflicts with daemon state")
		}
		return nil
	}
	t := q.newActiveTask(root.Owner, root.Workspace, access, root.Route, root.Channel, root)
	t.State = job.State
	op := Operation{Hash: digest, TaskID: t.ID}
	if job.State == "queued" {
		q.state.InputSequence++
		input := InputReceipt{ID: newID(), Sequence: q.state.InputSequence, Source: source, Route: root.Route, Status: "stored"}
		q.state.Inputs[input.ID] = &storedInput{Receipt: input, TaskID: t.ID, Author: job.Author, Text: job.Message}
		t.InputIDs = []string{input.ID}
		op.InputID = input.ID
	} else {
		t.Error = "legacy execution or delivery outcome requires operator review"
		t.InputClosed = true
	}
	q.event(t, "imported_job")
	q.state.Operations[key] = op
	return nil
}
