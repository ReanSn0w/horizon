package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func inputFixture(t *testing.T, home, name, capabilities, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = agent-plugin-metadata ]; then
 printf '%%s' '{"protocol_version":1,"version":"test","description":"Test input","capabilities":%s}'
 exit
fi
%s
`, capabilities, body)
	if err := os.WriteFile(filepath.Join(home, "bin", "agent-"+name), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestAgentHelpDiscoversAdministrativeInputs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HORIZON_HOME", home)
	inputFixture(t, home, "timer", `["admin-cli"]`, "exit 99")
	inputFixture(t, home, "headless", `[]`, "exit 99")
	inputFixture(t, home, "start", `["admin-cli"]`, "exit 99")
	if err := os.WriteFile(filepath.Join(home, "bin", "agent-broken"), []byte("#!/bin/sh\necho invalid"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"-h"}, {"--help"}} {
		var out, diag bytes.Buffer
		if code := run(context.Background(), args, &out, &diag); code != 0 {
			t.Fatal(code, diag.String())
		}
		_, inputs, ok := strings.Cut(out.String(), "Installed inputs:")
		if !ok || !strings.Contains(inputs, "timer  Test input (vtest)") || strings.Contains(inputs, "headless") || strings.Contains(inputs, "start") {
			t.Fatal(out.String())
		}
		if !strings.Contains(diag.String(), `input "broken" unavailable`) {
			t.Fatal(diag.String())
		}
	}
	var out, diag bytes.Buffer
	if code := run(context.Background(), []string{"logs", "-h"}, &out, &diag); code != 0 || strings.Contains(out.String(), "Installed inputs:") || diag.Len() != 0 {
		t.Fatalf("%d %s %s", code, &out, &diag)
	}
	if _, err := os.Stat(filepath.Join(home, "config.yaml")); !os.IsNotExist(err) {
		t.Fatal("help created config")
	}
}

func TestInputDispatchPreservesArgumentsStreamsAndExit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HORIZON_HOME", home)
	t.Setenv("HORIZON_INHERITED_ACCESS", "read")
	t.Setenv("HORIZON_ADAPTER_TOKEN", "fixture")
	t.Setenv("HORIZON_DAEMON_SOCKET", "fixture")
	inputFixture(t, home, "timer", `["admin-cli"]`, `printf '<%s>' "$@"
printf '\nhome=%s access=%s token=%s socket=%s\n' "$HORIZON_HOME" "$HORIZON_INHERITED_ACCESS" "${HORIZON_ADAPTER_TOKEN-unset}" "${HORIZON_DAEMON_SOCKET-unset}"
cat
echo diagnostic >&2
exit 37`)
	var out, diag bytes.Buffer
	code := runAdapterAdmin(context.Background(), []string{"timer", "command", "a b", "", "$(never-run)"}, strings.NewReader("input-data\n"), &out, &diag)
	if code != 37 || !strings.Contains(out.String(), "<agent-admin><command><a b><><$(never-run)>") || !strings.Contains(out.String(), "home="+home+" access=read token=unset socket=unset") || !strings.Contains(out.String(), "input-data") || diag.String() != "diagnostic\n" {
		t.Fatalf("%d %s %s", code, &out, &diag)
	}
	out.Reset()
	diag.Reset()
	code = run(context.Background(), []string{"timer", "--help"}, &out, &diag)
	if code != 37 || !strings.Contains(out.String(), "<agent-admin><--help>") {
		t.Fatalf("%d %s %s", code, &out, &diag)
	}
}

func TestInputDispatchRejectsUnsupportedAndInvalidAccess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HORIZON_HOME", home)
	t.Setenv("HORIZON_INHERITED_ACCESS", "write")
	inputFixture(t, home, "headless", `[]`, "echo must-not-run")
	for _, name := range []string{"headless", "missing", "../escape"} {
		var out, diag bytes.Buffer
		if code := run(context.Background(), []string{name}, &out, &diag); code != 2 || out.Len() != 0 {
			t.Fatalf("%d %s %s", code, &out, &diag)
		}
	}
	t.Setenv("HORIZON_INHERITED_ACCESS", "invalid")
	inputFixture(t, home, "timer", `["admin-cli"]`, "echo must-not-run")
	var out, diag bytes.Buffer
	if code := run(context.Background(), []string{"timer"}, &out, &diag); code != 2 || out.Len() != 0 || !strings.Contains(diag.String(), "invalid inherited access") {
		t.Fatalf("%d %s %s", code, &out, &diag)
	}
}
