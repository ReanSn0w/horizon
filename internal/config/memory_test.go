package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMemoryConfiguration(t *testing.T) {
	tests := []struct {
		name, extra, wantError string
		enabled                bool
	}{
		{name: "absent"},
		{name: "built in", extra: "memory:\n  enabled: true\n  threshold: 3\n", enabled: true},
		{name: "disabled", extra: "memory:\n  enabled: false\n"},
		{name: "legacy", extra: "agent_plugins: [memory]\nplugins:\n  memory:\n    threshold: 2\n", enabled: true},
		{name: "legacy settings without enable", extra: "plugins:\n  memory:\n    threshold: 2\n"},
		{name: "conflict", extra: "memory: {}\nagent_plugins: [memory]\n", wantError: "conflicts with legacy"},
		{name: "unknown field", extra: "memory:\n  impossible: true\n", wantError: "invalid memory settings"},
		{name: "null", extra: "memory: null\n", wantError: "memory must be a mapping"},
		{name: "null enabled", extra: "memory:\n  enabled: null\n", wantError: "invalid memory settings"},
		{name: "sequence", extra: "memory: []\n", wantError: "invalid memory settings"},
		{name: "unknown model", extra: "memory:\n  model: absent\n", wantError: "does not name a configured profile"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, validConfig+test.extra))
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("error = %v, want %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Memory.Enabled != test.enabled {
				t.Fatalf("enabled = %t", cfg.Memory.Enabled)
			}
			if test.extra != "" && cfg.Memory.Threshold != 2 && test.name != "built in" && test.name != "disabled" {
				t.Fatalf("threshold = %d", cfg.Memory.Threshold)
			}
			if test.name == "built in" && (cfg.Memory.Threshold != 3 || cfg.Memory.Interval != 24*time.Hour || cfg.Memory.Model != "coding") {
				t.Fatalf("memory = %+v", cfg.Memory)
			}
		})
	}
}

func TestMigrateMemoryPreservesConfigSymlinkAndComments(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(t.TempDir(), "real.yaml")
	path := filepath.Join(home, "config.yaml")
	original := validConfig + `# keep memory comment
agent_plugins: [memory, browser]
plugins:
  # keep section comment
  memory: # keep inline section comment
    threshold: 7 # keep threshold comment
    interval: 2h
  browser:
    api_key: retained
`
	if err := os.WriteFile(target, []byte(original), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	changed, err := MigrateMemory(home)
	if err != nil || !changed {
		t.Fatalf("changed=%t error=%v", changed, err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# keep memory comment", "# keep section comment", "# keep inline section comment", "# keep threshold comment", "memory:", "enabled: true", "threshold: 7", "browser:", "api_key: retained"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %q:\n%s", want, data)
		}
	}
	if strings.Contains(string(data), "agent_plugins: [memory") || strings.Count(string(data), "memory:") != 1 {
		t.Fatalf("legacy memory remains:\n%s", data)
	}
	info, _ := os.Lstat(path)
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("configuration symlink replaced")
	}
	info, _ = os.Stat(target)
	if info.Mode().Perm() != 0640 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	cfg, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Memory.Enabled || cfg.Memory.Threshold != 7 || cfg.Memory.Interval != 2*time.Hour || !reflect.DeepEqual(cfg.AgentPlugins, []string{"browser"}) {
		t.Fatalf("config = %+v", cfg)
	}
	changed, err = MigrateMemory(home)
	again, _ := os.ReadFile(target)
	if err != nil || changed || string(again) != string(data) {
		t.Fatalf("non-idempotent migration: changed=%t error=%v", changed, err)
	}
}

func TestMigrateMemoryConflictDoesNotWrite(t *testing.T) {
	home := writeConfig(t, validConfig+"memory: {}\nagent_plugins: [memory]\n")
	path := filepath.Join(home, "config.yaml")
	before, _ := os.ReadFile(path)
	if changed, err := MigrateMemory(home); err == nil || changed {
		t.Fatalf("changed=%t error=%v", changed, err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("conflict changed configuration")
	}
}

func TestMigrateMemorySettingsOnlyStaysDisabled(t *testing.T) {
	home := writeConfig(t, validConfig+"plugins:\n  memory:\n    threshold: 9\n")
	changed, err := MigrateMemory(home)
	if err != nil || !changed {
		t.Fatalf("changed=%t error=%v", changed, err)
	}
	cfg, err := Load(home)
	if err != nil || cfg.Memory.Enabled || cfg.Memory.Threshold != 9 {
		t.Fatalf("memory=%+v error=%v", cfg.Memory, err)
	}
	data, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
	if !strings.Contains(string(data), "enabled: false") || strings.Contains(string(data), "plugins:\n  memory:") {
		t.Fatalf("unexpected migration:\n%s", data)
	}
}
