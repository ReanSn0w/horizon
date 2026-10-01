package main

import (
	"context"
	"fmt"
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

func TestEqualChatNamesHaveSeparateBindings(t *testing.T) {
	home := configHome(t)
	cfg := defaults(home)
	s := newStore(home, 99)
	if err := s.update(func(v *state) error {
		for _, id := range []int64{-1, -2} {
			v.Chats[fmt.Sprint(id)] = &chat{ID: id, Origin: id, Type: "group", Title: "Same name"}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	run := func(_ context.Context, dir string, _ []string, _ string) (string, error) {
		return "session-" + filepath.Base(dir), nil
	}
	value, _ := s.snapshot()
	one, err := workspace(context.Background(), home, cfg, s, value.Chats["-1"], run)
	if err != nil {
		t.Fatal(err)
	}
	two, err := workspace(context.Background(), home, cfg, s, value.Chats["-2"], run)
	if err != nil {
		t.Fatal(err)
	}
	if one.Workspace == two.Workspace || one.Session == two.Session {
		t.Fatal("same titles mixed chat bindings")
	}
}

func TestSavedSymlinkParentDoesNotCreateOutsideDirectory(t *testing.T) {
	home := configHome(t)
	cfg := defaults(home)
	outside := t.TempDir()
	if err := os.MkdirAll(cfg.Workspace, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(cfg.Workspace, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	c := &chat{ID: 1, Origin: 1, Workspace: filepath.Join(link, "nested")}
	if _, err := workspace(context.Background(), home, cfg, newStore(home, 99), c, nil); err == nil {
		t.Fatal("accepted escaping saved workspace")
	}
	if _, err := os.Stat(filepath.Join(outside, "nested")); !os.IsNotExist(err) {
		t.Fatal("created directory outside workspace_dir")
	}
}
