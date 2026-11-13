package manager

import (
	"context"
	"errors"
	"github.com/ReanSn0w/horizon/internal/config"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSupervisorConfigMigration(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "config.yaml")
	original := "# preserve\nmode: unit\nplugins:\n  daemon: # supervisor\n    adapters: [timer]\n"
	os.WriteFile(path, []byte(original), 0600)
	if err := prepareSupervisorConfig(home, true); err != nil {
		t.Fatal(err)
	}
	if _, err := config.MergeCoreTemplate(home, []byte("plugins: {}\n")); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "mode:") || strings.Contains(string(data), "daemon:") || !strings.Contains(string(data), "# supervisor") || !strings.Contains(string(data), "agent:") {
		t.Fatal(string(data))
	}
	os.WriteFile(path, []byte("plugins:\n  agent: {}\n  daemon: {}\n"), 0600)
	before, _ := os.ReadFile(path)
	if err := prepareSupervisorConfig(home, true); err == nil {
		t.Fatal("accepted conflict")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("changed conflicted config")
	}
}
func TestUpdateRenamesManagedSupervisor(t *testing.T) {
	e, home := installFixture(t)
	// Model an existing managed daemon installation without accessing a real home.
	state, err := ReadState(home)
	if err != nil {
		t.Fatal(err)
	}
	state.Plugins = append(state.Plugins, "daemon")
	if err = writeJSON(filepath.Join(home, ".manager", "state.json"), state); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(home, "bin", "horizon-daemon")
	os.WriteFile(old, []byte("old binary"), 0700)
	configPath := filepath.Join(home, "config.yaml")
	data, _ := os.ReadFile(configPath)
	os.WriteFile(configPath, []byte(strings.Replace(string(data), "plugins:\n", "plugins:\n  daemon:\n    adapters: [timer]\n", 1)), 0600)
	dir := filepath.Join(e.repository, "plugins", "agent")
	os.MkdirAll(dir, 0700)
	metadata, _ := os.ReadFile("../../plugins/agent/plugin.json")
	os.WriteFile(filepath.Join(dir, "plugin.json"), metadata, 0600)
	commitFixture(t, e.repository)
	originalConfig, _ := os.ReadFile(configPath)
	rollbackReached := false
	e.afterReplace = func(int) error {
		if _, err := os.Stat(old); os.IsNotExist(err) {
			rollbackReached = true
			return errors.New("fail after removing legacy binary")
		}
		return nil
	}
	if err = e.execute(context.Background(), Options{Command: "update", Home: home}, nil, io.Discard); err == nil || !rollbackReached {
		t.Fatal("expected rollback after rename", err)
	}
	restored, _ := os.ReadFile(old)
	restoredConfig, _ := os.ReadFile(configPath)
	if string(restored) != "old binary" || string(restoredConfig) != string(originalConfig) {
		t.Fatal("rollback did not restore legacy installation")
	}
	if _, err = os.Stat(filepath.Join(home, "bin", "horizon-agent")); !os.IsNotExist(err) {
		t.Fatal("rollback left new binary")
	}
	e.afterReplace = nil
	if err = e.execute(context.Background(), Options{Command: "update", Home: home}, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	state, err = ReadState(home)
	if err != nil || !slices.Contains(state.Plugins, "agent") || slices.Contains(state.Plugins, "daemon") {
		t.Fatal(state, err)
	}
	if _, err = os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old binary retained", err)
	}
	if _, err = os.Stat(filepath.Join(home, "bin", "horizon-agent")); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(configPath)
	if !strings.Contains(string(data), "agent:") || strings.Contains(string(data), "daemon:") {
		t.Fatal(string(data))
	}
}

func TestOlderSourceKeepsLegacyConfiguration(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "config.yaml")
	original := "plugins:\n  daemon:\n    adapters: [timer]\n"
	os.WriteFile(path, []byte(original), 0600)
	if _, err := config.MergeCoreTemplate(home, []byte("mode: unit\nplugins: {}\n")); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "mode: unit") || !strings.Contains(string(data), "daemon:") {
		t.Fatal(string(data))
	}
	if err := disableAdapter(home, "agent:timer"); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), "mode: unit") || !strings.Contains(string(data), "daemon:") || strings.Contains(string(data), "timer") {
		t.Fatal(string(data))
	}
}
