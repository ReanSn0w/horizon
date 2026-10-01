package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestQueueSerializesChatAndRunsOtherChat(t *testing.T) {
	home := readyHome(t)
	cfg, _ := loadSettings(home, true)
	s := newStore(home, 99)
	privateStarted, releasePrivate, groupSent, allSent := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := s.update(func(v *state) error {
		for _, id := range []int64{1, -2} {
			c := &chat{ID: id, Origin: id, Available: true, Type: "group"}
			if id == 1 {
				c.Type = "private"
			}
			c.Jobs = []*job{{ID: fmt.Sprint(id), ChatID: id, Status: "queued", Manual: true}}
			if id == 1 {
				c.Jobs = append(c.Jobs, &job{ID: "second", ChatID: 1, Status: "queued", Manual: true})
			}
			v.Chats[fmt.Sprint(id)] = c
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"ok":true,"result":[]}`) }))
	defer server.Close()
	var mu sync.Mutex
	privateCalls, sends := 0, 0
	run := func(ctx context.Context, dir string, args []string, input string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "sessions create") {
			return "session-" + filepath.Base(dir), nil
		}
		if strings.HasSuffix(dir, "telegram_99_1") {
			mu.Lock()
			privateCalls++
			n := privateCalls
			mu.Unlock()
			if n == 1 {
				close(privateStarted)
				select {
				case <-releasePrivate:
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
			if n == 2 {
				select {
				case <-releasePrivate:
				default:
					t.Error("second job ran before first finished")
				}
			}
		} else {
			select {
			case <-privateStarted:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return "reply", nil
	}
	g := &gateway{home: home, cfg: cfg, store: s, run: run, log: io.Discard, tg: &telegram{base: server.URL, token: "test-token", http: server.Client()}}
	g.deliver = func(ctx context.Context, c *chat, j *job) error {
		err := g.setJob(c.ID, j.ID, func(_ *chat, j *job) error { j.Status = "sent"; return nil })
		if err != nil {
			return err
		}
		if c.ID == -2 {
			close(groupSent)
		}
		mu.Lock()
		sends++
		if sends == 3 {
			close(allSent)
		}
		mu.Unlock()
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- g.loop(ctx) }()
	select {
	case <-groupSent:
	case <-ctx.Done():
		t.Fatal("independent chat was blocked")
	}
	v, err := s.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if v.Chats["1"].Jobs[0].Status != "generating" || v.Chats["1"].Jobs[1].Status != "queued" {
		t.Fatal("busy chat was not serialized")
	}
	close(releasePrivate)
	select {
	case <-allSent:
	case <-ctx.Done():
		t.Fatal("queue did not finish")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
