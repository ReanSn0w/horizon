package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestMergePluginTemplatesMigratesLegacyAndPreservesSymlink(t *testing.T) {
	realHome, aliasHome := t.TempDir(), t.TempDir()
	path := filepath.Join(realHome, "config.yaml")
	initial := "# keep\nmode: unit\nplugins:\n  # legacy comment\n  gateway:\n    telegram:\n      bot_token: private-token\n"
	if err := os.WriteFile(path, []byte(initial), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, filepath.Join(aliasHome, "config.yaml")); err != nil {
		t.Fatal(err)
	}
	status, err := MergePluginTemplates(aliasHome, []PluginTemplate{pluginTemplate(t, "telegram", "telegram:\n  bot_token: ''\n  owner_user_id: 0\n", "gateway")})
	if err != nil || status["telegram"] != "updated" {
		t.Fatalf("status=%v err=%v", status, err)
	}
	data, _ := os.ReadFile(path)
	for _, want := range []string{"# keep", "# legacy comment", "private-token", "owner_user_id: 0", "telegram:"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %q: %s", want, data)
		}
	}
	if strings.Contains(string(data), "gateway:") {
		t.Fatal("legacy section not migrated")
	}
	info, err := os.Lstat(filepath.Join(aliasHome, "config.yaml"))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced")
	}
	info, err = os.Stat(path)
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("mode changed: %v %v", info, err)
	}
}

func TestMergePluginTemplatesRejectsInvalidSectionWithoutPartialWrite(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "config.yaml")
	initial := "mode: unit\nplugins:\n  beta: null\n"
	if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	templates := []PluginTemplate{pluginTemplate(t, "alpha", "value: 1"), pluginTemplate(t, "beta", "value: 2")}
	if _, err := MergePluginTemplates(home, templates); err == nil || !strings.Contains(err.Error(), "plugins.beta") {
		t.Fatalf("invalid section accepted: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != initial {
		t.Fatal("partial update")
	}
}

func TestMergePluginTemplatesConcurrentWithSkills(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(path, []byte("mode: unit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	template := pluginTemplate(t, "alpha", "value: 1")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := MergePluginTemplates(home, []PluginTemplate{template}); err != nil {
				t.Error(err)
			}
			if _, err := UpdateDisabledSkills(home, "sample", true, nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "alpha:") || !strings.Contains(string(data), "sample") {
		t.Fatalf("lost concurrent update: %s", data)
	}
}
