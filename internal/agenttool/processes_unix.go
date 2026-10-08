//go:build unix

package agenttool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const maxLiveProcesses = 4

type managedProcesses struct {
	mu        sync.Mutex
	processes map[string]*managedProcess
}

type managedProcess struct {
	id         string
	cwd        string
	stdoutPath string
	stderrPath string
	startedAt  time.Time
	maxChars   int
	cancel     context.CancelFunc
	started    chan struct{}
	done       chan struct{}
	mu         sync.Mutex
	result     outcome
	stdoutAt   int64
	stderrAt   int64
	reported   bool
}

type managedShellData struct {
	DecisionID string  `json:"decision_id,omitempty"`
	Status     string  `json:"status"`
	ProcessID  string  `json:"process_id"`
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

func newManagedProcesses() *managedProcesses {
	return &managedProcesses{processes: make(map[string]*managedProcess)}
}

func (m *managedProcesses) start(ctx context.Context, arguments []byte, env environment, yield time.Duration, maxChars int) outcome {
	m.mu.Lock()
	live := 0
	for _, process := range m.processes {
		select {
		case <-process.done:
		default:
			live++
		}
	}
	if live >= maxLiveProcesses {
		m.mu.Unlock()
		return outcome{Error: &ToolError{Code: "process_limit", Message: "too many running shell processes in this turn"}}
	}
	id, err := newProcessID()
	if err != nil {
		m.mu.Unlock()
		return outcome{Error: &ToolError{Code: "process_start_failed", Message: err.Error()}}
	}
	processCtx, cancel := context.WithCancel(ctx)
	prefix := artifactPrefix(env.callID)
	p := &managedProcess{
		id: id, cwd: env.workspace, startedAt: time.Now(), maxChars: maxChars, cancel: cancel,
		stdoutPath: filepath.Join(env.artifactsDir, prefix+".stdout"),
		stderrPath: filepath.Join(env.artifactsDir, prefix+".stderr"),
		started:    make(chan struct{}), done: make(chan struct{}),
	}
	m.processes[id] = p
	m.mu.Unlock()

	env.onShellStart = func() { close(p.started) }
	go func() {
		result := shellExecSyncHandler(processCtx, arguments, env)
		p.mu.Lock()
		p.result = result
		p.mu.Unlock()
		close(p.done)
		cancel()
	}()

	select {
	case <-p.started:
	case <-p.done:
		m.mu.Lock()
		delete(m.processes, id)
		m.mu.Unlock()
		return p.result
	case <-ctx.Done():
		cancel()
		<-p.done
		return p.result
	}
	result := p.wait(ctx, yield, maxChars)
	if data, ok := result.Data.(managedShellData); ok && data.Status == "completed" {
		m.mu.Lock()
		delete(m.processes, id)
		m.mu.Unlock()
		return p.result
	}
	return result
}

func newProcessID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return "proc_" + hex.EncodeToString(bytes[:]), nil
}

func (m *managedProcesses) get(id string) (*managedProcess, *ToolError) {
	m.mu.Lock()
	p := m.processes[id]
	m.mu.Unlock()
	if p == nil {
		return nil, &ToolError{Code: "process_not_found", Message: "process ID is unknown in this turn"}
	}
	return p, nil
}

func (m *managedProcesses) wait(ctx context.Context, id string, duration time.Duration, maxChars int) outcome {
	p, err := m.get(id)
	if err != nil {
		return outcome{Error: err}
	}
	if maxChars == 0 {
		maxChars = p.maxChars
	}
	return p.wait(ctx, duration, maxChars)
}

func (m *managedProcesses) cancel(ctx context.Context, id string, maxChars int) outcome {
	p, err := m.get(id)
	if err != nil {
		return outcome{Error: err}
	}
	if maxChars == 0 {
		maxChars = p.maxChars
	}
	select {
	case <-p.done:
	default:
		p.cancel()
	}
	return p.wait(ctx, 0, maxChars)
}

func shellWaitHandler(ctx context.Context, arguments json.RawMessage, env environment) outcome {
	var args shellWaitArgs
	if err := decodeStrict(arguments, &args); err != nil {
		return outcome{Error: err}
	}
	return env.processes.wait(ctx, args.ProcessID, time.Duration(args.WaitMS)*time.Millisecond, 0)
}

func shellCancelHandler(ctx context.Context, arguments json.RawMessage, env environment) outcome {
	var args shellCancelArgs
	if err := decodeStrict(arguments, &args); err != nil {
		return outcome{Error: err}
	}
	return env.processes.cancel(ctx, args.ProcessID, 0)
}

func (p *managedProcess) wait(ctx context.Context, duration time.Duration, maxChars int) outcome {
	if duration > 0 {
		timer := time.NewTimer(duration)
		defer timer.Stop()
		select {
		case <-p.done:
		case <-timer.C:
		case <-ctx.Done():
			return outcome{Error: &ToolError{Code: "cancelled", Message: ctx.Err().Error()}}
		}
	} else {
		select {
		case <-p.done:
		case <-ctx.Done():
			return outcome{Error: &ToolError{Code: "cancelled", Message: ctx.Err().Error()}}
		}
	}
	return p.snapshot(maxChars)
}

func (p *managedProcess) snapshot(maxChars int) outcome {
	p.mu.Lock()
	defer p.mu.Unlock()
	stdout, stdoutAt, stdoutCut, err := readNewOutput(p.stdoutPath, p.stdoutAt, maxChars*utf8.UTFMax/2)
	if err != nil {
		return outcome{Error: &ToolError{Code: "io_error", Message: err.Error()}}
	}
	stderr, stderrAt, stderrCut, err := readNewOutput(p.stderrPath, p.stderrAt, maxChars*utf8.UTFMax/2)
	if err != nil {
		return outcome{Error: &ToolError{Code: "io_error", Message: err.Error()}}
	}
	stdout, stderr, combinedCut := fitCombined(stdout, stderr, maxChars)
	p.stdoutAt, p.stderrAt = stdoutAt, stderrAt
	data := managedShellData{
		Status: "running", ProcessID: p.id, CWD: p.cwd,
		Stdout: stdout, Stderr: stderr, DurationMS: time.Since(p.startedAt).Milliseconds(),
		Truncated:  stdoutCut || stderrCut || combinedCut,
		StdoutPath: p.stdoutPath, StderrPath: p.stderrPath,
	}
	select {
	case <-p.done:
		if p.result.Error != nil || p.result.Fatal != nil {
			return p.result
		}
		final := p.result.Data.(shellExecData)
		data.Status = "completed"
		data.ExitCode, data.Signal = final.ExitCode, final.Signal
		data.DurationMS, data.TimedOut = final.DurationMS, final.TimedOut
		p.reported = true
	default:
	}
	return outcome{Data: data, Artifacts: []artifact{{Kind: "stdout", Path: p.stdoutPath}, {Kind: "stderr", Path: p.stderrPath}}}
}

func readNewOutput(path string, start int64, budget int) (string, int64, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", start, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", start, false, err
	}
	end := info.Size()
	if end < start {
		return "", start, false, errors.New("shell output shrank while process was running")
	}
	remaining := end - start
	if remaining == 0 {
		return "", end, false, nil
	}
	if remaining <= int64(budget) {
		data := make([]byte, remaining)
		_, err = file.ReadAt(data, start)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", start, false, err
		}
		return strings.ToValidUTF8(string(data), "�"), end, false, nil
	}
	headSize := int64(budget / 2)
	tailSize := int64(budget) - headSize
	head := make([]byte, headSize)
	tail := make([]byte, tailSize)
	if _, err := file.ReadAt(head, start); err != nil && !errors.Is(err, io.EOF) {
		return "", start, false, err
	}
	if _, err := file.ReadAt(tail, end-tailSize); err != nil && !errors.Is(err, io.EOF) {
		return "", start, false, err
	}
	return strings.ToValidUTF8(string(head), "�") + "\n… [output omitted] …\n" + strings.ToValidUTF8(string(tail), "�"), end, true, nil
}

func (m *managedProcesses) live() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.processes {
		select {
		case <-p.done:
		default:
			return true
		}
	}
	return false
}

func (m *managedProcesses) hasUnreported() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.processes {
		p.mu.Lock()
		unreported := !p.reported
		p.mu.Unlock()
		if unreported {
			return true
		}
	}
	return false
}

func (m *managedProcesses) collectUnreported(ctx context.Context) ([]json.RawMessage, error) {
	m.mu.Lock()
	processes := make([]*managedProcess, 0, len(m.processes))
	for _, p := range m.processes {
		processes = append(processes, p)
	}
	m.mu.Unlock()
	var results []json.RawMessage
	for _, p := range processes {
		p.mu.Lock()
		reported := p.reported
		p.mu.Unlock()
		if reported {
			continue
		}
		select {
		case <-p.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		result := p.snapshot(p.maxChars)
		encoded, err := marshalResponse(result)
		if err != nil {
			return nil, err
		}
		encoded, err = json.Marshal(struct {
			ProcessID string          `json:"process_id"`
			Result    json.RawMessage `json:"result"`
		}{ProcessID: p.id, Result: encoded})
		if err != nil {
			return nil, err
		}
		p.mu.Lock()
		p.reported = true
		p.mu.Unlock()
		results = append(results, encoded)
	}
	return results, nil
}

func (m *managedProcesses) close() {
	m.mu.Lock()
	processes := make([]*managedProcess, 0, len(m.processes))
	for _, p := range m.processes {
		processes = append(processes, p)
	}
	m.mu.Unlock()
	for _, p := range processes {
		p.cancel()
	}
	for _, p := range processes {
		<-p.done
	}
}
