package manager

import (
	"context"
	"github.com/ReanSn0w/horizon/internal/config"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureEngine(t *testing.T) *Engine {
	t.Helper()
	e := engine()
	e.repository = fixtureRepo(t)
	e.build = func(_ context.Context, _, pkg, path string) error {
		return os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' '{\"protocol_version\":1,\"version\":\"test\",\"description\":\"test plugin\"}'\n"), 0700)
	}
	return e
}
func TestInstallPreservesUserResources(t *testing.T) {
	e := fixtureEngine(t)
	home := filepath.Join(t.TempDir(), "home with spaces")
	o := Options{Command: "install", Home: home, Yes: true, NoShell: true, Source: &config.Source{Type: "branch", Branch: "feature/install"}}
	if err := e.execute(context.Background(), o, strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"bin/horizon", "bin/horizon-manager", "source/go.mod", "dialogs", "memory/workspaces", "skills/filesystem/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(home, path)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "gateway")); !os.IsNotExist(err) {
		t.Fatal("unexpected plugin state")
	}
	custom := filepath.Join(home, "AGENTS.md")
	os.WriteFile(custom, []byte("mine"), 0600)
	os.Remove(filepath.Join(home, "skills/filesystem/SKILL.md"))
	if err := e.execute(context.Background(), o, strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(custom)
	if string(data) != "mine" {
		t.Fatal("user file replaced")
	}
	if _, err := ReadState(home); err != nil {
		t.Fatal(err)
	}
}
func TestInstallBuildFailureLeavesBinaries(t *testing.T) {
	e := fixtureEngine(t)
	home := t.TempDir()
	e.build = func(context.Context, string, string, string) error { return io.ErrUnexpectedEOF }
	err := e.execute(context.Background(), Options{Command: "install", Home: home, Yes: true, NoShell: true, Source: &config.Source{Type: "branch", Branch: "feature/install"}}, strings.NewReader(""), io.Discard)
	if err == nil {
		t.Fatal("expected failure")
	}
	if _, err := os.Stat(filepath.Join(home, "bin/horizon")); !os.IsNotExist(err) {
		t.Fatal("published failed build")
	}
}
