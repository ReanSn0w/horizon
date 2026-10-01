package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLaunchAgentLifecycle(t *testing.T) {
	home := configHome(t)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HORIZON_EXECUTABLE", binary)
	var out bytes.Buffer
	a := &app{home: home, ctx: context.Background(), out: &out}
	loaded := false
	calls := []string{}
	host := serviceHost{platform: "darwin", userHome: t.TempDir(), uid: 501, run: func(ctx context.Context, name string, args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		switch args[0] {
		case "print":
			if !loaded {
				return "", fmt.Errorf("not loaded")
			}
		case "bootstrap":
			loaded = true
		case "bootout":
			loaded = false
		}
		return "", nil
	}}
	opt := &serviceCommand{Manager: "launchd"}
	opt.Args.Action = "install"
	if err = manageService(a, opt, host); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(host.userHome, "Library", "LaunchAgents", serviceName(home)+".plist")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	for {
		_, err := decoder.Token()
		if err != nil {
			if err.Error() != "EOF" {
				t.Fatal(err)
			}
			break
		}
	}
	if !bytes.Contains(data, []byte("--log-file")) || bytes.Contains(data, []byte("bot_token")) {
		t.Fatal("bad service arguments")
	}
	for _, action := range []string{"install", "start", "status", "stop", "restart", "uninstall", "uninstall"} {
		opt.Args.Action = action
		if err = manageService(a, opt, host); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	if len(calls) == 0 {
		t.Fatal("service manager not called")
	}
	if _, err = os.Stat(filepath.Join(home, "config.yaml")); err != nil {
		t.Fatal("uninstall removed config")
	}
}
func TestRotatingLog(t *testing.T) {
	log := &rotatingLog{path: filepath.Join(t.TempDir(), "service.log"), limit: 8}
	log.Write([]byte("123456"))
	log.Write([]byte("abcdef"))
	old, _ := os.ReadFile(log.path + ".1")
	current, _ := os.ReadFile(log.path)
	if string(old) != "123456" || string(current) != "abcdef" {
		t.Fatal("rotation failed")
	}
}

func TestSystemdLifecycleAndQuoting(t *testing.T) {
	home := configHome(t)
	binary, _ := os.Executable()
	t.Setenv("HORIZON_EXECUTABLE", binary)
	var out bytes.Buffer
	a := &app{home: home, ctx: context.Background(), out: &out}
	calls := []string{}
	host := serviceHost{platform: "linux", configHome: t.TempDir(), run: func(ctx context.Context, name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return "active", nil
	}}
	opt := &serviceCommand{Manager: "systemd"}
	for _, action := range []string{"install", "install", "start", "status", "stop", "restart", "uninstall", "uninstall"} {
		opt.Args.Action = action
		if err := manageService(a, opt, host); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	if !strings.Contains(strings.Join(calls, "\n"), "--user enable") {
		t.Fatal("service not enabled")
	}
	unit := string(systemdUnit("name", "/a path/%/$/binary", "/home path", "/a:path"))
	if !strings.Contains(unit, `"/a path/%%/$$/binary"`) || !strings.Contains(unit, "KillMode=control-group") {
		t.Fatal(unit)
	}
}

func TestSystemdEnvironmentDoesNotDoubleDollars(t *testing.T) {
	unit := string(systemdUnit("name", "/bin/horizon", "/home/user", "/tools/$literal/%path"))
	if !strings.Contains(unit, `Environment="PATH=/tools/$literal/%%path"`) {
		t.Fatal(unit)
	}
}

func TestNativePlistFormat(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS plist validator")
	}
	path := filepath.Join(t.TempDir(), "telegram.plist")
	data := launchPlist("io.horizon.test", "/path with spaces/horizon", "/tmp/home & test", "/bin:/usr/bin")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("plutil", "-lint", path).CombinedOutput(); err != nil {
		t.Fatalf("plist invalid: %v %s", err, output)
	}
}
