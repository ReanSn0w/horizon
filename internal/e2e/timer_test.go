package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ReanSn0w/horizon/internal/daemon"
	"github.com/ReanSn0w/horizon/internal/timer"
)

func installTimer(t *testing.T, home string) {
	t.Helper()
	timerBinary := filepath.Join(home, "bin", "agent-timer")
	if err := os.MkdirAll(filepath.Dir(timerBinary), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", timerBinary, "./plugins/agent-timer")
	cmd.Dir = "../.."
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build timer: %v %s", err, output)
	}
}

func installAgentCLI(t *testing.T, home string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", filepath.Join(home, "bin", "horizon-agent"), "./plugins/agent")
	cmd.Dir = "../.."
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build agent: %v %s", err, output)
	}
}

func TestAgentTimerValidateOffline(t *testing.T) {
	binary := buildBinary(t)
	home, workspace := t.TempDir(), t.TempDir()
	installTimer(t, home)
	installAgentCLI(t, home)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	writeConfig(t, home, server.URL)
	if err := os.Mkdir(filepath.Join(home, ".manager"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".manager", "maintenance.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "timers")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	valid := fmt.Sprintf("---\nname: Example\ndescription: Example task\nworkspace: %q\nschedule:\n  every: 10m\n  timezone: UTC\nenabled: false\n---\nInspect workspace.\n", workspace)
	for name, text := range map[string]string{"good.md": valid, "bad.md": "broken"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	out, diag, err := run(binary, workspace, "unused input", "--home", home, "agent", "timer", "validate")
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 ||
		out != "good: valid (disabled)\n" || !strings.Contains(diag, "bad:") {
		t.Fatalf("validate: stdout=%q stderr=%q err=%v", out, diag, err)
	}
	out, diag, err = run(binary, workspace, "", "--home", home, "agent", "timer", "validate", "good")
	if err != nil || out != "good: valid (disabled)\n" || diag != "" {
		t.Fatalf("validate good: stdout=%q stderr=%q err=%v", out, diag, err)
	}
	out, diag, err = run(binary, workspace, "", "--home", home, "agent", "timer", "--help")
	if err != nil || (!strings.Contains(out, "horizon agent timer") || !strings.Contains(out, "Validate schedules")) || diag != "" {
		t.Fatalf("help: stdout=%q stderr=%q err=%v", out, diag, err)
	}
	out, diag, err = run(binary, workspace, "", "--home", home, "agent", "-h")
	if err != nil || !strings.Contains(out, "Installed inputs:") || !strings.Contains(out, "timer") || diag != "" {
		t.Fatalf("agent help: %s %s %v", out, diag, err)
	}
	if requests.Load() != 0 {
		t.Fatalf("provider calls = %d", requests.Load())
	}
	for _, path := range []string{"timer", "daemon", "run", "dialogs"} {
		if _, err := os.Stat(filepath.Join(home, path)); !os.IsNotExist(err) {
			t.Fatalf("administrative command created %s: %v", path, err)
		}
	}
}

func TestAgentAdminForwardsOpaqueArgumentsAndStreams(t *testing.T) {
	binary := buildBinary(t)
	home, workspace := t.TempDir(), t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	installAgentCLI(t, home)
	writeConfig(t, home, "https://example.test/v1")
	const script = `#!/bin/sh
if [ "$1" = agent-plugin-metadata ]; then
  printf '%s\n' '{"protocol_version":1,"version":"test","description":"test","capabilities":["admin-cli"]}'
  exit 0
fi
if [ "$1" != agent-admin ] || [ "$2" != 'a; $(echo nope)' ] || [ "$3" != '--help' ]; then
  exit 10
fi
if [ -n "${HORIZON_ADAPTER_CREDENTIAL+x}" ] || [ "$HORIZON_INHERITED_ACCESS" != read ]; then
  exit 11
fi
IFS= read -r input
printf '%s\n' "$HORIZON_HOME" "$(pwd -P)" "$input"
printf '%s\n' diagnostics >&2
exit 19
`
	if err := os.WriteFile(filepath.Join(bin, "agent-test"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HORIZON_ADAPTER_CREDENTIAL", "adapter-test-marker")
	t.Setenv("HORIZON_INHERITED_ACCESS", "read")
	out, diag, err := run(binary, workspace, "input text\n", "--home", home, "agent", "test", "a; $(echo nope)", "--help")
	realWorkspace, pathErr := filepath.EvalSymlinks(workspace)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	wantOut := home + "\n" + realWorkspace + "\ninput text\n"
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 19 || out != wantOut || diag != "diagnostics\n" {
		t.Fatalf("agent command: stdout=%q stderr=%q err=%v", out, diag, err)
	}
	if err := os.WriteFile(filepath.Join(bin, "agent-legacy"), []byte(`#!/bin/sh
printf '%s\n' '{"protocol_version":1,"version":"test","description":"test","capabilities":[]}'
`), 0700); err != nil {
		t.Fatal(err)
	}
	out, diag, err = run(binary, workspace, "", "--home", home, "agent", "legacy", "validate")
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 ||
		out != "" || !strings.Contains(diag, "does not expose administrative commands") {
		t.Fatalf("legacy adapter: stdout=%q stderr=%q err=%v", out, diag, err)
	}
}

func TestAgentTimerRunsHorizonThroughDaemon(t *testing.T) {
	home, workspace := daemonHome(t), t.TempDir()
	installTestBinary(t, buildBinary(t), filepath.Join(home, "bin", "horizon"))
	installTimer(t, home)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			tool := json.RawMessage(`{"type":"function_call","call_id":"persist","name":"shell_exec","arguments":"{\"command\":\"printf 'scheduled' > timer-marker\",\"timeout_ms\":null,\"max_output_chars\":null}"}`)
			writeCompleted(w, 1, []json.RawMessage{tool})
		default:
			writeCompleted(w, 2, message("scheduled result"))
		}
	}))
	defer server.Close()
	writeConfig(t, home, server.URL)
	path := filepath.Join(home, "config.yaml")
	config, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	config = append(config, "\nplugins:\n  daemon:\n    adapters: [timer]\n  agent-timer:\n    reload_interval: 1s\n"...)
	if err := os.WriteFile(path, config, 0600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HORIZON_INHERITED_ACCESS", "write")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	done := make(chan error, 1)
	go func() { done <- daemon.Start(ctx, home) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	waitUntil := func(check func() bool) {
		t.Helper()
		for ctx.Err() == nil {
			if check() {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("timer integration timed out")
	}
	waitUntil(func() bool {
		_, err := os.Stat(filepath.Join(home, "timer", "scheduler.lock"))
		return err == nil
	})

	// Fit an interval of roughly two minutes to the epoch grid with its next
	// slot about four seconds away, without adding a short-period production knob.
	target := time.Now().Add(4 * time.Second).UnixNano()
	interval := target / (target / int64(2*time.Minute))
	definition := fmt.Sprintf("---\nname: Reports\ndescription: Update state\nworkspace: %q\nschedule:\n  every: %dns\n  timezone: UTC\n---\nUpdate workspace state.\n", workspace, interval)
	dir := filepath.Join(home, "timers")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	draft := filepath.Join(dir, ".draft")
	if err := os.WriteFile(draft, []byte(definition), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(draft, filepath.Join(dir, "reports.md")); err != nil {
		t.Fatal(err)
	}

	var taskID string
	waitUntil(func() bool {
		records, err := timer.Status(home)
		if err != nil {
			t.Fatal(err)
		}
		taskID = records["reports"].LastTaskID
		return records["reports"].LastEvent == "succeeded"
	})
	marker, err := os.ReadFile(filepath.Join(workspace, "timer-marker"))
	if err != nil || string(marker) != "scheduled" || calls.Load() != 2 {
		t.Fatalf("timer workspace effects: %q %v, requests=%d", marker, err, calls.Load())
	}
	var stored struct{ Tasks map[string]daemon.Task }
	data, err := os.ReadFile(filepath.Join(home, "daemon", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	task := stored.Tasks[taskID]
	if task.Session == "" || task.Route != "" || task.Channel || task.Mode != daemon.FreshTask || task.Result != "scheduled result" {
		t.Fatalf("unexpected timer task: %+v", task)
	}
}
