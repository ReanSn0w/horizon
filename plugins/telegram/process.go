package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/ReanSn0w/horizon/internal/config"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type runProcess func(context.Context, string, []string, string) (string, error)

func horizonBinary() (string, error) {
	value := os.Getenv("HORIZON_EXECUTABLE")
	if value == "" {
		var err error
		value, err = exec.LookPath("horizon")
		if err != nil {
			return "", errors.New("cannot find Horizon; launch telegram through horizon")
		}
	}
	path, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Mode()&0111 == 0 {
		return "", errors.New("Horizon executable is unavailable")
	}
	return path, nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

// Mask bytes.Buffer.ReadFrom so io.Copy cannot bypass the output limit.
func (b *limitedBuffer) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(struct{ io.Writer }{b}, r)
}
func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.Len()
	if remaining < len(p) {
		b.overflow = true
		p = p[:max(0, remaining)]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}
func processRunner(binary, home string) runProcess {
	return func(ctx context.Context, dir string, args []string, input string) (string, error) {
		out := &limitedBuffer{limit: 2 * 1024 * 1024}
		err := executeProcess(ctx, binary, home, dir, args, input, out)
		if err != nil {
			return "", err
		}
		if out.overflow {
			return "", errors.New("Horizon process output exceeds the telegram limit")
		}
		return out.String(), nil
	}
}
func executeProcess(ctx context.Context, binary, home, dir string, args []string, input string, out io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(input)
	var env []string
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "HORIZON_HOME=") && !strings.HasPrefix(value, "HORIZON_EXECUTABLE=") {
			env = append(env, value)
		}
	}
	cmd.Env = append(env, "HORIZON_HOME="+home, "HORIZON_EXECUTABLE="+binary)
	diag := &limitedBuffer{limit: 64 * 1024}
	cmd.Stdout = out
	cmd.Stderr = diag
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return errors.New("cannot start Horizon process")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				return fmt.Errorf("Horizon process failed (exit %d): %s", exit.ExitCode(), processDiagnostic(home, diag.String()))
			}
			return errors.New("Horizon process failed")
		}
		if diag.overflow {
			return errors.New("Horizon process output exceeds the telegram limit")
		}
		return nil
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		// Always finish the managed group, including children that ignore TERM.
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-done:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		case <-timer.C:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		return ctx.Err()
	}
}

func processDiagnostic(home, value string) string {
	if cfg, err := config.Load(home); err == nil {
		for _, secret := range []string{cfg.Provider.Key, cfg.Decision.Provider.Key} {
			if secret != "" {
				value = strings.ReplaceAll(value, secret, "[redacted]")
			}
		}
	}
	if cfg, err := loadSettings(home, false); err == nil && cfg.Telegram.Token != "" {
		value = strings.ReplaceAll(value, cfg.Telegram.Token, "[redacted]")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "no stderr; inspect the session locally"
	}
	return shortened(value, 4096)
}
