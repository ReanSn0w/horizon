package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ReanSn0w/horizon/internal/config"
	"gopkg.in/yaml.v3"
)

func activationFixture(t *testing.T) (*Engine, string) {
	t.Helper()
	e, home := installFixture(t)
	for _, name := range []string{"agent", "agent-timer"} {
		dir := filepath.Join(e.repository, "plugins", name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile("../../plugins/" + name + "/plugin.json")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "plugin.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	commitFixture(t, e.repository)
	e.build = func(_ context.Context, _, pkg, path string) error {
		metadata := map[string]any{"protocol_version": 1, "version": "test", "description": "fixture"}
		switch pkg {
		case "./plugins/telegram":
			metadata["agent_protocol_version"] = 2
			metadata["default_communication"] = true
		case "./plugins/browser":
			metadata["agent_protocol_version"] = 1
		case "./plugins/agent-timer":
			metadata["capabilities"] = []string{}
		}
		data, _ := json.Marshal(metadata)
		return os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' '"+string(data)+"'\n"), 0700)
	}
	if err := runOperation(e, home, "update", ""); err != nil {
		t.Fatal(err)
	}
	return e, home
}

func readExtensions(t *testing.T, home string) map[string]config.ExtensionSettings {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Extensions map[string]config.ExtensionSettings `yaml:"extensions"`
	}
	if err = yaml.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	return raw.Extensions
}

func TestInstallEnablesExtensionsAndPreservesUserChoices(t *testing.T) {
	e, home := activationFixture(t)
	var out bytes.Buffer
	for _, name := range []string{"telegram", "browser", "agent:timer"} {
		if err := e.execute(context.Background(), Options{Command: "plugins install", Home: home, Name: name}, nil, &out); err != nil {
			t.Fatal(err)
		}
	}
	settings := readExtensions(t, home)
	if settings["telegram"] != (config.ExtensionSettings{Enabled: true, Communication: true}) || settings["browser"] != (config.ExtensionSettings{Enabled: true}) {
		t.Fatal(settings)
	}
	if !strings.Contains(out.String(), "extensions.telegram: enabled=true, communication=true") {
		t.Fatal(out.String())
	}
	enabled, err := enabledPlugins(home)
	if err != nil || !slices.Contains(enabled, "agent:timer") {
		t.Fatal(enabled, err)
	}
	if err = config.DisableExtension(home, "telegram"); err != nil {
		t.Fatal(err)
	}
	if err = disableAdapter(home, "agent:timer"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"telegram", "agent:timer"} {
		if err = runOperation(e, home, "plugins install", name); err != nil {
			t.Fatal(err)
		}
	}
	if err = runOperation(e, home, "update", ""); err != nil {
		t.Fatal(err)
	}
	enabled, err = enabledPlugins(home)
	if err != nil || slices.Contains(enabled, "telegram") || slices.Contains(enabled, "agent:timer") {
		t.Fatal(enabled, err)
	}
	for _, command := range []string{"plugins remove", "plugins install"} {
		if err = runOperation(e, home, command, "agent:timer"); err != nil {
			t.Fatal(err)
		}
	}
	enabled, err = enabledPlugins(home)
	if err != nil || slices.Contains(enabled, "agent:timer") {
		t.Fatal("reinstalled input re-enabled", enabled, err)
	}
	if !readExtensions(t, home)["telegram"].Communication {
		t.Fatal("grant lost while disabled")
	}
	if err = runOperation(e, home, "plugins remove", "browser"); err != nil {
		t.Fatal(err)
	}
	if readExtensions(t, home)["browser"].Enabled {
		t.Fatal("removed extension enabled")
	}
	if err = runOperation(e, home, "plugins install", "browser"); err != nil {
		t.Fatal(err)
	}
	if readExtensions(t, home)["browser"].Enabled {
		t.Fatal("reinstall replaced disabled record")
	}
}

func TestActivationRollsBackWithInstallation(t *testing.T) {
	e, home := activationFixture(t)
	before, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
	reached := false
	e.afterReplace = func(int) error {
		if readExtensions(t, home)["telegram"].Enabled {
			reached = true
			return errors.New("failure after activation")
		}
		return nil
	}
	if err := runOperation(e, home, "plugins install", "telegram"); err == nil || !reached {
		t.Fatal("rollback not exercised", err)
	}
	after, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
	if !bytes.Equal(before, after) {
		t.Fatal("config not rolled back")
	}
	if _, err := os.Stat(filepath.Join(home, "bin", "horizon-telegram")); !os.IsNotExist(err) {
		t.Fatal("binary retained", err)
	}
	state, err := ReadState(home)
	if err != nil || slices.Contains(state.Plugins, "telegram") {
		t.Fatal(state, err)
	}
}

func TestUpdateMigratesLegacyWithoutEnablingOmittedExtension(t *testing.T) {
	e, home := activationFixture(t)
	for _, name := range []string{"telegram", "browser"} {
		if err := runOperation(e, home, "plugins install", name); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(home, "config.yaml")
	data, _ := os.ReadFile(path)
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	root := doc.Content[0]
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == "extensions" {
			root.Content = append(root.Content[:i], root.Content[i+2:]...)
			break
		}
	}
	data, _ = yaml.Marshal(&doc)
	data = append(data, []byte("agent_plugins: [telegram]\nagent_communication: []\n")...)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := runOperation(e, home, "update", ""); err != nil {
		t.Fatal(err)
	}
	settings := readExtensions(t, home)
	if settings["telegram"] != (config.ExtensionSettings{Enabled: true}) || settings["browser"].Enabled {
		t.Fatal(settings)
	}
	var out bytes.Buffer
	if err := e.execute(context.Background(), Options{Command: "plugins list", Home: home}, nil, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(strings.Fields(out.String()), " "), "telegram true true true") {
		t.Fatal(out.String())
	}
}
