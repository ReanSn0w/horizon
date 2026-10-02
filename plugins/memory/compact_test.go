package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ReanSn0w/horizon/internal/config"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestCompactionDueAndFailure(t *testing.T) {
	ctx := context.Background()
	m := newStore(t.TempDir(), t.TempDir(), testSettings(t))
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	calls := 0
	summarize := func(context.Context, string, string, []note) (string, error) { calls++; return "compressed", nil }
	if status, err := m.compact(ctx, "user", true, summarize); err != nil || status.Status != "empty" || calls != 0 {
		t.Fatal(status, err, calls)
	}
	for i := 0; i < 4; i++ {
		if _, _, err := m.add(ctx, "user", fmt.Sprint(i), fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	if status, err := m.compact(ctx, "user", false, summarize); err != nil || status.Status != "not_due" {
		t.Fatal(status, err)
	}
	now = now.Add(24 * time.Hour)
	if status, err := m.compact(ctx, "user", false, summarize); err != nil || status.Status != "compacted" || calls != 1 {
		t.Fatal(status, err, calls)
	}
	if _, dup, err := m.add(ctx, "user", "0", "0"); err != nil || !dup {
		t.Fatal("dedup after compaction", dup, err)
	}
	for i := 0; i < 5; i++ {
		if _, _, err := m.add(ctx, "user", fmt.Sprint(i), "new"+fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	failed := func(context.Context, string, string, []note) (string, error) {
		calls++
		return "", errors.New("offline")
	}
	if _, err := m.compact(ctx, "user", false, failed); err == nil {
		t.Fatal("failure missing")
	}
	if status, err := m.compact(ctx, "user", false, summarize); err != nil || status.Status != "cooldown" || calls != 2 {
		t.Fatal(status, err, calls)
	}
	s, err := m.read("user")
	if err != nil || s.Summary != "compressed" || len(s.Pending) != 5 {
		t.Fatal(s, err)
	}
	now = now.Add(5 * time.Minute)
	if status, err := m.compact(ctx, "user", false, summarize); err != nil || status.Status != "compacted" {
		t.Fatal(status, err)
	}
}
func TestConcurrentCompactionAndClear(t *testing.T) {
	for _, clear := range []bool{false, true} {
		t.Run(fmt.Sprint(clear), func(t *testing.T) {
			ctx := context.Background()
			m := newStore(t.TempDir(), t.TempDir(), testSettings(t))
			m.settings.Threshold = 1
			if _, _, err := m.add(ctx, "workspace", "old", "old"); err != nil {
				t.Fatal(err)
			}
			entered := make(chan struct{})
			release := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				_, err := m.compact(ctx, "workspace", false, func(context.Context, string, string, []note) (string, error) {
					close(entered)
					<-release
					return "old summary", nil
				})
				done <- err
			}()
			<-entered
			if status, err := m.compact(ctx, "workspace", false, func(context.Context, string, string, []note) (string, error) {
				t.Error("duplicate model call")
				return "", nil
			}); err != nil || (status.Status != "busy" && status.Status != "cooldown") {
				t.Fatal(status, err)
			}
			if clear {
				if err := m.clear(ctx, "workspace"); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := m.add(ctx, "workspace", "new", "new"); err != nil {
				t.Fatal(err)
			}
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			s, err := m.read("workspace")
			if err != nil || len(s.Pending) != 1 || s.Pending[0].ID != "new" {
				t.Fatal(s, err)
			}
			if clear && s.Summary != "" || !clear && s.Summary != "old summary" {
				t.Fatal(s)
			}
		})
	}
}
func TestInvalidSummaryAndCommitFailureKeepNotes(t *testing.T) {
	for _, kind := range []string{"empty", "large", "commit"} {
		t.Run(kind, func(t *testing.T) {
			m := newStore(t.TempDir(), t.TempDir(), testSettings(t))
			ctx := context.Background()
			if _, _, err := m.add(ctx, "agent", "keep", "id"); err != nil {
				t.Fatal(err)
			}
			if kind == "commit" {
				original := m.save
				writes := 0
				m.save = func(path string, s state) error {
					writes++
					if writes == 2 {
						return errors.New("disk full")
					}
					return original(path, s)
				}
			}
			_, err := m.compact(ctx, "agent", true, func(context.Context, string, string, []note) (string, error) {
				switch kind {
				case "empty":
					return "", nil
				case "large":
					return string(make([]byte, m.settings.SummaryLimit+1)), nil
				}
				return "summary", nil
			})
			if err == nil {
				t.Fatal("invalid compaction succeeded")
			}
			s, err := m.read("agent")
			if err != nil || len(s.Pending) != 1 || s.Summary != "" || s.LastCompactedAt != nil {
				t.Fatal(s, err)
			}
		})
	}
}
func TestSummaryUsesOrdinaryResponsesWithoutHistory(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/responses" {
			t.Error(r.URL.Path)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request["store"] != false || len(request["tools"].([]any)) != 0 || request["model"] != "memory-model" {
			t.Error(request)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"id":"summary","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"Durable fact"}]}]}}`+"\n\n")
	}))
	defer server.Close()
	cfg := config.Config{DefaultModel: "memory", Models: map[string]config.Model{"memory": {Model: "memory-model", CompactThreshold: 1000}}, Provider: config.Provider{URL: server.URL, Key: "test"}}
	s, err := loadSettings(cfg)
	if err != nil {
		t.Fatal(err)
	}
	result, err := apiSummarizer(cfg, s)(context.Background(), "agent", "old", []note{{ID: "id", Text: "new", At: time.Now()}})
	if err != nil || result != "Durable fact" || calls.Load() != 1 {
		t.Fatal(result, err, calls.Load())
	}
}
