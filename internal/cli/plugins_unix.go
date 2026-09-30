//go:build unix

package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/plugins"
	"golang.org/x/term"
)

func (a *App) printPluginHelp(homeFlag string, reserved []string) {
	home, err := config.ResolveHome(homeFlag)
	if err != nil {
		fmt.Fprintf(a.errOut, "horizon: plugin discovery: %v\n", err)
		return
	}
	entries, err := plugins.Candidates(home, reserved)
	if err != nil {
		fmt.Fprintf(a.errOut, "horizon: plugin discovery: %v\n", err)
		return
	}
	if len(entries) == 0 {
		return
	}
	fmt.Fprintln(a.out, "\nInstalled plugins:")
	deadline := time.Now().Add(5 * time.Second)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	for _, entry := range entries {
		if entry.Err == nil {
			if time.Now().After(deadline) {
				fmt.Fprintf(a.out, "  %s  [not checked: help time budget exceeded]\n", entry.Name)
				continue
			}
			remaining := time.Until(deadline)
			if remaining > plugins.MetadataTimeout {
				remaining = plugins.MetadataTimeout
			}
			entry = plugins.InspectTimeout(home, entry.Name, reserved, remaining)
			if remaining < plugins.MetadataTimeout && errors.Is(entry.Err, plugins.ErrMetadataTimeout) {
				fmt.Fprintf(a.out, "  %s  [not checked: help time budget exceeded]\n", entry.Name)
				continue
			}
		}
		if entry.Err != nil {
			fmt.Fprintf(a.errOut, "horizon: plugin %q unavailable: %v\n", entry.Name, entry.Err)
			continue
		}
		fmt.Fprintf(a.out, "  %s  %s (v%s)\n", entry.Name, entry.Metadata.Description, entry.Metadata.Version)
	}
}

func (a *App) runPlugin(entry plugins.Entry, args []string, home string) int {
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintf(a.errOut, "horizon: locate executable: %v\n", err)
		return ExitFailure
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		fmt.Fprintf(a.errOut, "horizon: locate executable: %v\n", err)
		return ExitFailure
	}
	cwd, err := a.workingDir()
	if err != nil {
		fmt.Fprintf(a.errOut, "horizon: working directory: %v\n", err)
		return ExitFailure
	}
	command := exec.Command(entry.Path, args...)
	command.Dir = cwd
	command.Stdin = a.in
	command.Stdout = a.out
	command.Stderr = a.errOut
	command.Env = plugins.Environment(os.Environ(), home, executable)
	terminal := false
	if file, ok := a.in.(*os.File); ok {
		terminal = term.IsTerminal(int(file.Fd()))
	}
	// A nonterminal plugin has its own group so Horizon can stop its children.
	// A nested Horizon receives shell_exec's signal and forwards it here.
	if !terminal {
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	if err := command.Start(); err != nil {
		fmt.Fprintf(a.errOut, "horizon: start plugin %q: %v\n", entry.Name, err)
		return ExitFailure
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	select {
	case err = <-done:
	case received := <-signals:
		sig := received.(syscall.Signal)
		if terminal {
			_ = command.Process.Signal(sig)
		} else {
			_ = syscall.Kill(-command.Process.Pid, sig)
		}
		timer := time.NewTimer(time.Second)
		select {
		case err = <-done:
		case <-timer.C:
			if terminal {
				_ = command.Process.Kill()
			} else {
				_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			}
			err = <-done
		}
		timer.Stop()
		if !terminal {
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		}
		if sig == syscall.SIGINT {
			return ExitInterrupted
		}
		return 128 + int(sig)
	}
	if err == nil {
		return ExitOK
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal())
		}
		return exit.ExitCode()
	}
	fmt.Fprintf(a.errOut, "horizon: wait for plugin %q: %v\n", entry.Name, err)
	return ExitFailure
}
