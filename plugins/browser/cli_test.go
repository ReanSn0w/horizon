package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
