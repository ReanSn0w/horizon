package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
		if err := ctx.Err(); err != nil {
			return "", err
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
		out := &limitedBuffer{limit: 2 * 1024 * 1024}
		diag := &limitedBuffer{limit: 64 * 1024}
		cmd.Stdout = out
		cmd.Stderr = diag
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.WaitDelay = 2 * time.Second
		if err := cmd.Start(); err != nil {
			return "", errors.New("cannot start Horizon process")
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				var exit *exec.ExitError
				if errors.As(err, &exit) {
					return "", fmt.Errorf("Horizon process failed (exit %d); inspect its session locally", exit.ExitCode())
				}
				return "", errors.New("Horizon process failed")
			}
			if out.overflow || diag.overflow {
				return "", errors.New("Horizon process output exceeds the telegram limit")
			}
			return out.String(), nil
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
			return "", ctx.Err()
		}
	}
}
