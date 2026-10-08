//go:build unix

package agenttool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestManagedProcessYieldsAndReturnsFinalOutput(t *testing.T) {
	manager := newManagedProcesses()
	defer manager.close()
	dir := t.TempDir()
	args := json.RawMessage(`{"command":"printf first; sleep 1; printf second","timeout_ms":null,"max_output_chars":null}`)
	started := manager.start(context.Background(), args, environment{workspace: dir, artifactsDir: filepath.Join(dir, "artifacts"), callID: "call-1"}, 10*time.Millisecond, 16000)
	first, ok := started.Data.(managedShellData)
	if !ok || first.Status != "running" || first.ProcessID == "" {
		t.Fatalf("start = %+v", started)
	}
	finished := manager.wait(context.Background(), first.ProcessID, 3*time.Second, 16000)
	final, ok := finished.Data.(managedShellData)
	if !ok || final.Status != "completed" || final.ExitCode == nil || *final.ExitCode != 0 {
		t.Fatalf("wait = %+v", finished)
	}
	if first.Stdout+final.Stdout != "firstsecond" {
		t.Fatalf("output chunks = %q + %q", first.Stdout, final.Stdout)
	}
	contents, err := os.ReadFile(final.StdoutPath)
	if err != nil || string(contents) != "firstsecond" {
		t.Fatalf("artifact = %q, %v", contents, err)
	}
}

func TestManagedProcessFastExitKeepsLegacyResult(t *testing.T) {
	manager := newManagedProcesses()
	defer manager.close()
	dir := t.TempDir()
	result := manager.start(context.Background(), json.RawMessage(`{"command":"printf fast; exit 7","timeout_ms":null,"max_output_chars":null}`), environment{workspace: dir, artifactsDir: filepath.Join(dir, "artifacts"), callID: "fast"}, time.Second, 16000)
	data, ok := result.Data.(shellExecData)
	if !ok || data.Stdout != "fast" || data.ExitCode == nil || *data.ExitCode != 7 || manager.hasUnreported() {
		t.Fatalf("fast result = %+v", result)
	}
}

func TestManagedProcessWaitAndCancelAreIdempotent(t *testing.T) {
	manager := newManagedProcesses()
	defer manager.close()
	dir := t.TempDir()
	args := json.RawMessage(`{"command":"printf before; sleep 1; printf after","timeout_ms":null,"max_output_chars":null}`)
	started := manager.start(context.Background(), args, environment{workspace: dir, artifactsDir: filepath.Join(dir, "artifacts"), callID: "call-wait"}, time.Millisecond, 16000)
	first := started.Data.(managedShellData)
	middle := manager.wait(context.Background(), first.ProcessID, time.Millisecond, 0)
	progress := middle.Data.(managedShellData)
	if progress.Status != "running" {
		t.Fatalf("early wait = %+v", progress)
	}
	finished := manager.wait(context.Background(), first.ProcessID, 3*time.Second, 0)
	final := finished.Data.(managedShellData)
	if final.Status != "completed" || first.Stdout+progress.Stdout+final.Stdout != "beforeafter" {
		t.Fatalf("chunks = %q + %q + %q, final=%+v", first.Stdout, progress.Stdout, final.Stdout, final)
	}
	for _, repeated := range []outcome{
		manager.wait(context.Background(), first.ProcessID, time.Millisecond, 0),
		manager.cancel(context.Background(), first.ProcessID, 0),
	} {
		data := repeated.Data.(managedShellData)
		if data.Status != "completed" || data.Stdout != "" || data.Stderr != "" {
			t.Fatalf("repeated result = %+v", data)
		}
	}
}

func TestManagedProcessLimitAndUnknownID(t *testing.T) {
	manager := newManagedProcesses()
	defer manager.close()
	dir := t.TempDir()
	args := json.RawMessage(`{"command":"sleep 5","timeout_ms":null,"max_output_chars":null}`)
	for index := 0; index < maxLiveProcesses; index++ {
		result := manager.start(context.Background(), args, environment{workspace: dir, artifactsDir: filepath.Join(dir, "artifacts"), callID: "limit-" + string(rune('a'+index))}, time.Millisecond, 16000)
		if data, ok := result.Data.(managedShellData); !ok || data.Status != "running" {
			t.Fatalf("start %d = %+v", index, result)
		}
	}
	blocked := manager.start(context.Background(), args, environment{workspace: dir, artifactsDir: filepath.Join(dir, "artifacts"), callID: "limit-extra"}, time.Millisecond, 16000)
	if blocked.Error == nil || blocked.Error.Code != "process_limit" {
		t.Fatalf("limit = %+v", blocked)
	}
	unknown := manager.wait(context.Background(), "proc_other_turn", time.Millisecond, 0)
	if unknown.Error == nil || unknown.Error.Code != "process_not_found" {
		t.Fatalf("unknown ID = %+v", unknown)
	}
}

func TestManagedProcessTimeoutAndExplicitCancel(t *testing.T) {
	for _, test := range []struct {
		name    string
		args    json.RawMessage
		cancel  bool
		timeout bool
	}{
		{"timeout", json.RawMessage(`{"command":"sleep 5","timeout_ms":50,"max_output_chars":null}`), false, true},
		{"cancel", json.RawMessage(`{"command":"sleep 5","timeout_ms":null,"max_output_chars":null}`), true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := newManagedProcesses()
			defer manager.close()
			dir := t.TempDir()
			started := manager.start(context.Background(), test.args, environment{workspace: dir, artifactsDir: filepath.Join(dir, "artifacts"), callID: test.name}, time.Millisecond, 16000)
			first := started.Data.(managedShellData)
			var result outcome
			if test.cancel {
				result = manager.cancel(context.Background(), first.ProcessID, 0)
			} else {
				result = manager.wait(context.Background(), first.ProcessID, time.Second, 0)
			}
			final := result.Data.(managedShellData)
			if final.Status != "completed" || final.TimedOut != test.timeout || final.Signal == nil {
				t.Fatalf("final = %+v", final)
			}
		})
	}
}

func TestManagedProcessConcurrentWaitAndCancel(t *testing.T) {
	manager := newManagedProcesses()
	defer manager.close()
	dir := t.TempDir()
	started := manager.start(context.Background(), json.RawMessage(`{"command":"sleep 5","timeout_ms":null,"max_output_chars":null}`), environment{workspace: dir, artifactsDir: filepath.Join(dir, "artifacts"), callID: "concurrent"}, time.Millisecond, 16000)
	id := started.Data.(managedShellData).ProcessID
	results := make(chan outcome, 2)
	go func() { results <- manager.wait(context.Background(), id, 2*time.Second, 0) }()
	go func() { results <- manager.cancel(context.Background(), id, 0) }()
	for range 2 {
		result := <-results
		data, ok := result.Data.(managedShellData)
		if !ok || data.Status != "completed" || data.Signal == nil {
			t.Fatalf("concurrent result = %+v", result)
		}
	}
}
