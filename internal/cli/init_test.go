package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitIsExplicitIdempotentAndUsesSelectedHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "custom home")
	t.Setenv("HORIZON_HOME", home)
	code, out, diagnostics := runApp(t, "", "init")
	if code != ExitOK || diagnostics != "" {
		t.Fatalf("init: code=%d out=%q err=%q", code, out, diagnostics)
	}
	for _, path := range []string{filepath.Join(home, "config.yaml"), filepath.Join(home, "AGENTS.md")} {
		if !strings.Contains(out, path) {
			t.Fatalf("init omitted %q: %s", path, out)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("%q: %v %v", path, info, err)
		}
	}
	if !strings.Contains(out, "provider.url") || !strings.Contains(out, "provider.key") || !strings.Contains(out, "models.chatting.model") {
		t.Fatalf("missing setup fields: %s", out)
	}
	configPath := filepath.Join(home, "config.yaml")
	agentsPath := filepath.Join(home, "AGENTS.md")
	for path, content := range map[string]string{configPath: "personal config", agentsPath: "personal instructions"} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	code, out, diagnostics = runApp(t, "", "init")
	if code != ExitOK || diagnostics != "" || !strings.Contains(out, "Существующая конфигурация сохранена") {
		t.Fatalf("repeat init: code=%d out=%q err=%q", code, out, diagnostics)
	}
	for path, want := range map[string]string{configPath: "personal config", agentsPath: "personal instructions"} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("%s: %q %v", path, data, err)
		}
	}
}

func TestInitAddsBundledSkillWithoutChangingExistingHome(t *testing.T) {
	home := t.TempDir()
	files := map[string]string{
		"config.yaml":                    "mode: unit\ndefault_model: personal\nmodels:\n  personal:\n    model: chosen-model\n    compact_threshold: 12345\nprovider:\n  url: https://example.test/v1\n  key: personal-key\ndisabled_skills:\n  - personal-skill\n",
		"AGENTS.md":                      "Custom agent instructions\n",
		"skills/personal-skill/SKILL.md": "---\nname: personal-skill\ndescription: Personal skill\n---\nCustom instructions\n",
	}
	for name, content := range files {
		path := filepath.Join(home, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	code, _, diagnostics := runApp(t, "", "--home", home, "init")
	if code != ExitOK || diagnostics != "" {
		t.Fatalf("init: code=%d err=%q", code, diagnostics)
	}
	for name, want := range files {
		data, err := os.ReadFile(filepath.Join(home, name))
		if err != nil || string(data) != want {
			t.Fatalf("init changed %s: %q %v", name, data, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "skills", "skill-creator", "SKILL.md")); err != nil {
		t.Fatalf("init did not add bundled skill: %v", err)
	}
}

func TestOnlyInitCreatesUninitializedHome(t *testing.T) {
	for _, args := range [][]string{{"models"}, {"sessions", "list"}, {"skills", "list"}, {"resume", "-m", "hello"}} {
		home := filepath.Join(t.TempDir(), "uninitialized")
		code, out, diagnostics := runApp(t, "", append([]string{"--home", home}, args...)...)
		if code != ExitUsage || out != "" || !strings.Contains(diagnostics, "horizon init") {
			t.Fatalf("%v: %d %q %q", args, code, out, diagnostics)
		}
		if _, err := os.Stat(home); !os.IsNotExist(err) {
			t.Fatalf("%v created home: %v", args, err)
		}
	}
}
