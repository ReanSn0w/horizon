package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ReanSn0w/horizon/internal/config"
)

const continueUpdate = "manager-continue-update-v1"

// This small versioned envelope is the bootstrap protocol. It deliberately
// carries no core configuration: only the selected manager interprets that.
type preparedUpdate struct {
	Version                        int
	Options                        Options
	Stage, Commit, Ref, Repository string
	Source                         config.Source
}

func launchPrepared(ctx context.Context, p preparedUpdate, locks []*os.File, in io.Reader, out io.Writer) error {
	path := filepath.Join(p.Stage, "update.json")
	if err := writeJSON(path, p); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, filepath.Join(p.Stage, "horizon-manager-next"), continueUpdate, path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, out
	// The parent keeps both locks throughout the child process, including build,
	// configuration migration and deployment. There is no unlock/relock window.
	cmd.ExtraFiles = locks
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("selected manager could not complete update (revision must support %s): %w", continueUpdate, err)
	}
	data, err := os.ReadFile(filepath.Join(p.Stage, "update-complete"))
	if err != nil || string(data) != p.Commit {
		return errors.New("selected manager did not acknowledge update completion; use a revision supporting the update handoff protocol")
	}
	return nil
}

func resumePrepared(ctx context.Context, path string, out io.Writer) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var p preparedUpdate
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	if p.Version != 1 || p.Options.Command != "update" || !commitPattern.MatchString(p.Commit) || p.Repository == "" {
		return errors.New("invalid prepared update")
	}
	if err := p.Source.Validate(); err != nil {
		return err
	}
	home, err := filepath.Abs(p.Options.Home)
	if err != nil || home != p.Options.Home {
		return errors.New("prepared home must be absolute")
	}
	if filepath.Dir(p.Stage) != filepath.Join(home, ".manager") || filepath.Join(p.Stage, "update.json") != path {
		return errors.New("prepared update must be inside home staging directory")
	}
	for i, name := range []string{"operation.lock", "maintenance.lock"} {
		f := os.NewFile(uintptr(3+i), name)
		if f == nil {
			return errors.New("missing inherited maintenance lock")
		}
		defer f.Close()
		syscall.CloseOnExec(int(f.Fd()))
		actual, err := f.Stat()
		if err != nil {
			return fmt.Errorf("missing inherited %s: %w", name, err)
		}
		expected, err := os.Stat(filepath.Join(home, ".manager", name))
		if err != nil || !os.SameFile(actual, expected) {
			return fmt.Errorf("invalid inherited %s", name)
		}
		// These are duplicate descriptors for the parent's open file descriptions.
		// Flock is idempotent for them; a separate process cannot bypass a held lock.
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			return err
		}
	}
	e := engine()
	e.repository = p.Repository
	source := filepath.Join(p.Stage, "source")
	if err := e.checkCheckout(ctx, source, p.Commit); err != nil {
		return err
	}
	old, err := ReadState(home)
	if err != nil {
		return err
	}
	if err := e.checkCheckout(ctx, filepath.Join(home, "source"), old.Commit); err != nil {
		return err
	}
	if err := e.installPrepared(ctx, p.Options, old, true, p.Stage, p.Source, p.Commit, p.Ref, out); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(p.Stage, "update-complete"), []byte(p.Commit), 0600)
}
