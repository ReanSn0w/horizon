package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIdleSchedulerCancelsForNewMessageAndVisitsOtherChat(t *testing.T) {
	home := readyHome(t)
	cfg, err := loadSettings(home, true)
	if err != nil {
		t.Fatal(err)
	}
	s := newStore(home, 99)
	if err := os.MkdirAll(cfg.Workspace, 0700); err != nil {
		t.Fatal(err)
	}
	base, err := filepath.EvalSymlinks(cfg.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	privateDir := filepath.Join(base, "telegram_99_1")
	groupDir := filepath.Join(base, "telegram_99_2")
	for _, bound := range []struct {
		path string
		chat string
	}{{privateDir, `{"Bot":99,"Chat":1}`}, {groupDir, `{"Bot":99,"Chat":2}`}} {
		if err := os.MkdirAll(bound.path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bound.path, ".horizon-gateway.json"), []byte(bound.chat), 0600); err != nil {
			t.Fatal(err)
		}
	}
	privateID := idleSession(t, home, privateDir, minIdleCheckpointBytes)
	groupID := idleSession(t, home, groupDir, minIdleCheckpointBytes)
	old := time.Now().Add(-13 * time.Hour)
	if err := s.update(func(v *state) error {
		v.Chats["1"] = &chat{ID: 1, Origin: 1, Type: "private", Available: true, Workspace: privateDir, Session: privateID, LastReceivedAt: old}
		v.Chats["2"] = &chat{ID: 2, Origin: 2, Type: "group", Available: true, Workspace: groupDir, Session: groupID, LastReceivedAt: old}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true,"result":[]}`)
	}))
	defer server.Close()
	firstStarted, firstStopped, secondStarted, delivered := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	run := func(ctx context.Context, dir string, args []string, input string) (string, error) {
		if !strings.Contains(strings.Join(args, " "), "sessions compact") {
			return "", errors.New("unexpected Horizon command")
		}
		if filepath.Clean(dir) == filepath.Clean(privateDir) {
			close(firstStarted)
			<-ctx.Done()
			close(firstStopped)
			return "", ctx.Err()
		}
		close(secondStarted)
		return `{"type":"compaction_completed","data":{"compaction_id":"compact"}}`, nil
	}
	g := &bridge{home: home, cfg: cfg, store: s, run: run, idleCheckInterval: 250 * time.Millisecond, tg: &telegram{base: server.URL, token: "test-token", http: server.Client()}}
	g.deliver = func(_ context.Context, c *chat, j *job) error {
		if err := g.setJob(c.ID, j.ID, func(_ *chat, current *job) error { current.Status = "sent"; return nil }); err != nil {
			return err
		}
		close(delivered)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- g.loop(ctx) }()
	await := func(name string, signal <-chan struct{}) {
		t.Helper()
		select {
		case <-signal:
		case err := <-finished:
			t.Fatalf("bridge stopped before %s: %v", name, err)
		case <-ctx.Done():
			value, _ := s.snapshot()
			t.Fatalf("timed out waiting for %s: private=%+v group=%+v", name, value.Chats["1"], value.Chats["2"])
		}
	}
	await("first compaction", firstStarted)
	select {
	case <-secondStarted:
		t.Fatal("second compaction started while first was active")
	case <-time.After(350 * time.Millisecond):
	}
	if err := s.update(func(v *state) error {
		c := v.Chats["1"]
		c.LastReceivedAt = time.Now().UTC()
		c.Jobs = append(c.Jobs, &job{ID: "new", ChatID: 1, Status: "generated", Response: "answer"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	await("cancelled compaction", firstStopped)
	await("new message delivery", delivered)
	await("second chat compaction", secondStarted)
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	value, err := s.snapshot()
	if err != nil || !value.Chats["1"].Available || value.Chats["1"].Jobs[0].Status != "sent" {
		t.Fatalf("private chat after cancellation: %+v, err=%v", value.Chats["1"], err)
	}
}

func TestIdleCompactionFailurePersistsRetryTime(t *testing.T) {
	home, workspace := readyHome(t), t.TempDir()
	id := idleSession(t, home, workspace, minIdleCheckpointBytes)
	cfg, err := loadSettings(home, true)
	if err != nil {
		t.Fatal(err)
	}
	s := newStore(home, 99)
	old := time.Now().Add(-13 * time.Hour)
	if err := s.update(func(v *state) error {
		v.Chats["1"] = &chat{ID: 1, Origin: 1, Type: "private", Available: true, Workspace: workspace, Session: id, LastReceivedAt: old}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var journal bytes.Buffer
	g := &bridge{home: home, store: s, journal: &diagnosticLog{out: &journal, runID: "test"}, run: func(context.Context, string, []string, string) (string, error) {
		return "", errors.New("mock provider failure")
	}}
	done := make(chan int64, 1)
	var wg sync.WaitGroup
	stop, started := g.startIdleCompaction(context.Background(), &chat{ID: 1, Origin: 1, Type: "private", Available: true, Workspace: workspace, Session: id, LastReceivedAt: old}, cfg, time.Now(), done, &wg)
	if !started {
		t.Fatal("compaction did not start")
	}
	defer stop()
	<-done
	wg.Wait()
	value, err := s.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	c := value.Chats["1"]
	if !c.Available || c.LastCompactAttemptAt.IsZero() || idleCompactDue(c, time.Now(), cfg.idleCompactAfter()) {
		t.Fatal("failure did not preserve chat and delay retry")
	}
	for _, event := range []string{"idle_compaction_candidate", "idle_compaction_started", "idle_compaction_error"} {
		if !strings.Contains(journal.String(), event) {
			t.Fatalf("missing diagnostic event %s", event)
		}
	}
}
