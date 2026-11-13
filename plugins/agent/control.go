package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ReanSn0w/horizon/internal/daemon"
	"github.com/ReanSn0w/horizon/internal/session"
)

type serviceRunner func(context.Context, io.Writer, string, ...string) error

func runServiceCommand(ctx context.Context, out io.Writer, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = out
	command.Stderr = out
	if err := command.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func controlService(ctx context.Context, home, action string, out io.Writer) error {
	if err := controlServiceWith(ctx, runtime.GOOS, home, action, out, runServiceCommand); err != nil {
		return err
	}
	if action == "start" {
		return waitForDaemon(ctx, home)
	}
	return nil
}

func waitForDaemon(ctx context.Context, home string) error {
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := daemon.Call(ctx, home, daemon.Request{ID: "status", Method: "hello"}); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("agent service did not become ready; inspect horizon agent logs")
		case <-tick.C:
		}
	}
}

func controlServiceWith(ctx context.Context, platform, home, action string, out io.Writer, run serviceRunner) error {
	if action == "start" {
		if err := validate(ctx, home, out); err != nil {
			return err
		}
	}
	path, err := servicePath(platform, home)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("service file %s: %w; run horizon agent service install", path, err)
	}
	switch platform {
	case "darwin":
		if action == "start" && serviceLoaded(platform, home, path) {
			if _, err := daemon.Call(ctx, home, daemon.Request{ID: "status", Method: "hello"}); err == nil {
				_, err = fmt.Fprintln(out, "Agent service is already running")
				return err
			}
			label := "horizon-agent-" + session.WorkspaceID(home)
			return run(ctx, out, "launchctl", "kickstart", "gui/"+strconv.Itoa(os.Getuid())+"/"+label)
		}
		verb := "bootstrap"
		if action == "stop" {
			verb = "bootout"
		}
		return run(ctx, out, "launchctl", verb, "gui/"+strconv.Itoa(os.Getuid()), path)
	case "linux":
		if action == "start" {
			if err := run(ctx, out, "systemctl", "--user", "daemon-reload"); err != nil {
				return err
			}
		}
		return run(ctx, out, "systemctl", "--user", action, filepath.Base(path))
	default:
		return fmt.Errorf("service control is supported on Linux and macOS")
	}
}

func logs(ctx context.Context, home string, lines int, follow bool, out io.Writer) error {
	return logsWith(ctx, runtime.GOOS, home, lines, follow, out, runServiceCommand)
}

func logsWith(ctx context.Context, platform, home string, lines int, follow bool, out io.Writer, run serviceRunner) error {
	if lines < 1 || lines > 10000 {
		return fmt.Errorf("--lines must be between 1 and 10000")
	}
	switch platform {
	case "darwin":
		paths := []string{}
		for _, name := range []string{"agent.stderr.log", "agent.stdout.log"} {
			path := filepath.Join(home, "run", name)
			if _, err := os.Stat(path); err == nil {
				paths = append(paths, path)
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		if len(paths) == 0 {
			return fmt.Errorf("no service logs found; reinstall the service file to enable log capture")
		}
		args := []string{"-n", strconv.Itoa(lines)}
		if follow {
			args = append(args, "-F")
		}
		return run(ctx, out, "tail", append(args, paths...)...)
	case "linux":
		path, err := servicePath(platform, home)
		if err != nil {
			return err
		}
		args := []string{"--user", "-u", filepath.Base(path), "-n", strconv.Itoa(lines), "--no-pager"}
		if follow {
			args = append(args, "-f")
		}
		return run(ctx, out, "journalctl", args...)
	default:
		return fmt.Errorf("service logs are supported on Linux and macOS")
	}
}

func validate(ctx context.Context, home string, out io.Writer) error {
	settings, err := daemon.LoadSettings(home)
	if err != nil {
		return err
	}
	if err := daemon.ValidateAdapters(ctx, home, settings); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Agent configuration valid; enabled adapters: %s\n", strings.Join(settings.Adapters, ", "))
	return err
}
