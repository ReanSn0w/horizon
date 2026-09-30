//go:build unix

package agenttool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

type shellExecData struct {
	CWD        string  `json:"cwd"`
	ExitCode   *int    `json:"exit_code"`
	Signal     *string `json:"signal"`
	Stdout     string  `json:"stdout"`
	Stderr     string  `json:"stderr"`
	DurationMS int64   `json:"duration_ms"`
	TimedOut   bool    `json:"timed_out"`
	Truncated  bool    `json:"truncated"`
	StdoutPath string  `json:"stdout_path"`
	StderrPath string  `json:"stderr_path"`
}

var shellExecutable = "/bin/sh"

func shellExecHandler(ctx context.Context, arguments json.RawMessage, env environment) outcome {
	var args shellExecArgs
	if err := decodeStrict(arguments, &args); err != nil {
		return outcome{Error: err}
	}
	maxChars := 16000
	if args.MaxOutputChars != nil {
		maxChars = *args.MaxOutputChars
	}
	if err := os.MkdirAll(env.artifactsDir, 0o700); err != nil {
		return outcome{Error: filesystemError(err, env.artifactsDir)}
	}
	prefix := artifactPrefix(env.callID)
	stdoutPath := filepath.Join(env.artifactsDir, prefix+".stdout")
	stderrPath := filepath.Join(env.artifactsDir, prefix+".stderr")
	stdoutFile, err := os.OpenFile(stdoutPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return outcome{Error: filesystemError(err, stdoutPath)}
	}
	stderrFile, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = stdoutFile.Close()
		_ = os.Remove(stdoutPath)
		return outcome{Error: filesystemError(err, stderrPath)}
	}
	cleanup := func() {
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		_ = os.Remove(stdoutPath)
		_ = os.Remove(stderrPath)
	}

	byteBudget := maxChars * utf8.UTFMax
	stdoutEdge := newEdgeBuffer(byteBudget)
	stderrEdge := newEdgeBuffer(byteBudget)
	command := exec.Command(shellExecutable, "-c", args.Command)
	command.Dir = env.workspace
	command.Stdin = nil
	command.Stdout = io.MultiWriter(stdoutFile, stdoutEdge)
	command.Stderr = io.MultiWriter(stderrFile, stderrEdge)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	startedAt := time.Now()
	if err := command.Start(); err != nil {
		cleanup()
		return outcome{Error: &ToolError{Code: "process_start_failed", Message: err.Error(), Details: map[string]any{"cwd": env.workspace}}}
	}

	waitChannel := make(chan error, 1)
	go func() { waitChannel <- command.Wait() }()
	var timer *time.Timer
	var timeout <-chan time.Time
	if args.TimeoutMS != nil {
		timer = time.NewTimer(time.Duration(*args.TimeoutMS) * time.Millisecond)
		timeout = timer.C
		defer timer.Stop()
	}
	timedOut := false
	var waitErr error
	select {
	case waitErr = <-waitChannel:
	case <-ctx.Done():
		waitErr = terminateProcessGroup(command, waitChannel)
	case <-timeout:
		timedOut = true
		waitErr = terminateProcessGroup(command, waitChannel)
	}
	duration := time.Since(startedAt)

	artifacts := []artifact{{Kind: "stdout", Path: stdoutPath}, {Kind: "stderr", Path: stderrPath}}
	if err := syncAndClose(stdoutFile, stderrFile); err != nil {
		return outcome{Error: &ToolError{Code: "io_error", Message: fmt.Sprintf("save shell output: %v", err), Details: map[string]any{"stdout_path": stdoutPath, "stderr_path": stderrPath}}, Artifacts: artifacts}
	}
	var exitError *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitError) {
		return outcome{Error: &ToolError{Code: "io_error", Message: fmt.Sprintf("wait for shell command: %v", waitErr)}, Artifacts: artifacts}
	}

	stdout, stdoutCut := stdoutEdge.Text()
	stderr, stderrCut := stderrEdge.Text()
	stdout, stderr, charCut := fitCombined(stdout, stderr, maxChars)
	data := shellExecData{
		CWD: env.workspace, Stdout: stdout, Stderr: stderr,
		DurationMS: duration.Milliseconds(), TimedOut: timedOut,
		Truncated:  stdoutCut || stderrCut || charCut,
		StdoutPath: stdoutPath, StderrPath: stderrPath,
	}
	if command.ProcessState != nil {
		status := command.ProcessState.Sys().(syscall.WaitStatus)
		if status.Signaled() {
			signal := status.Signal().String()
			data.Signal = &signal
		} else {
			exitCode := status.ExitStatus()
			data.ExitCode = &exitCode
		}
	}
	return outcome{Data: data, Artifacts: artifacts}
}

func terminateProcessGroup(command *exec.Cmd, waitChannel <-chan error) error {
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case err := <-waitChannel:
		return err
	case <-timer.C:
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		return <-waitChannel
	}
}

func syncAndClose(files ...*os.File) error {
	var result error
	for _, file := range files {
		if err := file.Sync(); err != nil && result == nil {
			result = err
		}
		if err := file.Close(); err != nil && result == nil {
			result = err
		}
	}
	return result
}

func artifactPrefix(callID string) string {
	sum := sha256.Sum256([]byte(callID))
	return hex.EncodeToString(sum[:16])
}

type edgeBuffer struct {
	mu       sync.Mutex
	limit    int
	headSize int
	tailSize int
	total    int64
	head     []byte
	tail     []byte
}

func newEdgeBuffer(limit int) *edgeBuffer {
	return &edgeBuffer{limit: limit, headSize: limit / 2, tailSize: limit - limit/2}
}

func (buffer *edgeBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	originalLength := len(data)
	buffer.total += int64(originalLength)
	if len(buffer.head) < buffer.headSize {
		count := buffer.headSize - len(buffer.head)
		if count > len(data) {
			count = len(data)
		}
		buffer.head = append(buffer.head, data[:count]...)
		data = data[count:]
	}
	if len(data) > 0 {
		buffer.tail = append(buffer.tail, data...)
		if len(buffer.tail) > buffer.tailSize {
			buffer.tail = append([]byte(nil), buffer.tail[len(buffer.tail)-buffer.tailSize:]...)
		}
	}
	return originalLength, nil
}

func (buffer *edgeBuffer) Text() (string, bool) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	truncated := buffer.total > int64(buffer.limit)
	data := append(append([]byte(nil), buffer.head...), buffer.tail...)
	text := strings.ToValidUTF8(string(data), "�")
	if truncated {
		text = strings.ToValidUTF8(string(buffer.head), "�") + "\n… [output omitted] …\n" + strings.ToValidUTF8(string(buffer.tail), "�")
	}
	return text, truncated
}

func fitCombined(stdout, stderr string, limit int) (string, string, bool) {
	stdoutRunes, stderrRunes := []rune(stdout), []rune(stderr)
	if len(stdoutRunes)+len(stderrRunes) <= limit {
		return stdout, stderr, false
	}
	stdoutBudget := min(len(stdoutRunes), limit/2)
	stderrBudget := min(len(stderrRunes), limit/2)
	remaining := limit - stdoutBudget - stderrBudget
	extraStdout := min(len(stdoutRunes)-stdoutBudget, remaining)
	stdoutBudget += extraStdout
	remaining -= extraStdout
	stderrBudget += min(len(stderrRunes)-stderrBudget, remaining)
	return truncateRunes(stdoutRunes, stdoutBudget), truncateRunes(stderrRunes, stderrBudget), true
}

func truncateRunes(value []rune, limit int) string {
	if len(value) <= limit {
		return string(value)
	}
	marker := []rune("… [truncated] …")
	if limit <= len(marker) {
		return string(marker[:limit])
	}
	available := limit - len(marker)
	head := available / 2
	tail := available - head
	return string(value[:head]) + string(marker) + string(value[len(value)-tail:])
}
