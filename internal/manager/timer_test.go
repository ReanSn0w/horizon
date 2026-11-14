package manager

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/daemon"
)

func TestInstallTimerAdapterAndPreserveUserSkill(t *testing.T) {
	e := fixtureEngine(t)
	ctx := context.Background()
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	catalogPath := filepath.Join("plugins", "agent-timer", "plugin.json")
	catalog, err := os.ReadFile(filepath.Join(repo, catalogPath))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(e.repository, filepath.Dir(catalogPath)), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(e.repository, catalogPath), catalog, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", catalogPath}, {"commit", "-qm", "timer catalog"}} {
		if _, err := command(ctx, e.repository, "git", args...); err != nil {
			t.Fatal(err)
		}
	}
	defaultBuild := e.build
	e.build = func(ctx context.Context, dir, pkg, destination string) error {
		if pkg == "./plugins/agent-timer" {
			_, err := command(ctx, repo, "go", "build", "-o", destination, pkg)
			return err
		}
		return defaultBuild(ctx, dir, pkg, destination)
	}

	home := t.TempDir()
	install := Options{Command: "install", Home: home, Yes: true, Source: &config.Source{Type: "branch", Branch: "feature/install"}}
	if err := e.execute(ctx, install, strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(home, "skills", "timers", "SKILL.md")
	bundled, err := os.ReadFile(skill)
	if err != nil || !strings.Contains(string(bundled), "horizon agent timer validate") {
		t.Fatalf("timer skill not installed: %v", err)
	}
	const custom = "---\nname: timers\ndescription: User-specific instructions\n---\nCustom."
	if err := os.Remove(skill); err != nil {
		t.Fatal(err)
	}
	// Exercise template installation into an existing home whose configuration
	// predates the timer section.
	configFile := filepath.Join(home, "config.yaml")
	data, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	oldConfig := strings.Replace(string(data), "plugins:\n  agent-timer:\n    access: write\n    reload_interval: 30s\n", "", 1)
	if err := os.WriteFile(configFile, []byte(oldConfig), 0600); err != nil {
		t.Fatal(err)
	}
	timerInstall := Options{Command: "plugins install", Home: home, Yes: true, Name: "agent:timer"}
	if err := e.execute(ctx, timerInstall, strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(skill)
	if err != nil || string(restored) != string(bundled) {
		t.Fatalf("timer plugin installation did not restore missing bundled skill: %v", err)
	}
	if err := os.WriteFile(skill, []byte(custom), 0600); err != nil {
		t.Fatal(err)
	}
	if err := e.execute(ctx, timerInstall, strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}
	meta, err := daemon.AdapterInspect(ctx, home, "timer")
	if err != nil || !slices.Contains(meta.Capabilities, daemon.AdapterAdminCLI) {
		t.Fatalf("timer metadata: %+v %v", meta, err)
	}
	state, err := ReadState(home)
	if err != nil || !slices.Contains(state.Plugins, "agent:timer") {
		t.Fatalf("managed state: %+v %v", state, err)
	}
	enabled, err := enabledPlugins(home)
	if err != nil || !slices.Contains(enabled, "agent:timer") {
		t.Fatalf("installation did not enable timer: %v %v", enabled, err)
	}
	data, err = os.ReadFile(configFile)
	if err != nil || !strings.Contains(string(data), "agent-timer:") || !strings.Contains(string(data), "reload_interval: 30s") {
		t.Fatalf("timer config template not installed: %v", err)
	}
	if err := e.execute(ctx, install, strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(skill)
	if err != nil || string(data) != custom {
		t.Fatalf("manager replaced user skill: %q %v", data, err)
	}
}
