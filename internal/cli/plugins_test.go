package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pluginScript(t *testing.T, home, name, script string) {
	t.Helper()
	dir := filepath.Join(home, "plugins")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "horizon-"+name), []byte("#!/bin/sh\nif [ \"$1\" = horizon-plugin-metadata ]; then printf '%s' '{\"protocol_version\":1,\"version\":\"1\",\"description\":\"Example\"}'; exit; fi\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestPluginRoutingAndStreams(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("not parsed"), 0600); err != nil {
		t.Fatal(err)
	}
	pluginScript(t, home, "echo", "printf 'cwd=%s home=%s exe=%s\\n' \"$PWD\" \"$HORIZON_HOME\" \"$HORIZON_EXECUTABLE\"; printf 'args:'; printf '<%s>' \"$@\"; printf '\\n'; cat; echo diagnostic >&2\n")
	workspace := t.TempDir()
	workspace, _ = filepath.EvalSymlinks(workspace)
	var out, diagnostic bytes.Buffer
	app := New(strings.NewReader("input\n"), &out, &diagnostic)
	app.workingDir = func() (string, error) { return workspace, nil }
	code := app.Run([]string{"--home=" + home, "--verbose", "echo", "a b", "", "$(touch /tmp/horizon-plugin-injection)"})
	if code != 0 || !strings.Contains(out.String(), "<a b><><$(touch /tmp/horizon-plugin-injection)>") || !strings.Contains(out.String(), "input") || !strings.Contains(out.String(), "cwd="+workspace) || !strings.Contains(out.String(), "home="+home) || !strings.Contains(diagnostic.String(), "diagnostic") {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), diagnostic.String())
	}
	if _, err := os.Stat("/tmp/horizon-plugin-injection"); err == nil {
		t.Fatal("shell interpreted argument")
	}
}

func TestPluginHelpAndMissingHome(t *testing.T) {
	home := t.TempDir()
	pluginScript(t, home, "sample", "echo plugin-help\n")
	code, out, errout := runApp(t, "", "--home", home, "--help")
	if code != 0 || !strings.Contains(out, "sample") || errout != "" {
		t.Fatalf("help %d %q %q", code, out, errout)
	}
	code, out, errout = runApp(t, "", "--home", home, "sample", "--help")
	if code != 0 || !strings.Contains(out, "plugin-help") || errout != "" {
		t.Fatalf("plugin help %d %q %q", code, out, errout)
	}
	if _, err := os.Stat(filepath.Join(home, "config.yaml")); !os.IsNotExist(err) {
		t.Fatalf("created config: %v", err)
	}
	code, _, _ = runApp(t, "", "--home", home, "sample")
	if code != ExitUsage {
		t.Fatalf("without config: %d", code)
	}
}

func TestPluginExitCodesSelectionAndIsolation(t *testing.T) {
	homeA, homeB := t.TempDir(), t.TempDir()
	for _, home := range []string{homeA, homeB} {
		if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("invalid provider"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	pluginScript(t, homeA, "only-a", "exit 37\n")
	pluginScript(t, homeB, "only-b", "echo other\n")
	t.Setenv("HORIZON_HOME", homeB)
	if code, _, errout := runApp(t, "", "--home", homeA, "only-a"); code != 37 || errout != "" {
		t.Fatalf("exit code %d %q", code, errout)
	}
	if code, _, _ := runApp(t, "", "--home", homeA, "only-b"); code != ExitUsage {
		t.Fatalf("cross-home command code %d", code)
	}
	if code, out, _ := runApp(t, "", "only-b"); code != 0 || !strings.Contains(out, "other") {
		t.Fatalf("environment selection %d %q", code, out)
	}
	pluginScript(t, homeA, "resume", "echo forbidden\n")
	if code, _, _ := runApp(t, "", "--home", homeA, "resume", "--bad"); code != ExitUsage {
		t.Fatalf("reserved command code %d", code)
	}
	pluginScript(t, homeA, "broken", "exit 1\n")
	path := filepath.Join(homeA, "plugins", "horizon-broken")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if code, _, errout := runApp(t, "", "--home", homeA, "broken"); code != ExitUsage || !strings.Contains(errout, "unavailable") {
		t.Fatalf("broken %d %q", code, errout)
	}
}

func TestPluginSignalAndNoHomeCreation(t *testing.T) {
	home := filepath.Join(t.TempDir(), "missing")
	if code, _, _ := runApp(t, "", "--home", home, "unknown"); code != ExitUsage {
		t.Fatal(code)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("created home: %v", err)
	}
	home = t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	pluginScript(t, home, "signal", "kill -TERM $$\n")
	if code, _, errout := runApp(t, "", "--home", home, "signal"); code != 143 || errout != "" {
		t.Fatalf("signal %d %q", code, errout)
	}
}

func TestPluginHelpBudgetAndDiagnostics(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{"a", "b", "c", "d"} {
		pluginScript(t, home, name, "echo okay\n")
		path := filepath.Join(home, "plugins", "horizon-"+name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 3\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	code, out, errout := runApp(t, "", "--home", home, "--help")
	if code != 0 || time.Since(start) > 6*time.Second || !strings.Contains(out, "not checked") || !strings.Contains(errout, "unavailable") {
		t.Fatalf("help %d %v %q %q", code, time.Since(start), out, errout)
	}
}

func TestBuiltinsDoNotRunPluginMetadata(t *testing.T) {
	home := testHome(t, "mode: unit\ndefault_model: coding\nmodels:\n  coding:\n    model: local\n    compact_threshold: 1000\nprovider:\n  url: https://example.test/v1\n  key: x\n")
	dir := filepath.Join(home, "plugins")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(home, "metadata-ran")
	script := "#!/bin/sh\ntouch \"$HORIZON_HOME/metadata-ran\"\nsleep 3\n"
	if err := os.WriteFile(filepath.Join(dir, "horizon-broken"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	code, out, errout := runApp(t, "", "--home", home, "models")
	if code != 0 || !strings.Contains(out, "coding") || errout != "" {
		t.Fatalf("builtin %d %q %q", code, out, errout)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("metadata ran: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "dialogs")); !os.IsNotExist(err) {
		t.Fatalf("history created: %v", err)
	}
}

func TestPluginsAreNotSearchedOutsideSelectedHome(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	pluginScript(t, parent, "stray", "echo found\n")
	pathDir := t.TempDir()
	pluginScript(t, pathDir, "path-only", "echo found\n")
	t.Setenv("PATH", filepath.Join(pathDir, "plugins")+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, name := range []string{"stray", "path-only"} {
		code, _, diagnostic := runAppAt(t, workspace, "", "--home", home, name)
		if code != ExitUsage || !strings.Contains(diagnostic, "Unknown command") {
			t.Fatalf("%s: %d %q", name, code, diagnostic)
		}
	}
}

func TestBareWordIsPluginNameNotResumeMessage(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	code, _, diagnostic := runApp(t, "", "--home", home, "hello")
	if code != ExitUsage || !strings.Contains(diagnostic, "Unknown command") {
		t.Fatalf("bare word: code=%d diagnostic=%q", code, diagnostic)
	}
	code, _, diagnostic = runApp(t, "", "--home", home, "resume", "hello")
	if code != ExitUsage || !strings.Contains(diagnostic, "unexpected arguments") {
		t.Fatalf("resume positional argument: code=%d diagnostic=%q", code, diagnostic)
	}
	pluginScript(t, home, "hello", "printf 'plugin:%s\\n' \"$1\"\n")
	code, output, diagnostic := runApp(t, "", "--home", home, "hello", "Ada")
	if code != ExitOK || output != "plugin:Ada\n" || diagnostic != "" {
		t.Fatalf("plugin: code=%d output=%q diagnostic=%q", code, output, diagnostic)
	}
}
