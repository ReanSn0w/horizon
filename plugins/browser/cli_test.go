package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ReanSn0w/horizon/internal/session"
)

func TestBrowserCLIListAndCloseStale(t *testing.T) {
	home := t.TempDir()
	state := newBrowserState(home)
	var homeID string
	if err := state.withLock(func() (err error) { homeID, err = state.homeID(); return err }); err != nil {
		t.Fatal(err)
	}
	stops := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v4/browsers":
			page := r.URL.Query().Get("pageNumber")
			items := []any{}
			if page == "1" {
				items = append(items, map[string]any{"id": "orphan", "status": "active", "startedAt": "2026-01-01", "timeoutAt": "2026-01-01", "metadata": map[string]string{"horizon_home": homeID, "horizon_turn": "turn", "horizon_session": "session", "horizon_workspace": "workspace"}})
			}
			json.NewEncoder(w).Encode(map[string]any{"items": items, "totalItems": 1, "pageNumber": 1, "pageSize": 100})
		case r.Method == "GET" && r.URL.Path == "/api/v4/browsers/foreign":
			json.NewEncoder(w).Encode(map[string]any{"id": "foreign", "status": "active", "metadata": map[string]string{"horizon_home": "someone-else"}})
		case r.Method == "PATCH":
			stops[strings.TrimPrefix(r.URL.Path, "/api/v4/browsers/")]++
			json.NewEncoder(w).Encode(map[string]string{"status": "stopped"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	b := newBrowserLifecycle(home, settings{APIKey: "test", APIURL: server.URL + "/api/v4"})
	var out bytes.Buffer
	if err := b.listCLI(context.Background(), homeID, &out); err != nil || !strings.Contains(out.String(), "orphan") || !strings.Contains(out.String(), "оставшийся после сбоя") {
		t.Fatalf("list=%q err=%v", out.String(), err)
	}
	if err := b.closeOneCLI(context.Background(), homeID, "foreign", &out); err == nil || len(stops) != 0 {
		t.Fatal("foreign browser was accepted")
	}
	if err := b.closeStaleCLI(context.Background(), homeID, &out); err != nil || stops["orphan"] != 1 {
		t.Fatalf("stale close=%v stops=%v", err, stops)
	}
}

func TestBrowserCLIUnavailableState(t *testing.T) {
	home := t.TempDir()
	state := newBrowserState(home)
	var homeID string
	if err := state.withLock(func() (err error) { homeID, err = state.homeID(); return err }); err != nil {
		t.Fatal(err)
	}
	b := newBrowserLifecycle(home, settings{APIKey: "test", APIURL: "http://127.0.0.1:1/api/v4"})
	var out bytes.Buffer
	if err := b.listCLI(context.Background(), homeID, &out); err == nil || out.Len() != 0 {
		t.Fatalf("unavailable list=%q err=%v", out.String(), err)
	}
}

func TestBrowserCLIListsLocalRecordWithoutRemoteSession(t *testing.T) {
	home := t.TempDir()
	state := newBrowserState(home)
	var homeID string
	if err := state.withLock(func() (err error) {
		homeID, err = state.homeID()
		if err != nil {
			return err
		}
		return state.save(browserRecord{ID: "missing-remote", CDPURL: "ws://private", SessionID: "session", TurnID: "turn", WorkspaceID: "workspace"})
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "totalItems": 0, "pageNumber": 1, "pageSize": 100})
	}))
	defer server.Close()
	b := newBrowserLifecycle(home, settings{APIKey: "test", APIURL: server.URL})
	var out bytes.Buffer
	if err := b.listCLI(context.Background(), homeID, &out); err != nil || !strings.Contains(out.String(), "локальная запись без активного браузера") || strings.Contains(out.String(), "ws://private") {
		t.Fatalf("list=%q err=%v", out.String(), err)
	}
	out.Reset()
	if err := b.closeStaleCLI(context.Background(), homeID, &out); err != nil || !strings.Contains(out.String(), "removed 1 local records") {
		t.Fatalf("close stale=%q err=%v", out.String(), err)
	}
}

func TestCloseStaleSkipsLockedTurn(t *testing.T) {
	home := t.TempDir()
	state := newBrowserState(home)
	var homeID string
	if err := state.withLock(func() (err error) { homeID, err = state.homeID(); return err }); err != nil {
		t.Fatal(err)
	}
	lockDir := filepath.Join(home, "dialogs", "workspace")
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(lockDir, "session.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	stopped := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PATCH" {
			stopped = true
		}
		json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{"id": "active", "status": "active", "metadata": map[string]string{"horizon_home": homeID, "horizon_session": "session", "horizon_workspace": "workspace", "horizon_turn": "turn"}}}, "totalItems": 1, "pageNumber": 1, "pageSize": 100})
	}))
	defer server.Close()
	b := newBrowserLifecycle(home, settings{APIKey: "test", APIURL: server.URL})
	var out bytes.Buffer
	if err := b.closeStaleCLI(context.Background(), homeID, &out); err != nil || stopped || !strings.Contains(out.String(), "closed 0") {
		t.Fatalf("out=%q stopped=%t err=%v", out.String(), stopped, err)
	}
}

func TestBrowserTurnBusyDistinguishesPreviousTurnInSameSession(t *testing.T) {
	home, workspaceDir := t.TempDir(), t.TempDir()
	store := session.NewStore(home)
	workspace, err := store.ResolveWorkspace(workspaceDir)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(workspace)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := store.LockSession(workspace, created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	for _, id := range []string{"previous", "current"} {
		if err := locked.StartTurn(session.Turn{ID: id, Status: session.StatusActive, StartedAt: time.Now(), Model: session.ModelProfile{Name: "test", Model: "test", CompactThreshold: 1000}}); err != nil {
			t.Fatal(err)
		}
		if id == "previous" {
			if err := locked.CompleteTurn(id, session.StatusCompleted, nil, nil, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
	}
	state := newBrowserState(home)
	previousBusy, err := state.turnBusy(created.SessionID, workspace.ID, "previous")
	if err != nil || previousBusy {
		t.Fatalf("previous busy=%t err=%v", previousBusy, err)
	}
	currentBusy, err := state.turnBusy(created.SessionID, workspace.ID, "current")
	if err != nil || !currentBusy {
		t.Fatalf("current busy=%t err=%v", currentBusy, err)
	}
}
