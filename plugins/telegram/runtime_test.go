package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ReanSn0w/horizon/internal/config"
	"gopkg.in/yaml.v3"
	"strings"
	"testing"
	"time"
)

func readyHome(t *testing.T) string {
	home := configHome(t)
	_, err := config.UpdateDocument(home, func(doc *yaml.Node) (bool, error) {
		root := doc.Content[0]
		putNode(root, "provider", encodedNode(map[string]string{"url": "https://example.test", "key": "unused"}))
		putNode(root, "decision", encodedNode(map[string]any{"provider": map[string]string{"url": "https://example.test", "key": "unused"}, "model": "jev"}))
		putNode(root, "default_model", encodedNode("chat"))
		putNode(root, "models", encodedNode(map[string]any{"chat": map[string]any{"model": "model", "compact_threshold": 1000}}))
		section := nodeValue(nodeValue(root, "plugins"), "telegram")
		putNode(section, "telegram", encodedNode(map[string]any{"bot_token": "test-token", "owner_user_id": 1}))
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return home
}

func TestJobDurationFollowsHorizonLimit(t *testing.T) {
	g := &bridge{}
	for _, test := range []struct {
		turn, want time.Duration
	}{
		{5 * time.Minute, 7 * time.Minute},
		{120 * time.Minute, 122 * time.Minute},
		{0, 0},
	} {
		if got := g.jobDuration(test.turn); got != test.want {
			t.Fatalf("turn %s: got %s, want %s", test.turn, got, test.want)
		}
	}
	g.jobTimeout = 20 * time.Millisecond
	if got := g.jobDuration(120 * time.Minute); got != g.jobTimeout {
		t.Fatalf("test override: got %s", got)
	}
}

func TestJobContextWithoutDeadlineStillCancels(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	job, stop := jobContext(parent, 0)
	defer stop()
	if _, limited := job.Deadline(); limited {
		t.Fatal("zero Horizon limit created a deadline")
	}
	cancel()
	if !errors.Is(job.Err(), context.Canceled) {
		t.Fatalf("cancellation not inherited: %v", job.Err())
	}
}

func TestTimedOutGenerationDoesNotBlockNextJob(t *testing.T) {
	home := readyHome(t)
	cfg, err := loadSettings(home, true)
	if err != nil {
		t.Fatal(err)
	}
	s := newStore(home, 99)
	first := &job{ID: "first", ChatID: 1, Status: "queued", Input: record{ID: 1, Author: 1, Seq: 1, Text: "first"}}
	second := &job{ID: "second", ChatID: 1, Status: "queued", Input: record{ID: 2, Author: 1, Seq: 2, Text: "second"}}
	c := &chat{ID: 1, Origin: 1, Type: "private", Available: true, Jobs: []*job{first, second}, History: []record{first.Input, second.Input}}
	if err := s.update(func(v *state) error { v.Chats["1"] = c; return nil }); err != nil {
		t.Fatal(err)
	}
	g := &bridge{home: home, cfg: cfg, store: s, jobTimeout: 30 * time.Millisecond}
	g.run = func(ctx context.Context, _ string, args []string, _ string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "sessions create") {
			return "session", nil
		}
		if first.Status == "queued" {
			<-ctx.Done()
			return "", ctx.Err()
		}
		return "second reply", nil
	}
	g.deliver = func(_ context.Context, c *chat, j *job) error {
		return g.setJob(c.ID, j.ID, func(_ *chat, j *job) error { j.Status = "sent"; return nil })
	}
	if err := g.process(context.Background(), c, first, cfg); err != nil {
		t.Fatal(err)
	}
	v, err := s.snapshot()
	if err != nil || v.Chats["1"].Jobs[0].Status != "unknown" {
		t.Fatalf("first job: %+v, %v", v.Chats["1"].Jobs[0], err)
	}
	first.Status = "unknown"
	g.jobTimeout = time.Second
	if err := g.process(context.Background(), v.Chats["1"], second, cfg); err != nil {
		t.Fatal(err)
	}
	v, err = s.snapshot()
	if err != nil || v.Chats["1"].Jobs[0].Status != "unknown" || v.Chats["1"].Jobs[1].Status != "sent" {
		t.Fatalf("jobs after retry: %+v, %v", v.Chats["1"].Jobs, err)
	}
}
func TestGenerateUsesSessionAccessAndData(t *testing.T) {
	home := readyHome(t)
	cfg, _ := loadSettings(home, true)
	s := newStore(home, 99)
	j := &job{ID: "j", ChatID: 1, Status: "evaluating", Input: record{ID: 1, Author: 1, Seq: 1, Text: "$(do not execute)"}, Thread: 7}
	c := &chat{ID: 1, Origin: 1, Type: "private", Jobs: []*job{j}, History: []record{j.Input}}
	s.update(func(v *state) error { v.Chats["1"] = c; return nil })
	calls := 0
	run := func(ctx context.Context, dir string, args []string, input string) (string, error) {
		calls++
		if strings.Contains(strings.Join(args, " "), "sessions create") {
			return "session-fixed", nil
		}
		if !strings.Contains(strings.Join(args, " "), "--session session-fixed --mode plain --access write") {
			t.Fatal(args)
		}
		var body map[string]any
		if json.Unmarshal([]byte(input), &body) != nil {
			t.Fatal("invalid structured prompt")
		}
		return "reply", nil
	}
	var journal bytes.Buffer
	g := &bridge{home: home, cfg: cfg, store: s, run: run, journal: &diagnosticLog{out: &journal, runID: "run"}}
	if err := g.process(context.Background(), c, j, cfg); err != nil {
		t.Fatal(err)
	}
	v, _ := s.snapshot()
	if calls != 2 || v.Chats["1"].Jobs[0].Status != "generated" || v.Chats["1"].Jobs[0].Response != "reply" {
		t.Fatal("generation not saved")
	}
	if !strings.Contains(journal.String(), `"event":"horizon_resume_started"`) || !strings.Contains(journal.String(), `"event":"horizon_resume_finished"`) {
		t.Fatal("Horizon process timing was not recorded")
	}
}
func TestFailedProcessIsNotReplayed(t *testing.T) {
	home := readyHome(t)
	cfg, _ := loadSettings(home, true)
	s := newStore(home, 99)
	j := &job{ID: "x", ChatID: 1, Status: "evaluating", Manual: true}
	c := &chat{ID: 1, Origin: 1, Type: "private", Jobs: []*job{j}}
	s.update(func(v *state) error { v.Chats["1"] = c; return nil })
	g := &bridge{home: home, cfg: cfg, store: s, run: func(ctx context.Context, dir string, args []string, input string) (string, error) {
		if strings.Contains(fmt.Sprint(args), "create") {
			return "id", nil
		}
		return "", errors.New("interrupted")
	}}
	if err := g.process(context.Background(), c, j, cfg); err != nil {
		t.Fatal(err)
	}
	v, _ := s.snapshot()
	if v.Chats["1"].Jobs[0].Status != "unknown" {
		t.Fatal("failed command can be replayed")
	}
}

func TestContextMarkerDoesNotConsumeQueuedMessages(t *testing.T) {
	home := readyHome(t)
	cfg, _ := loadSettings(home, true)
	s := newStore(home, 99)
	j := &job{ID: "first", ChatID: 1, Status: "evaluating", Input: record{Seq: 1, ID: 1, Author: 1, Text: "first"}}
	c := &chat{ID: 1, Origin: 1, Type: "private", History: []record{j.Input, {Seq: 2, ID: 2, Text: "waiting"}, {Seq: 3, ID: 3, Bot: true, Text: "bot"}}, Jobs: []*job{j}}
	if err := s.update(func(v *state) error { v.Chats["1"] = c; return nil }); err != nil {
		t.Fatal(err)
	}
	run := func(ctx context.Context, dir string, args []string, input string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "sessions create") {
			return "fixed", nil
		}
		return "reply", nil
	}
	g := &bridge{home: home, cfg: cfg, store: s, run: run}
	if err := g.process(context.Background(), c, j, cfg); err != nil {
		t.Fatal(err)
	}
	value, err := s.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if value.Chats["1"].ContextSeq != 1 {
		t.Fatal("queued participant input was incorrectly consumed")
	}
}

func TestDeliveryDeadlineDoesNotStopTelegram(t *testing.T) {
	home := readyHome(t)
	cfg, _ := loadSettings(home, true)
	s := newStore(home, 99)
	j := &job{ID: "job", ChatID: 1, Status: "generated", Response: "reply", Sent: []int64{42}}
	c := &chat{ID: 1, Origin: 1, Type: "private", Jobs: []*job{j}}
	if err := s.update(func(v *state) error { v.Chats["1"] = c; return nil }); err != nil {
		t.Fatal(err)
	}
	g := &bridge{home: home, cfg: cfg, store: s, run: func(context.Context, string, []string, string) (string, error) { return "session", nil }, deliver: func(context.Context, *chat, *job) error { return context.DeadlineExceeded }}
	if err := g.process(context.Background(), c, j, cfg); err != nil {
		t.Fatal("one delivery deadline stopped the scheduler", err)
	}
	value, err := s.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	saved := value.Chats["1"].Jobs[0]
	if saved.Status != "failed" || len(saved.Sent) != 1 || saved.Sent[0] != 42 {
		t.Fatalf("deadline lost confirmed effects: %+v", saved)
	}
}
