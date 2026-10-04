package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ReanSn0w/horizon/internal/plugins"
)

func TestBrowserLifecycleCreateReuseAndStop(t *testing.T) {
	home := t.TempDir()
	created, stopped, listed := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Browser-Use-API-Key") != "secret" {
			t.Errorf("missing API key")
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v4/browsers":
			listed++
			json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "totalItems": 0, "pageNumber": 1, "pageSize": 100})
		case r.Method == "POST" && r.URL.Path == "/api/v4/browsers":
			created++
			var input struct {
				Metadata map[string]string `json:"metadata"`
				Timeout  int               `json:"timeout"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Timeout != 10 || input.Metadata["horizon_turn"] != "turn-1" || input.Metadata["horizon_home"] == "" {
				t.Errorf("creation payload=%+v err=%v", input, err)
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": "browser-1", "cdpUrl": "ws://example.invalid/devtools/browser/secret", "status": "active"})
		case r.Method == "PATCH" && r.URL.Path == "/api/v4/browsers/browser-1":
			stopped++
			json.NewEncoder(w).Encode(map[string]any{"id": "browser-1", "status": "stopped"})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	s := settings{APIKey: "secret", APIURL: server.URL + "/api/v4", TimeoutMinutes: 10}
	b := newBrowserLifecycle(home, s)
	req := plugins.Request{Home: home, WorkspaceID: "workspace-1", SessionID: "session-1", TurnID: "turn-1"}
	for i := 0; i < 2; i++ {
		record, err := b.open(context.Background(), req)
		if err != nil || record.ID != "browser-1" {
			t.Fatalf("open %d: %+v %v", i, record, err)
		}
	}
	if created != 1 || listed == 0 {
		t.Fatalf("created=%d listed=%d", created, listed)
	}
	if err := b.finish(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if err := b.finish(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if stopped != 1 {
		t.Fatalf("stopped=%d", stopped)
	}
}

func TestBrowserAPIErrorsHideSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("secret-token-and-cdp-url"))
	}))
	defer server.Close()
	a := newBrowserAPI(settings{APIKey: "secret-token", APIURL: server.URL})
	_, err := a.create(context.Background(), "home", "turn", "session", "workspace", 10)
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "cdp") {
		t.Fatalf("error=%v", err)
	}
}

func TestBrowserListPaginationAndHomeFilter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("pageNumber")
		if r.URL.Query().Get("metadata") != "horizon_home=ours" || r.URL.Query().Get("filterBy") != "active" {
			t.Errorf("list query=%s", r.URL.RawQuery)
		}
		items := []any{}
		if page == "1" {
			for i := 0; i < 100; i++ {
				items = append(items, map[string]any{"id": "ours", "status": "active", "metadata": map[string]string{"horizon_home": "ours"}})
			}
		} else if page == "2" {
			items = append(items, map[string]any{"id": "second", "status": "active", "metadata": map[string]string{"horizon_home": "ours"}})
			items = append(items, map[string]any{"id": "foreign", "status": "active", "metadata": map[string]string{"horizon_home": "other"}})
		} else {
			t.Errorf("unexpected page %s", page)
		}
		pageNumber := 1
		if page == "2" {
			pageNumber = 2
		}
		json.NewEncoder(w).Encode(map[string]any{"items": items, "totalItems": 101, "pageNumber": pageNumber, "pageSize": 100})
	}))
	defer server.Close()
	a := newBrowserAPI(settings{APIKey: "test", APIURL: server.URL})
	items, err := a.list(context.Background(), "ours")
	if err != nil || len(items) != 101 || items[len(items)-1].ID != "second" {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
}

func TestBrowserAPITimeoutHasNoSecret(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	a := newBrowserAPI(settings{APIKey: "secret-token", APIURL: server.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := a.list(ctx, "home")
	if err == nil || strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), server.URL) {
		t.Fatalf("timeout err=%v", err)
	}
}

func TestUnknownCreateOutcomeIsNotRetriedBlindly(t *testing.T) {
	created := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			created++
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "totalItems": 0, "pageNumber": 1, "pageSize": 100})
	}))
	defer server.Close()
	b := newBrowserLifecycle(t.TempDir(), settings{APIKey: "test", APIURL: server.URL, TimeoutMinutes: 10})
	req := plugins.Request{SessionID: "session", TurnID: "turn", WorkspaceID: "workspace"}
	for i := 0; i < 2; i++ {
		if _, err := b.open(context.Background(), req); err == nil {
			t.Fatal("unknown creation accepted")
		}
	}
	if created != 1 {
		t.Fatalf("browser created %d times", created)
	}
	if err := b.finish(context.Background(), req); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("finalization erased uncertain creation: %v", err)
	}
}

func TestBrowserStopRequiresConfirmedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"id": "browser", "status": "active"})
	}))
	defer server.Close()
	a := newBrowserAPI(settings{APIKey: "test", APIURL: server.URL})
	if err := a.stop(context.Background(), "browser"); err == nil {
		t.Fatal("unconfirmed stop accepted")
	}
}
