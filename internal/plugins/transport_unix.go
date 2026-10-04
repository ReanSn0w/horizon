//go:build unix

package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// Decode rejects extra fields, invalid UTF-8 and trailing JSON.
func Decode(data []byte, target any) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("JSON must be UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected exactly one JSON value")
	}
	return nil
}

// Call never retries. Errors after Start conservatively indicate unknown effects.
func Call(ctx context.Context, path, operation string, request Request, timeout time.Duration) (Reply, string, error) {
	var empty Reply
	switch operation {
	case "describe", "context", "maintain", "tool", "finalize":
	default:
		return empty, "", fmt.Errorf("invalid agent operation")
	}
	data, err := json.Marshal(request)
	if err != nil {
		return empty, "", err
	}
	if len(data) > OutputLimit {
		return empty, "", &Failure{Code: "plugin_start_failed", Message: "plugin request too large"}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return empty, "", &Failure{Code: "plugin_start_failed", Message: err.Error()}
	}
	cmd := exec.Command(path, "horizon-plugin-agent-"+operation)
	executable, err := os.Executable()
	if err != nil {
		return empty, "", err
	}
	cmd.Env = Environment(os.Environ(), request.Home, executable, request.Access)
	cmd.Dir = request.Workspace
	cmd.Stdin = bytes.NewReader(data)
	stdout := &cappedBuffer{exceeded: make(chan struct{})}
	stderr := &cappedBuffer{exceeded: make(chan struct{})}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 250 * time.Millisecond
	if err := cmd.Start(); err != nil {
		return empty, "", &Failure{Code: "plugin_start_failed", Message: err.Error()}
	}
	defer killGroup(cmd)
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	var reason error
	select {
	case reason = <-waited:
	case <-ctx.Done():
		reason = ctx.Err()
		terminate(cmd, waited)
	case <-stdout.exceeded:
		reason = fmt.Errorf("plugin stdout exceeds limit")
		terminate(cmd, waited)
	case <-stderr.exceeded:
		reason = fmt.Errorf("plugin stderr exceeds limit")
		terminate(cmd, waited)
	}
	diagnostic := stderr.buffer.String()
	if stdout.once || stderr.once {
		reason = fmt.Errorf("plugin output exceeds %d byte limit", OutputLimit)
	}
	if reason == nil {
		reason = Decode(stdout.buffer.Bytes(), &empty)
	}
	if reason == nil && (!empty.OK && (empty.Error == nil || strings.TrimSpace(empty.Error.Code) == "" || strings.TrimSpace(empty.Error.Message) == "" || len(empty.Data) > 0) || empty.OK && (empty.Error != nil || len(empty.Data) == 0)) {
		reason = fmt.Errorf("invalid plugin result envelope")
	}
	if reason != nil {
		return Reply{}, diagnostic, &Failure{Code: "plugin_outcome_unknown", Message: reason.Error()}
	}
	if !empty.OK {
		return empty, diagnostic, empty.Error
	}
	return empty, diagnostic, nil
}
func terminate(cmd *exec.Cmd, waited <-chan error) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-waited:
	case <-timer.C:
		killGroup(cmd)
		<-waited
	}
}
