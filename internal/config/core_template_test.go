package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const coreDefaults = `
soul_enabled: false
default_model: chatting
decision_model: decision
models:
  chatting:
    provider: default
    model: sample
    compact_threshold: 1000
providers:
  default:
    url: https://example.test/v1
    key: ''
memory:
  enabled: true
  model: chatting
  threshold: 5
  interval: 24h
limits:
  max_model_requests: 128
  max_input_tokens: 2000000
  max_turn_duration: 120m
disabled_skills: []
agent_plugins: []
agent_communication: []
plugins:
  browser:
    api_key: ''
`

func TestCoreTemplatePreservesUserChoicesAndIsIdempotent(t *testing.T) {
	initial := strings.Replace(validConfig, "mode: unit", "# retired mode comment\nmode: unit # keep this note", 1) + "\n# custom settings\nsoul_enabled: true\nlimits:\n  max_model_requests: 0 # no request cap\nagent_plugins: [browser]\nplugins:\n  browser:\n    custom: keep\n"
	home := writeConfig(t, initial)
	path := filepath.Join(home, "config.yaml")
	target := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.Rename(path, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0640); err != nil {
		t.Fatal(err)
	}
	changed, err := MergeCoreTemplate(home, []byte(coreDefaults))
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	cfg, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel != "coding" || cfg.Memory.Enabled || cfg.Memory.Model != "coding" || !cfg.SoulEnabled || cfg.Limits.MaxModelRequests != 0 {
		t.Fatal("user behavior changed")
	}
	if _, ok := cfg.Models["chatting"]; ok {
		t.Fatal("sample model added")
	}
	data, _ := os.ReadFile(target)
	for _, text := range []string{"# retired mode comment", "# keep this note", "# custom settings", "# no request cap", "custom: keep", "max_input_tokens: 2000000"} {
		if !strings.Contains(string(data), text) {
			t.Fatalf("missing retained or added setting: %s", text)
		}
	}
	if strings.Contains(string(data), "api_key") || strings.Contains(string(data), "mode: unit") {
		t.Fatal("plugin defaults added or obsolete mode retained")
	}
	info, _ := os.Lstat(path)
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced")
	}
	info, _ = os.Stat(target)
	if info.Mode().Perm() != 0640 {
		t.Fatal("permissions changed")
	}
	at := time.Unix(123456789, 0)
	os.Chtimes(target, at, at)
	changed, err = MergeCoreTemplate(home, []byte(coreDefaults))
	if err != nil || changed {
		t.Fatal("second merge changed config", changed, err)
	}
	again, _ := os.ReadFile(target)
	info, _ = os.Stat(target)
	if string(again) != string(data) || !info.ModTime().Equal(at) {
		t.Fatal("idempotent merge rewrote config")
	}
}

func TestCoreTemplatePreservesExplicitEmptyNullAndDisabledValues(t *testing.T) {
	home := writeConfig(t, "models: {}\nproviders: {}\nlimits: null\nsoul_enabled: false\nagent_plugins: []\nmemory:\n  enabled: false\n  model: ''\n")
	changed, err := MergeCoreTemplate(home, []byte(coreDefaults))
	if err != nil || !changed {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
	for _, text := range []string{"models: {}", "providers: {}", "limits: null", "soul_enabled: false", "enabled: false", "model: ''"} {
		if !strings.Contains(string(data), text) {
			t.Fatal("explicit value overwritten", text)
		}
	}
	if strings.Contains(string(data), "default_model:") || strings.Contains(string(data), "decision_model:") {
		t.Fatal("selected a nonexistent sample profile")
	}
}

func TestCoreTemplateRejectsUnknownSchemaAndUnsafeMigrationWithoutWriting(t *testing.T) {
	for _, tc := range []struct{ initial, template string }{
		{"mode: server\n", coreDefaults},
		{"mystery: keep\n", coreDefaults},
		{"soul_enabled: true\n", "future_field: true\n"},
		{"soul_enabled: true\n", "soul_enabled: false\n---\nsoul_enabled: true\n"},
	} {
		home := writeConfig(t, tc.initial)
		if changed, err := MergeCoreTemplate(home, []byte(tc.template)); err == nil || changed {
			t.Fatal("accepted invalid update", err)
		}
		data, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
		if string(data) != tc.initial {
			t.Fatal("failed update changed config")
		}
	}
}
