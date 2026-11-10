package manager

import (
	"context"
	"fmt"
	"github.com/ReanSn0w/horizon/internal/daemon"
	"github.com/ReanSn0w/horizon/internal/maintenance"
	"os"
	"path/filepath"
	"testing"
)

func TestTypedAdapterCatalog(t *testing.T) {
	root := t.TempDir()
	for _, v := range []struct{ dir, json string }{{"manager", `{"name":"manager","description":"manager","package":"./plugins/manager","binary":"horizon-manager"}`}, {"telegram", `{"name":"telegram","kind":"adapter","description":"tg","package":"./plugins/telegram","binary":"agent-telegram"}`}, {"legacy", `{"name":"telegram","description":"legacy","package":"./plugins/legacy","binary":"horizon-telegram"}`}} {
		p := filepath.Join(root, "plugins", v.dir)
		os.MkdirAll(p, 0700)
		os.WriteFile(filepath.Join(p, "plugin.json"), []byte(v.json), 0600)
	}
	catalog, e := Catalog(root)
	if e != nil {
		t.Fatal(e)
	}
	if len(catalog) != 3 || catalog["agent:telegram"].Binary != "agent-telegram" {
		t.Fatal(catalog)
	}
	if managedBinary("agent:telegram") == managedBinary("telegram") {
		t.Fatal("collision")
	}
	if !validReplacement("bin/agent-telegram") || validReplacement("bin/agent-../bad") {
		t.Fatal("invalid replacement validation")
	}
	if _, e := Parse([]string{"plugins", "install", "agent:telegram"}); e != nil {
		t.Fatal(e)
	}
}
func TestAdapterStagingMetadata(t *testing.T) {
	h := t.TempDir()
	os.MkdirAll(filepath.Join(h, "bin"), 0700)
	os.WriteFile(filepath.Join(h, "bin", "agent-test"), []byte("#!/bin/sh\necho '{\"protocol_version\":1,\"version\":\"test\",\"description\":\"test\",\"capabilities\":[]}'\n"), 0700)
	if e := inspectManaged(h, "agent:test"); e != nil {
		t.Fatal(e)
	}
	if _, e := daemon.AdapterInspect(context.Background(), h, "missing"); e == nil {
		t.Fatal("accepted missing adapter")
	}
}
func TestDaemonMaintenanceFailsPromptly(t *testing.T) {
	h := t.TempDir()
	os.MkdirAll(filepath.Join(h, ".manager"), 0700)
	release, e := maintenance.Acquire(h, false)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	if _, e := maintenance.Acquire(h, true); e == nil {
		t.Fatal("maintenance acquired while daemon active")
	}
}

func TestAdapterRollback(t *testing.T) {
	home := t.TempDir()
	stage := t.TempDir()
	for _, root := range []string{home, stage} {
		os.MkdirAll(filepath.Join(root, "bin"), 0700)
		os.MkdirAll(filepath.Join(root, ".manager"), 0700)
		os.WriteFile(filepath.Join(root, "config.yaml"), []byte("plugins: {}\n"), 0600)
	}
	os.WriteFile(filepath.Join(home, "bin", "agent-test"), []byte("old"), 0700)
	os.WriteFile(filepath.Join(stage, "bin", "agent-test"), []byte("new"), 0700)
	e := &Engine{afterReplace: func(int) error { return fmt.Errorf("injected failure") }}
	if err := e.deploy(home, stage, []string{"bin/agent-test"}, nil); err == nil {
		t.Fatal("expected failure")
	}
	b, err := os.ReadFile(filepath.Join(home, "bin", "agent-test"))
	if err != nil || string(b) != "old" {
		t.Fatal("adapter not restored", err)
	}
}
