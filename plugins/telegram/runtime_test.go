package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ReanSn0w/horizon/internal/config"
	"gopkg.in/yaml.v3"
	"strings"
	"testing"
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
	g := &bridge{home: home, cfg: cfg, store: s, run: run}
	if err := g.process(context.Background(), c, j, cfg); err != nil {
		t.Fatal(err)
	}
	v, _ := s.snapshot()
	if calls != 2 || v.Chats["1"].Jobs[0].Status != "generated" || v.Chats["1"].Jobs[0].Response != "reply" {
		t.Fatal("generation not saved")
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
