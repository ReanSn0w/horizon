package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func pluginTemplate(t *testing.T, name, value string, legacy ...string) PluginTemplate {
	t.Helper()
	var section yaml.Node
	if err := yaml.Unmarshal([]byte(value), &section); err != nil {
		t.Fatal(err)
	}
	return PluginTemplate{Name: name, Section: section.Content[0], LegacyNames: legacy}
}

func TestMergePluginTemplatesPreservesValuesAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "config.yaml")
	initial := "# personal\nmode: unit\nplugins:\n  telegram:\n    # keep token\n    telegram:\n      bot_token: personal-token\n    group_access: full\n"
	if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	template := pluginTemplate(t, "telegram", "telegram:\n  bot_token: ''\n  owner_user_id: 0\ngroup_access: read\n")
	status, err := MergePluginTemplates(home, []PluginTemplate{template})
	if err != nil || status["telegram"] != "updated" {
		t.Fatalf("status=%v err=%v", status, err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"# personal", "# keep token", "personal-token", "group_access: full", "owner_user_id: 0"} {
		if !strings.Contains(string(first), value) {
			t.Fatalf("missing %q in %s", value, first)
		}
	}
	status, err = MergePluginTemplates(home, []PluginTemplate{template})
	second, _ := os.ReadFile(path)
	if err != nil || status["telegram"] != "existing" || string(first) != string(second) {
		t.Fatalf("repeat status=%v err=%v", status, err)
	}
}

func TestMergePluginTemplatesConflictDoesNotWrite(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "config.yaml")
	initial := "mode: unit\nplugins:\n  gateway: {}\n  telegram: {}\n"
	if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := MergePluginTemplates(home, []PluginTemplate{pluginTemplate(t, "telegram", "telegram: {}", "gateway")})
	data, _ := os.ReadFile(path)
	if err == nil || string(data) != initial {
		t.Fatalf("conflict err=%v data=%s", err, data)
	}
}
