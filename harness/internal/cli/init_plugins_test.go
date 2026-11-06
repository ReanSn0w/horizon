package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func configPluginScript(t *testing.T, home, name, template string) {
	t.Helper()
	dir := filepath.Join(home, "plugins")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \"$1\" in\nhorizon-plugin-metadata) printf '%s' '{\"protocol_version\":1,\"config_protocol_version\":1,\"version\":\"1\",\"description\":\"Test\"}';;\nhorizon-plugin-config-template) printf '%s' '" + template + "';;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "horizon-"+name), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestInitAddsInstalledPluginSettingsAndPreservesValues(t *testing.T) {
	home := t.TempDir()
	configPluginScript(t, home, "alpha", `{"config_protocol_version":1,"section":{"token":"","nested":{"count":2,"mode":"default"}}}`)
	pluginScript(t, home, "old", "exit 2\n")
	initial := "# personal\nmode: unit\nagent_plugins: []\nplugins:\n  alpha:\n    token: private-token\n    nested:\n      count: 9\n"
	path := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, diag := runApp(t, "", "--home", home, "init")
	if code != ExitOK || !strings.Contains(out, "Плагин alpha: updated") || !strings.Contains(diag, "old") {
		t.Fatalf("init %d %q %q", code, out, diag)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# personal", "private-token", "count: 9", "mode:", "default", "agent_plugins: []"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %q: %s", want, data)
		}
	}
	if strings.Contains(out+diag, "private-token") {
		t.Fatal("secret leaked to init output")
	}
	code, _, _ = runApp(t, "", "--home", home, "init")
	again, _ := os.ReadFile(path)
	if code != ExitOK || string(again) != string(data) {
		t.Fatal("repeat init rewrote config")
	}
	configPluginScript(t, home, "beta", `{"config_protocol_version":1,"section":{"enabled":false}}`)
	code, out, _ = runApp(t, "", "--home", home, "init")
	after, _ := os.ReadFile(path)
	if code != ExitOK || !strings.Contains(out, "Плагин beta: added") || !strings.Contains(string(after), "enabled:") {
		t.Fatalf("new plugin %d %q %s", code, out, after)
	}
}

func TestInitPluginConfigFailureDoesNotPartiallyWrite(t *testing.T) {
	home := t.TempDir()
	configPluginScript(t, home, "alpha", `{"config_protocol_version":1,"section":{"a":1}}`)
	configPluginScript(t, home, "beta", `{"config_protocol_version":1,"section":{"b":2}}`)
	path := filepath.Join(home, "config.yaml")
	initial := "mode: unit\nplugins:\n  beta: wrong-type\n"
	if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	code, _, diag := runApp(t, "", "--home", home, "init")
	data, _ := os.ReadFile(path)
	if code == ExitOK || !strings.Contains(diag, "plugins.beta") || string(data) != initial {
		t.Fatalf("partial write %d %q %s", code, diag, data)
	}
}

func TestInitSkipsBrokenTemplateWithoutPrintingOutput(t *testing.T) {
	home := t.TempDir()
	configPluginScript(t, home, "good", `{"config_protocol_version":1,"section":{"value":1}}`)
	configPluginScript(t, home, "bad", `SECRET_TEMPLATE_TEXT`)
	code, out, diag := runApp(t, "", "--home", home, "init")
	data, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
	if code != ExitOK || !strings.Contains(string(data), "value:") || !strings.Contains(diag, "bad") || strings.Contains(out+diag, "SECRET_TEMPLATE_TEXT") {
		t.Fatalf("skip %d %q %q %s", code, out, diag, data)
	}
}

func TestInitSkipsTimedOutTemplate(t *testing.T) {
	home := t.TempDir()
	configPluginScript(t, home, "slow", `{"config_protocol_version":1,"section":{"value":1}}`)
	path := filepath.Join(home, "plugins", "horizon-slow")
	script := "#!/bin/sh\nif [ \"$1\" = horizon-plugin-metadata ]; then printf '%s' '{\"protocol_version\":1,\"config_protocol_version\":1,\"version\":\"1\",\"description\":\"Test\"}'; exit; fi\nsleep 5\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	code, _, diag := runApp(t, "", "--home", home, "init")
	if code != ExitOK || !strings.Contains(diag, "slow") {
		t.Fatalf("timeout %d %q", code, diag)
	}
}

func TestInitInvalidYAMLRemainsUntouched(t *testing.T) {
	home := t.TempDir()
	configPluginScript(t, home, "sample", `{"config_protocol_version":1,"section":{"value":1}}`)
	path := filepath.Join(home, "config.yaml")
	initial := "plugins: [\n"
	if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	code, _, _ := runApp(t, "", "--home", home, "init")
	data, _ := os.ReadFile(path)
	if code == ExitOK || string(data) != initial {
		t.Fatalf("invalid config changed: %d %s", code, data)
	}
}
