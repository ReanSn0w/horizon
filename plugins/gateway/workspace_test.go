package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestChatWorkspaceBinding(t *testing.T) {
	home := t.TempDir()
	cfg := defaults(home)
	s := newStore(home, 9)
	s.update(func(v *state) error {
		v.Chats["1"] = &chat{ID: 1, Origin: 1}
		v.Chats["2"] = &chat{ID: 2, Origin: 2}
		return nil
	})
	run := func(ctx context.Context, dir string, args []string, input string) (string, error) {
		return "session-1\n", nil
	}
	v, _ := s.snapshot()
	c, err := workspace(context.Background(), home, cfg, s, v.Chats["1"], run)
	if err != nil {
		t.Fatal(err)
	}
	v, _ = s.snapshot()
	if v.Chats["1"].Session != "session-1" || c.Workspace == "" {
		t.Fatal("lost binding")
	}
	if err := os.MkdirAll(cfg.Workspace, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(cfg.Workspace, "telegram_9_2")
	if err := os.Symlink(t.TempDir(), target); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace(context.Background(), home, cfg, s, v.Chats["2"], run); err == nil {
		t.Fatal("accepted escaping symlink")
	}
}
