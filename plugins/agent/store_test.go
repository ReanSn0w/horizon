package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestStoreAtomicQueue(t *testing.T) {
	s := newStore(t.TempDir(), 9)
	if err := s.update(func(v *state) error { v.Chats["1"] = &chat{ID: 1}; return nil }); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.update(func(v *state) error {
				return enqueue(v, v.Chats["1"], &job{ID: requestID(), ChatID: 1, Status: "queued"})
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	v, err := s.snapshot()
	if err != nil || len(v.Chats["1"].Jobs) != 40 {
		t.Fatalf("%v %d", err, len(v.Chats["1"].Jobs))
	}
	s.update(func(v *state) error { v.Chats["1"].Jobs[0].Status = "generating"; return nil })
	if err := s.recover(); err != nil {
		t.Fatal(err)
	}
	v, _ = s.snapshot()
	if v.Chats["1"].Jobs[0].Status != "unknown" {
		t.Fatal("unsafe retry")
	}
	p := filepath.Join(s.dir, "state.yaml")
	os.WriteFile(p, []byte("version: 99"), 0600)
	if s.update(func(v *state) error { return nil }) == nil {
		t.Fatal("overwrote corrupt state")
	}
}

func TestStoreReadsLegacyChatAndPersistsIdleTimes(t *testing.T) {
	s := newStore(t.TempDir(), 9)
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		t.Fatal(err)
	}
	legacy := "version: 1\nbot_id: 9\noffset: 0\nchats:\n  '1':\n    origin: 1\n    id: 1\n    available: true\n    last_at: 2026-10-07T00:00:00Z\naliases: {}\n"
	if err := os.WriteFile(filepath.Join(s.dir, "state.yaml"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	value, err := s.snapshot()
	if err != nil || !value.Chats["1"].LastReceivedAt.IsZero() || !value.Chats["1"].LastCompactAttemptAt.IsZero() {
		t.Fatalf("legacy state: %+v, err=%v", value.Chats["1"], err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.update(func(v *state) error {
		v.Chats["1"].LastReceivedAt = now
		v.Chats["1"].LastCompactAttemptAt = now
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	value, err = s.snapshot()
	if err != nil || !value.Chats["1"].LastReceivedAt.Equal(now) || !value.Chats["1"].LastCompactAttemptAt.Equal(now) {
		t.Fatalf("idle times were not saved: %+v, err=%v", value.Chats["1"], err)
	}
}
func TestProcessLock(t *testing.T) {
	home := t.TempDir()
	f, err := lockFile(filepath.Join(home, "gateway", "process.lock"), true)
	if err != nil {
		t.Fatal(err)
	}
	if !telegramRunning(home) {
		t.Fatal("not detected")
	}
	unlock(f)
	if telegramRunning(home) {
		t.Fatal("stale lock treated as running")
	}
}

func TestStoreLockWaitCanBeCancelled(t *testing.T) {
	s := newStore(t.TempDir(), 9)
	lock, err := lockFile(filepath.Join(s.dir, "state.lock"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock(lock)
	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = ctx
	done := make(chan error, 1)
	go func() { _, err := s.snapshot(); done <- err }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("state lock ignored cancellation")
	}
}

func TestRecoveryPreservesSafeAndUnknownJobs(t *testing.T) {
	s := newStore(t.TempDir(), 9)
	statuses := []string{"queued", "evaluating", "generated", "generating", "sending"}
	if err := s.update(func(v *state) error {
		c := &chat{ID: 1}
		for i, status := range statuses {
			c.Jobs = append(c.Jobs, &job{ID: fmt.Sprint(i), ChatID: 1, Status: status, Response: "saved", Sent: []int64{7}})
		}
		v.Chats["1"] = c
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.recover(); err != nil {
		t.Fatal(err)
	}
	v, err := s.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"queued", "queued", "generated", "unknown", "unknown"} {
		j := v.Chats["1"].Jobs[i]
		if j.Status != want || j.Response != "saved" || len(j.Sent) != 1 {
			t.Fatal(j)
		}
	}
}
