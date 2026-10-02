package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/ReanSn0w/horizon/internal/config"
	"gopkg.in/yaml.v3"
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
	g := &bridge{home: home, cfg: cfg, store: s, run: run, log: io.Discard, tg: &telegram{base: server.URL, token: "test-token", http: server.Client()}}
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

func TestBusyGroupCannotStarvePrivateChat(t *testing.T) {
	for _, tc := range []struct {
		parallel int
		timeout  bool
	}{{1, false}, {2, false}, {1, true}, {2, true}} {
		parallel := tc.parallel
		t.Run(fmt.Sprintf("parallel-%d-timeout-%t", parallel, tc.timeout), func(t *testing.T) {
			home := readyHome(t)
			_, err := config.UpdateDocument(home, func(doc *yaml.Node) (bool, error) {
				section := nodeValue(nodeValue(doc.Content[0], "plugins"), "telegram")
				putNode(section, "max_parallel_chats", encodedNode(parallel))
				putNode(section, "group_defaults", encodedNode(groupSettings{false, "conversation"}))
				return true, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			cfg, _ := loadSettings(home, true)
			s := newStore(home, 99)
			if err := s.update(func(v *state) error {
				c := &chat{ID: -2, Origin: -2, Available: true, Type: "group"}
				for i := range 8 {
					c.Jobs = append(c.Jobs, &job{ID: fmt.Sprint(i), ChatID: -2, Status: "queued", Input: record{Author: 2}})
				}
				v.Chats["-2"] = c
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			groupStarted, release, privateDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var mu sync.Mutex
			decisions := 0
			privateCalled := false
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			run := func(ctx context.Context, dir string, args []string, input string) (string, error) {
				joined := strings.Join(args, " ")
				if strings.Contains(joined, "sessions create") {
					return "session", nil
				}
				if strings.Contains(joined, "decision -") {
					mu.Lock()
					decisions++
					n := decisions
					mu.Unlock()
					if n == 1 {
						close(groupStarted)
						select {
						case <-release:
						case <-ctx.Done():
							return "", ctx.Err()
						}
					}
					if tc.timeout {
						<-ctx.Done()
						return "", ctx.Err()
					}
					return `{"answers":{"should_reply":{"type":"noul","noul":0.1}}}`, nil
				}
				mu.Lock()
				n := decisions
				privateCalled = true
				mu.Unlock()
				if parallel == 1 && n > 1 {
					t.Errorf("private chat starved behind %d group evaluations", n)
				}
				return "private reply", nil
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"ok":true,"result":[]}`) }))
			defer server.Close()
			g := &bridge{home: home, cfg: cfg, store: s, run: run, tg: &telegram{base: server.URL, token: "test", http: server.Client()}}
			if tc.timeout {
				g.jobTimeout = 700 * time.Millisecond
			}
			g.deliver = func(ctx context.Context, c *chat, j *job) error {
				err := g.setJob(c.ID, j.ID, func(_ *chat, j *job) error { j.Status = "sent"; return nil })
				close(privateDone)
				return err
			}
			done := make(chan error, 1)
			go func() { done <- g.loop(ctx) }()
			select {
			case <-groupStarted:
			case <-ctx.Done():
				t.Fatal("group did not start")
			}
			if err := s.update(func(v *state) error {
				v.Chats["1"] = &chat{ID: 1, Origin: 1, Type: "private", Available: true, Jobs: []*job{{ID: "private", ChatID: 1, Status: "queued", Input: record{Author: 1}}}}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if parallel == 1 {
				close(release)
			}
			select {
			case <-privateDone:
			case <-ctx.Done():
				t.Error("private chat never completed")
			}
			if parallel == 2 {
				close(release)
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if !privateCalled {
				t.Fatal("private generation missing")
			}
		})
	}
}

type notifyWriter func([]byte) (int, error)

func (w notifyWriter) Write(p []byte) (int, error) { return w(p) }

func TestPollingRecoversNetworkFailureWithoutRestart(t *testing.T) {
	home := readyHome(t)
	cfg, _ := loadSettings(home, true)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		polls++
		if polls == 1 {
			fmt.Fprint(w, `{"ok":false,"error_code":500,"description":"temporary"}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":[{"update_id":5,"message":{"message_id":42,"chat":{"id":1,"type":"private"},"from":{"id":1},"text":"input"}}]}`)
	}))
	defer server.Close()
	saved := false
	g := &bridge{home: home, cfg: cfg, store: newStore(home, 99), health: newHealth("run"), tg: &telegram{base: server.URL, token: "test", http: server.Client()}}
	g.journal = &diagnosticLog{out: notifyWriter(func(p []byte) (int, error) {
		if strings.Contains(string(p), `"event":"message_queued"`) {
			saved = true
			cancel()
		}
		return len(p), nil
	}), runID: "run"}
	err := g.poll(ctx)
	if !errors.Is(err, context.Canceled) || !saved {
		t.Fatal(err, saved)
	}
	v, err := g.store.snapshot()
	if err != nil || v.Offset != 6 || len(v.Chats["1"].Jobs) != 1 {
		t.Fatal(v, err)
	}
}
