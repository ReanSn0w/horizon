package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ReanSn0w/horizon/internal/responses"
	"github.com/creack/pty"
)

func TestBuiltBinaryPluginWorkflow(t *testing.T) {
	binary := buildBinary(t)
	homeA, homeB := t.TempDir(), t.TempDir()
	workspace := t.TempDir()
	if out, errout, err := run(binary, workspace, "", "--home", homeA, "init"); err != nil || errout != "" || !strings.Contains(out, "Horizon home") {
		t.Fatalf("init %v %q %q", err, out, errout)
	}
	for _, home := range []string{homeA, homeB} {
		if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("invalid provider"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	installed := filepath.Join(homeA, "plugins", "horizon-fixture")
	data := []byte(`#!/bin/sh
if [ "$1" = horizon-plugin-metadata ]; then
  printf '%s\n' '{"protocol_version":1,"version":"1.0.0","description":"Test fixture"}'
  exit 0
fi
if [ "$1" = greet ]; then
  printf 'Hello, %s!\n' "$2"
  exit 0
fi
exit 2
`)
	if err := os.WriteFile(installed, data, 0700); err != nil {
		t.Fatal(err)
	}
	if out, _, err := run(binary, workspace, "", "--home", homeA, "--help"); err != nil || !strings.Contains(out, "fixture") {
		t.Fatalf("help %v %q", err, out)
	}
	if out, errout, err := run(binary, workspace, "", "--home", homeA, "fixture", "greet", "Ada"); err != nil || out != "Hello, Ada!\n" || errout != "" {
		t.Fatalf("run %v %q %q", err, out, errout)
	}
	if _, _, err := run(binary, workspace, "", "--home", homeB, "fixture", "greet"); err == nil {
		t.Fatal("plugin leaked to second home")
	}
	if err := os.Remove(installed); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(binary, workspace, "", "--home", homeA, "fixture", "greet"); err == nil {
		t.Fatal("removed plugin still available")
	}
}

func TestPluginNestedShellTimeout(t *testing.T) {
	binary := buildBinary(t)
	home, workspace := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "plugins"), 0700); err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(home, "plugins", "horizon-sleeper")
	script := "#!/bin/sh\nif [ \"$1\" = horizon-plugin-metadata ]; then echo '{\"protocol_version\":1,\"version\":\"1\",\"description\":\"Sleeper\"}'; exit; fi\necho $$ > plugin.pid\nsleep 30\n"
	if err := os.WriteFile(plugin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	calls := 0
	timedOut := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		var request responses.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		output := message("done")
		if calls == 1 {
			output = []json.RawMessage{json.RawMessage(`{"type":"function_call","call_id":"plugin","name":"shell_exec","arguments":"{\"command\":\"horizon sleeper\",\"timeout_ms\":1200,\"max_output_chars\":16000}"}`)}
		} else {
			for _, raw := range request.Input {
				if strings.Contains(string(raw), `\"timed_out\":true`) {
					timedOut = true
				}
			}
		}
		writeCompleted(w, calls, output)
	}))
	defer server.Close()
	writeConfig(t, home, server.URL)
	if out, errout, err := run(binary, workspace, "", "--home", home, "resume", "-m", "run sleeper"); err != nil || out != "done\n" {
		t.Fatalf("resume: %v %q %q", err, out, errout)
	}
	mu.Lock()
	observed := timedOut
	mu.Unlock()
	if !observed {
		t.Fatal("shell tool did not report timeout")
	}
	data, err := os.ReadFile(filepath.Join(workspace, "plugin.pid"))
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	if _, err := fmt.Sscan(string(data), &pid); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("plugin process %d survived shell timeout", pid)
	}
}

func TestPluginNestedShellCancellation(t *testing.T) {
	binary := buildBinary(t)
	home, workspace := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "plugins"), 0700); err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(home, "plugins", "horizon-sleeper")
	script := "#!/bin/sh\nif [ \"$1\" = horizon-plugin-metadata ]; then echo '{\"protocol_version\":1,\"version\":\"1\",\"description\":\"Sleeper\"}'; exit; fi\necho $$ > plugin.pid\nsleep 30\n"
	if err := os.WriteFile(plugin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeCompleted(w, 1, []json.RawMessage{json.RawMessage(`{"type":"function_call","call_id":"plugin","name":"shell_exec","arguments":"{\"command\":\"horizon sleeper\",\"timeout_ms\":null,\"max_output_chars\":16000}"}`)})
	}))
	defer server.Close()
	writeConfig(t, home, server.URL)
	command := exec.Command(binary, "--home", home, "resume", "-m", "run sleeper")
	command.Dir = workspace
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	pidFile := filepath.Join(workspace, "plugin.pid")
	var data []byte
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, _ = os.ReadFile(pidFile)
		if len(data) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(data) == 0 {
		_ = command.Process.Kill()
		<-wait
		t.Fatalf("plugin did not start: %q", output.String())
	}
	var pid int
	if _, err := fmt.Sscan(string(data), &pid); err != nil {
		t.Fatal(err)
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-wait:
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 130 {
			t.Fatalf("cancel: %v output=%q", err, output.String())
		}
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		t.Fatal("cancel did not stop Horizon")
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("plugin %d survived cancellation", pid)
	}
}

func TestPluginSignalWithInheritedAccessFromUserEnvironment(t *testing.T) {
	binary := buildBinary(t)
	home, workspace := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "plugins"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(home, "plugins", "horizon-child")
	script := "#!/bin/sh\nif [ \"$1\" = horizon-plugin-metadata ]; then echo '{\"protocol_version\":1,\"version\":\"1\",\"description\":\"Child\"}'; exit; fi\n( trap '' TERM; echo ready > child.ready; sleep 2; echo survived > child.marker ) &\nwait\n"
	if err := os.WriteFile(plugin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "--home", home, "child")
	command.Dir = workspace
	command.Env = append(filteredEnv("HORIZON_INHERITED_ACCESS"), "HORIZON_INHERITED_ACCESS=write")
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	ready := filepath.Join(workspace, "child.ready")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(ready); err != nil {
		_ = command.Process.Kill()
		<-wait
		t.Fatalf("child did not start: %v %q", err, output.String())
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-wait:
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 143 {
			t.Fatalf("SIGTERM: %v output=%q", err, output.String())
		}
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		t.Fatal("Horizon did not stop")
	}
	time.Sleep(2200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(workspace, "child.marker")); err == nil {
		t.Fatal("managed child survived SIGTERM")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestBuiltBinaryPluginPTY(t *testing.T) {
	binary := buildBinary(t)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "plugins"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(home, "plugins", "horizon-interactive")
	script := "#!/bin/sh\nif [ \"$1\" = horizon-plugin-metadata ]; then echo '{\"protocol_version\":1,\"version\":\"1\",\"description\":\"Interactive\"}'; exit; fi\necho READY\nread line\necho GOT:$line\n"
	if err := os.WriteFile(plugin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	for _, cancel := range []bool{false, true} {
		command := exec.Command(binary, "--home", home, "interactive")
		terminal, err := pty.Start(command)
		if err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		var outputMu sync.Mutex
		readOutput := func() string { outputMu.Lock(); defer outputMu.Unlock(); return output.String() }
		ready := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			buf := make([]byte, 1024)
			announced := false
			for {
				n, err := terminal.Read(buf)
				if n > 0 {
					outputMu.Lock()
					output.Write(buf[:n])
					visible := output.String()
					outputMu.Unlock()
					if !announced && strings.Contains(visible, "READY") {
						announced = true
						close(ready)
					}
				}
				if err != nil {
					return
				}
			}
		}()
		select {
		case <-ready:
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			t.Fatalf("PTY not ready: %q", readOutput())
		}
		if cancel {
			_, _ = terminal.Write([]byte{3})
		} else {
			_, _ = terminal.Write([]byte("hello\n"))
		}
		wait := make(chan error, 1)
		go func() { wait <- command.Wait() }()
		select {
		case err = <-wait:
			if cancel {
				if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 130 {
					t.Fatalf("cancel: %v output=%q", err, readOutput())
				}
			} else if err != nil {
				t.Fatalf("input: %v output=%q", err, readOutput())
			}
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			t.Fatal("PTY plugin hung")
		}
		_ = terminal.Close()
		<-done
		if !cancel && !strings.Contains(readOutput(), "GOT:hello") {
			t.Fatalf("input lost: %q", readOutput())
		}
	}
}
