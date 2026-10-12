package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProgressMessageUsesSafeEventsAndIsRemoved(t *testing.T) {
	home := readyHome(t)
	cfg, err := loadSettings(home, true)
	if err != nil {
		t.Fatal(err)
	}
	s := newStore(home, 99)
	j := &job{ID: "work", ChatID: 1, Status: "generating", Thread: 42, Input: record{ID: 10, Author: 1, Text: "SECRET_INPUT"}}
	c := &chat{ID: 1, Origin: 1, Type: "private", Jobs: []*job{j}}
	if err := s.update(func(v *state) error { v.Chats["1"] = c; return nil }); err != nil {
		t.Fatal(err)
	}
	type call struct {
		method string
		data   map[string]any
	}
	var mu sync.Mutex
	var calls []call
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data map[string]any
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			t.Error(err)
		}
		method := strings.TrimPrefix(r.URL.Path, "/bottoken/")
		mu.Lock()
		calls = append(calls, call{method, data})
		mu.Unlock()
		switch method {
		case "sendMessage", "editMessageText":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":77}}`))
		case "deleteMessage":
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		default:
			t.Errorf("unexpected method %s", method)
		}
	}))
	defer server.Close()
	g := &bridge{home: home, cfg: cfg, store: s, tg: &telegram{base: server.URL, token: "token", http: server.Client()}, progressDelay: 10 * time.Millisecond, progressInterval: 25 * time.Millisecond, progressTick: 5 * time.Millisecond}
	p := g.startProgress(context.Background(), cfg, c, j)
	p.observe(childEvent{Type: "tool_started", Data: map[string]json.RawMessage{"name": json.RawMessage(`"shell_exec"`), "arguments": json.RawMessage(`{"command":"SECRET_COMMAND"}`)}})
	deadline := time.After(time.Second)
	for {
		value, err := s.snapshot()
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		count := len(calls)
		mu.Unlock()
		if value.Chats["1"].Jobs[0].ProgressID == 77 && count >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("progress was not saved or refreshed")
		case <-time.After(5 * time.Millisecond):
		}
	}
	p.stop()
	if err := g.setJob(1, j.ID, func(_ *chat, saved *job) error { saved.Status = "sent"; return nil }); err != nil {
		t.Fatal(err)
	}
	g.finishProgress(1, j.ID)
	value, err := s.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if value.Chats["1"].Jobs[0].ProgressID != 0 || len(value.Chats["1"].History) != 0 {
		t.Fatal("progress remained in job or conversation history")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls[0].method != "sendMessage" || calls[len(calls)-1].method != "deleteMessage" {
		t.Fatalf("unexpected methods: %+v", calls)
	}
	if calls[0].data["message_thread_id"] != float64(42) {
		t.Fatalf("missing topic: %+v", calls[0].data)
	}
	for _, call := range calls {
		encoded, _ := json.Marshal(call.data)
		if strings.Contains(string(encoded), "SECRET_COMMAND") || strings.Contains(string(encoded), "SECRET_INPUT") {
			t.Fatalf("private content leaked: %s", encoded)
		}
	}
}

func TestProgressRecoveryMarksUnknownWithoutNewMessage(t *testing.T) {
	home := readyHome(t)
	s := newStore(home, 99)
	j := &job{ID: "work", ChatID: 1, Status: "generating", ProgressID: 77}
	if err := s.update(func(v *state) error { v.Chats["1"] = &chat{ID: 1, Origin: 1, Jobs: []*job{j}}; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.recover(); err != nil {
		t.Fatal(err)
	}
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, strings.TrimPrefix(r.URL.Path, "/bottoken/"))
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":77}}`))
	}))
	defer server.Close()
	g := &bridge{store: s, tg: &telegram{base: server.URL, token: "token", http: server.Client()}}
	g.recoverProgress(context.Background())
	value, err := s.snapshot()
	if err != nil || value.Chats["1"].Jobs[0].Status != "unknown" || value.Chats["1"].Jobs[0].ProgressID != 0 {
		t.Fatalf("recovery: %+v, %v", value.Chats["1"].Jobs[0], err)
	}
	if len(methods) != 1 || methods[0] != "editMessageText" {
		t.Fatalf("recovery methods: %+v", methods)
	}
}

func TestProgressModeAndUnknownSendOutcome(t *testing.T) {
	home := readyHome(t)
	cfg, err := loadSettings(home, true)
	if err != nil {
		t.Fatal(err)
	}
	s := newStore(home, 99)
	j := &job{ID: "group", ChatID: -2, Status: "generating"}
	c := &chat{ID: -2, Origin: -2, Type: "group", Jobs: []*job{j}}
	if err := s.update(func(v *state) error { v.Chats["-2"] = c; return nil }); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`invalid response`))
	}))
	defer server.Close()
	g := &bridge{home: home, store: s, tg: &telegram{base: server.URL, token: "token", http: server.Client()}, progressDelay: 5 * time.Millisecond, progressInterval: 5 * time.Millisecond, progressTick: 2 * time.Millisecond}
	p := g.startProgress(context.Background(), cfg, c, j)
	time.Sleep(25 * time.Millisecond)
	p.stop()
	if calls.Load() != 0 {
		t.Fatalf("default private mode sent to group: %d", calls.Load())
	}
	cfg.ProgressMessages = "all"
	p = g.startProgress(context.Background(), cfg, c, j)
	time.Sleep(40 * time.Millisecond)
	p.stop()
	if calls.Load() != 1 {
		t.Fatalf("unknown send outcome was retried: %d", calls.Load())
	}
	value, err := s.snapshot()
	if err != nil || value.Chats["-2"].Jobs[0].ProgressID != 0 {
		t.Fatal("unconfirmed progress ID saved", err)
	}
}

func TestProgressRateLimitWaitsBeforeRetry(t *testing.T) {
	home := readyHome(t)
	cfg, err := loadSettings(home, true)
	if err != nil {
		t.Fatal(err)
	}
	s := newStore(home, 99)
	j := &job{ID: "work", ChatID: 1, Status: "generating"}
	c := &chat{ID: 1, Origin: 1, Type: "private", Jobs: []*job{j}}
	if err := s.update(func(v *state) error { v.Chats["1"] = c; return nil }); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"slow down","parameters":{"retry_after":1}}`))
	}))
	defer server.Close()
	g := &bridge{home: home, store: s, tg: &telegram{base: server.URL, token: "token", http: server.Client()}, progressDelay: 5 * time.Millisecond, progressInterval: 5 * time.Millisecond, progressTick: 2 * time.Millisecond}
	p := g.startProgress(context.Background(), cfg, c, j)
	time.Sleep(100 * time.Millisecond)
	p.stop()
	if calls.Load() != 1 {
		t.Fatalf("ignored retry_after: %d calls", calls.Load())
	}
}

func TestProgressFinishRetriesAfterEditFailure(t *testing.T) {
	home := readyHome(t)
	s := newStore(home, 99)
	j := &job{ID: "work", ChatID: 1, Status: "unknown", ProgressID: 77}
	if err := s.update(func(v *state) error { v.Chats["1"] = &chat{ID: 1, Origin: 1, Jobs: []*job{j}}; return nil }); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"ok":false,"error_code":503,"description":"unavailable"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":77}}`))
	}))
	defer server.Close()
	g := &bridge{store: s, tg: &telegram{base: server.URL, token: "token", http: server.Client()}}
	g.finishProgress(1, j.ID)
	value, err := s.snapshot()
	if err != nil || value.Chats["1"].Jobs[0].ProgressID != 77 {
		t.Fatal("progress ID lost after edit failure", err)
	}
	g.recoverProgress(context.Background())
	value, err = s.snapshot()
	if err != nil || value.Chats["1"].Jobs[0].ProgressID != 0 || calls.Load() != 2 {
		t.Fatal("recovery did not finish progress", err, calls.Load())
	}
}

func TestProgressIsNotResentWhenSavingMessageIDFails(t *testing.T) {
	home := readyHome(t)
	cfg, err := loadSettings(home, true)
	if err != nil {
		t.Fatal(err)
	}
	s := newStore(home, 99)
	j := &job{ID: "work", ChatID: 1, Status: "generating"}
	c := &chat{ID: 1, Origin: 1, Type: "private", Jobs: []*job{j}}
	if err := s.update(func(v *state) error { v.Chats["1"] = c; return nil }); err != nil {
		t.Fatal(err)
	}
	storeCtx, cancelStore := context.WithCancel(context.Background())
	s.ctx = storeCtx
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		cancelStore() // Simulates shutdown after Telegram accepts the message.
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":77}}`))
	}))
	defer server.Close()
	g := &bridge{home: home, store: s, tg: &telegram{base: server.URL, token: "token", http: server.Client()}, progressDelay: 5 * time.Millisecond, progressInterval: 5 * time.Millisecond, progressTick: 2 * time.Millisecond}
	p := g.startProgress(context.Background(), cfg, c, j)
	time.Sleep(40 * time.Millisecond)
	p.stop()
	if calls.Load() != 1 {
		t.Fatalf("sent again after uncertain save: %d", calls.Load())
	}
	s.ctx = nil
	value, err := s.snapshot()
	if err != nil || value.Chats["1"].Jobs[0].ProgressID != 0 {
		t.Fatal("unconfirmed message ID persisted", err)
	}
}
