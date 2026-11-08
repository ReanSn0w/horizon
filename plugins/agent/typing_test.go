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

func TestTypingStartsOnlyAfterReplyDecision(t *testing.T) {
	home := readyHome(t)
	cfg, err := loadSettings(home, true)
	if err != nil {
		t.Fatal(err)
	}
	s := newStore(home, 99)
	privateJob := &job{ID: "private", ChatID: 1, Status: "queued", Input: record{ID: 1, Author: 1, Seq: 1, Text: "hello"}}
	groupJob := &job{ID: "group", ChatID: -2, Status: "queued", Input: record{ID: 2, Author: 2, Seq: 1, Text: "hello"}}
	private := &chat{ID: 1, Origin: 1, Type: "private", Available: true, Jobs: []*job{privateJob}, History: []record{privateJob.Input}}
	group := &chat{ID: -2, Origin: -2, Type: "group", Available: true, Jobs: []*job{groupJob}, History: []record{groupJob.Input}}
	if err := s.update(func(v *state) error { v.Chats["1"] = private; v.Chats["-2"] = group; return nil }); err != nil {
		t.Fatal(err)
	}
	typed := make(chan struct{}, 1)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		select {
		case typed <- struct{}{}:
		default:
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()
	g := &bridge{home: home, cfg: cfg, store: s, tg: &telegram{base: server.URL, token: "token", http: server.Client()}}
	g.run = func(ctx context.Context, _ string, args []string, _ string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "sessions create") {
			return "session", nil
		}
		select {
		case <-typed:
			return "reply", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	g.deliver = func(_ context.Context, c *chat, j *job) error {
		return g.setJob(c.ID, j.ID, func(_ *chat, j *job) error { j.Status = "sent"; return nil })
	}
	if err := g.process(context.Background(), private, privateJob, cfg); err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	if before == 0 {
		t.Fatal("no typing while generating private reply")
	}
	if err := g.process(context.Background(), group, groupJob, cfg); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != before {
		t.Fatal("typing sent for skipped group message")
	}
	value, err := s.snapshot()
	if err != nil || value.Chats["-2"].Jobs[0].Status != "skipped" {
		t.Fatal(err, value.Chats["-2"].Jobs[0].Status)
	}
}

func TestTypingRefreshesPerChatAndStops(t *testing.T) {
	type request struct {
		ChatID int64  `json:"chat_id"`
		Thread int64  `json:"message_thread_id"`
		Action string `json:"action"`
	}
	var mu sync.Mutex
	var calls []request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bottoken/sendChatAction" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		var data request
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			t.Error(err)
		}
		mu.Lock()
		calls = append(calls, data)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()
	g := &bridge{tg: &telegram{base: server.URL, token: "token", http: server.Client()}, typingInterval: 20 * time.Millisecond}
	stopPrivate := g.startTyping(context.Background(), &chat{ID: 1}, &job{ID: "private"})
	stopThread := g.startTyping(context.Background(), &chat{ID: -2}, &job{ID: "thread", Thread: 42})
	deadline := time.After(time.Second)
	for {
		mu.Lock()
		count := len(calls)
		mu.Unlock()
		if count >= 4 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("typing did not refresh")
		case <-time.After(5 * time.Millisecond):
		}
	}
	stopPrivate()
	stopThread()
	mu.Lock()
	final := append([]request(nil), calls...)
	mu.Unlock()
	seen := map[int64]bool{}
	for _, call := range final {
		if call.Action != "typing" || (call.ChatID == -2 && call.Thread != 42) || (call.ChatID == 1 && call.Thread != 0) {
			t.Fatalf("unexpected action: %+v", call)
		}
		seen[call.ChatID] = true
	}
	if !seen[1] || !seen[-2] {
		t.Fatalf("missing chat action: %+v", final)
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != len(final) {
		t.Fatalf("typing continued after stop: %d -> %d", len(final), len(calls))
	}
}

func TestTypingFailureDoesNotStopRefreshLoop(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":503,"description":"unavailable"}`))
	}))
	defer server.Close()
	client := &telegram{base: server.URL, token: "token", http: server.Client()}
	g := &bridge{tg: client, typingInterval: time.Millisecond}
	stop := g.startTyping(context.Background(), &chat{ID: 1}, &job{ID: "job"})
	time.Sleep(20 * time.Millisecond)
	stop()
	if calls.Load() < 2 {
		t.Fatalf("typing error stopped refresh after %d call(s)", calls.Load())
	}
}
