package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/daemon"
	"github.com/ReanSn0w/horizon/internal/maintenance"
	flags "github.com/umputun/go-flags"
)

func printInputHelp(ctx context.Context, parser *flags.Parser, out, diag io.Writer) {
	home, err := config.ResolveHome("")
	if err != nil {
		fmt.Fprintln(diag, "agent: input discovery:", err)
		return
	}
	entries, err := os.ReadDir(filepath.Join(home, "bin"))
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		fmt.Fprintln(diag, "agent: input discovery:", err)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	heading := false
	for _, entry := range entries {
		name, ok := strings.CutPrefix(entry.Name(), "agent-")
		if !ok || parser.Find(name) != nil {
			continue
		}
		if ctx.Err() != nil {
			fmt.Fprintln(diag, "agent: input discovery time budget exceeded")
			break
		}
		metadata, err := daemon.AdapterInspect(ctx, home, name)
		if err != nil {
			fmt.Fprintf(diag, "agent: input %q unavailable: %v\n", name, err)
			continue
		}
		if !slices.Contains(metadata.Capabilities, daemon.AdapterAdminCLI) {
			continue
		}
		if !heading {
			fmt.Fprintln(out, "\nInstalled inputs:")
			heading = true
		}
		fmt.Fprintf(out, "  %s  %s (v%s)\n", name, metadata.Description, metadata.Version)
	}
}

func runAdapterAdmin(ctx context.Context, args []string, in io.Reader, out, diag io.Writer) int {
	if access, ok := os.LookupEnv("HORIZON_INHERITED_ACCESS"); ok && !slices.Contains([]string{"read", "write", "full"}, access) {
		fmt.Fprintf(diag, "agent: invalid inherited access mode %q\n", access)
		return 2
	}
	home, err := config.ResolveHome("")
	if err != nil {
		fmt.Fprintln(diag, "agent:", err)
		return 2
	}
	release, err := maintenance.AcquireReadOnly(home)
	if err != nil {
		fmt.Fprintln(diag, "agent:", err)
		return 1
	}
	defer release()
	name := args[0]
	metadata, err := daemon.AdapterInspect(ctx, home, name)
	if err != nil {
		fmt.Fprintf(diag, "agent: input %q unavailable: %v\n", name, err)
		return 2
	}
	if !slices.Contains(metadata.Capabilities, daemon.AdapterAdminCLI) {
		fmt.Fprintf(diag, "agent: input %q does not expose administrative commands\n", name)
		return 2
	}
	path, err := daemon.AdapterPath(home, name)
	if err != nil {
		fmt.Fprintln(diag, "agent:", err)
		return 2
	}
	command := exec.CommandContext(ctx, path, append([]string{"agent-admin"}, args[1:]...)...)
	command.Stdin, command.Stdout, command.Stderr = in, out, diag
	command.Env = slices.DeleteFunc(os.Environ(), func(item string) bool {
		return strings.HasPrefix(item, "HORIZON_ADAPTER_") || strings.HasPrefix(item, "HORIZON_DAEMON_SOCKET=") || strings.HasPrefix(item, "HORIZON_HOME=")
	})
	command.Env = append(command.Env, "HORIZON_HOME="+home)
	// Keep the parent's process group: Horizon forwards cancellation to the group.
	command.Cancel = func() error { return command.Process.Signal(syscall.SIGTERM) }
	command.WaitDelay = time.Second
	err = command.Run()
	if ctx.Err() != nil {
		return 130
	}
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal())
		}
		return exit.ExitCode()
	}
	fmt.Fprintln(diag, "agent: run input:", err)
	return 1
}
