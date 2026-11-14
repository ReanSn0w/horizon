package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestExtensionsControlToolsAndGrants(t *testing.T) {
	home := writeConfig(t, validConfig+`extensions:
  telegram: {enabled: true, communication: true}
  browser: {enabled: false, communication: true}
  subagent: {enabled: true}
`)
	cfg, err := Load(home)
	if err != nil || !reflect.DeepEqual(cfg.AgentPlugins, []string{"subagent", "telegram"}) || !reflect.DeepEqual(cfg.AgentCommunication, []string{"telegram"}) {
		t.Fatalf("%+v %v", cfg.AgentPlugins, err)
	}
	if !cfg.Extensions["browser"].Communication {
		t.Fatal("disabled grant lost")
	}
	for _, extra := range []string{
		"extensions: {browser: null}\n", "extensions: {browser: {enabled: yes}}\n", "extensions: {browser: {unknown: true}}\n",
		"extensions: {../escape: {enabled: true}}\n", "extensions: {memory: {enabled: true}}\n",
		"extensions: {browser: {enabled: true}}\nagent_plugins: [telegram]\n",
		"extensions: {browser: {enabled: true, enabled: false}}\n",
	} {
		if _, err := Load(writeConfig(t, validConfig+extra)); err == nil {
			t.Fatal("accepted", extra)
		}
	}
}

func TestExtensionMigrationPreservesSettingsAndSymlink(t *testing.T) {
	home := writeConfig(t, validConfig+"# personal extensions\nagent_plugins: [telegram, browser] # tools\nagent_communication: [telegram] # send grant\n")
	path := filepath.Join(home, "config.yaml")
	target := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.Rename(path, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	changed, err := MigrateExtensions(home)
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	cfg, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.AgentPlugins, []string{"browser", "telegram"}) || !reflect.DeepEqual(cfg.AgentCommunication, []string{"telegram"}) {
		t.Fatal(cfg.Extensions)
	}
	info, _ := os.Lstat(path)
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced")
	}
	info, _ = os.Stat(target)
	if info.Mode().Perm() != 0640 {
		t.Fatal("permissions lost")
	}
	before, _ := os.ReadFile(path)
	for _, comment := range []string{"personal extensions", "tools", "send grant"} {
		if !bytes.Contains(before, []byte(comment)) {
			t.Fatal("comment lost", comment)
		}
	}
	if strings.Contains(string(before), "agent_plugins:") || strings.Contains(string(before), "agent_communication:") {
		t.Fatal("legacy keys retained")
	}
	changed, err = MigrateExtensions(home)
	if err != nil || changed {
		t.Fatal(changed, err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("noop rewrote file")
	}
}

func TestEnsureExtensionPreservesDisabledChoice(t *testing.T) {
	home := writeConfig(t, validConfig+"extensions: {}\n")
	if err := EnsureExtension(home, "telegram", ExtensionSettings{Enabled: true, Communication: true}); err != nil {
		t.Fatal(err)
	}
	if err := DisableExtension(home, "telegram"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
	if err := EnsureExtension(home, "telegram", ExtensionSettings{Enabled: true, Communication: true}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
	if !bytes.Equal(before, after) {
		t.Fatal("re-enabled")
	}
	cfg, err := Load(home)
	if err != nil || len(cfg.AgentPlugins) != 0 || !cfg.Extensions["telegram"].Communication {
		t.Fatal(cfg.Extensions, err)
	}
}

func TestExtensionMigrationRejectsConflictAtomically(t *testing.T) {
	home := writeConfig(t, validConfig+"agent_plugins: [telegram]\nextensions: {browser: {enabled: false}}\n")
	path := filepath.Join(home, "config.yaml")
	before, _ := os.ReadFile(path)
	if _, err := MigrateExtensions(home); err == nil {
		t.Fatal("accepted conflict")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("modified conflict")
	}
}

func TestExtensionMigrationRetainsLegacyMemory(t *testing.T) {
	home := writeConfig(t, validConfig+"agent_plugins: [memory, telegram]\nagent_communication: [telegram]\nplugins:\n  memory:\n    threshold: 7\n")
	if _, err := MigrateExtensions(home); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(home)
	if err != nil || !cfg.Memory.Enabled || cfg.Memory.Threshold != 7 || !reflect.DeepEqual(cfg.AgentPlugins, []string{"telegram"}) {
		t.Fatal(cfg.Memory, err)
	}
}
