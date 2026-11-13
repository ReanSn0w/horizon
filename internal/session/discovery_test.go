package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestListAllFiltersAcrossWorkspacesAndReadsByID(t *testing.T) {
	home := t.TempDir()
	store := NewStore(home)
	root := t.TempDir()
	firstDir, secondDir := filepath.Join(root, "first"), filepath.Join(root, "second")
	for _, dir := range []string{firstDir, secondDir} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.ResolveWorkspace(firstDir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.ResolveWorkspace(secondDir)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base }
	old, err := store.Create(first)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return base.Add(24 * time.Hour) }
	recent, err := store.Create(second)
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.ListAll(false, base.Add(time.Nanosecond))
	if err != nil || len(items) != 1 || items[0].Session.SessionID != recent.SessionID {
		t.Fatalf("recent list = %#v, %v", items, err)
	}
	items, err = store.ListAll(false, base)
	if err != nil || len(items) != 2 || items[0].Session.SessionID != recent.SessionID || items[1].Session.SessionID != old.SessionID {
		t.Fatalf("boundary list = %#v, %v", items, err)
	}
	lock, err := store.LockSession(second, recent.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	active, err := store.ListAll(true, time.Time{})
	lock.Close()
	if err != nil || len(active) != 1 || active[0].Session.SessionID != recent.SessionID || !active[0].Active {
		t.Fatalf("active list = %#v, %v", active, err)
	}
	if err := os.Remove(secondDir); err != nil {
		t.Fatal(err)
	}
	read, err := store.ReadByID(recent.SessionID)
	if err != nil || read.Workspace != second.Dir {
		t.Fatalf("read after workspace removal = %#v, %v", read, err)
	}
}

func TestReadByIDRejectsDuplicateAndListAllRecoversRegistry(t *testing.T) {
	store := NewStore(t.TempDir())
	root := t.TempDir()
	for _, name := range []string{"first", "second"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	first, _ := store.ResolveWorkspace(filepath.Join(root, "first"))
	second, _ := store.ResolveWorkspace(filepath.Join(root, "second"))
	created, err := store.Create(first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(second); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.DialogsDir(), "workspaces.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListAll(false, time.Time{})
	if err != nil || len(items) != 2 {
		t.Fatalf("recovered list = %#v, %v", items, err)
	}
	copy := *created
	copy.Workspace = second.Dir
	data, err := json.Marshal(&copy)
	if err != nil {
		t.Fatal(err)
	}
	path, _ := store.SessionPath(second, created.SessionID)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadByID(created.SessionID); err == nil || !strings.Contains(err.Error(), "multiple workspaces") {
		t.Fatalf("duplicate ID accepted: %v", err)
	}
}
