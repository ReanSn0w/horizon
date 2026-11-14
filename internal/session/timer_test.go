package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTimerOriginSurvivesStorageForkAndRecovery(t *testing.T) {
	store := NewStore(t.TempDir())
	workspace, err := store.ResolveWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	origin := TimerOrigin{TimerID: "nap", TaskID: strings.Repeat("a", 32)}
	created, err := store.CreateTimer(workspace, origin)
	if err != nil {
		t.Fatal(err)
	}
	path, _ := store.SessionPath(workspace, created.SessionID)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var initial Session
	if err = json.Unmarshal(data, &initial); err != nil || initial.Timer == nil || *initial.Timer != origin || initial.FormatVersion != 1 {
		t.Fatal(initial.Timer, err)
	}
	for _, status := range []Status{StatusCompleted, StatusFailed, StatusCancelled} {
		locked, value, err := store.AcquireForResume(workspace, created.SessionID, "task")
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		value.Turns = append(value.Turns, Turn{ID: string(status), Status: status, StartedAt: now, CompletedAt: &now, Model: ModelProfile{Name: "test", Model: "test", CompactThreshold: 1000}})
		err = locked.Save(value)
		locked.Close()
		if err != nil {
			t.Fatal(err)
		}
		read, err := store.ReadByID(created.SessionID)
		if err != nil || read.Timer == nil || *read.Timer != origin {
			t.Fatal(read, err)
		}
	}
	lock, err := store.LockSession(workspace, created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	err = lock.RecordCompaction(Compaction{ID: "compact", BoundaryTurnID: string(StatusCompleted), ModelProfile: "test", CreatedAt: time.Now(), Items: []json.RawMessage{json.RawMessage(`{"type":"compaction","encrypted_content":"opaque"}`)}})
	lock.Close()
	if err != nil {
		t.Fatal(err)
	}
	read, err := store.ReadByID(created.SessionID)
	if err != nil || read.Timer == nil || *read.Timer != origin {
		t.Fatal(read, err)
	}
	fork, err := store.Fork(workspace, created.SessionID)
	if err != nil || fork.Timer == nil || *fork.Timer != origin {
		t.Fatal(fork, err)
	}
	// Force registry recovery from the actual session files.
	if err := os.WriteFile(filepath.Join(store.DialogsDir(), "workspaces.json"), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	listed, err := store.ListAll(false, time.Time{})
	if err != nil || len(listed) != 2 {
		t.Fatal(listed, err)
	}
	for _, item := range listed {
		if item.Session.Timer == nil || *item.Session.Timer != origin {
			t.Fatal(item.Session.Timer)
		}
	}
	ordinary, err := store.Create(workspace)
	if err != nil || ordinary.Timer != nil {
		t.Fatal(ordinary, err)
	}
}

func TestInvalidTimerOriginDoesNotCreateSession(t *testing.T) {
	store := NewStore(t.TempDir())
	workspace, _ := store.ResolveWorkspace(t.TempDir())
	for _, origin := range []TimerOrigin{{}, {TimerID: "nap"}, {TaskID: strings.Repeat("a", 32)}, {TimerID: "../escape", TaskID: strings.Repeat("a", 32)}} {
		if _, err := store.CreateTimer(workspace, origin); err == nil {
			t.Fatal("accepted", origin)
		}
	}
	items, err := store.List(workspace, false)
	if err != nil || len(items) != 0 {
		t.Fatal(items, err)
	}
}
