//go:build unix

package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ReanSn0w/horizon/internal/decision"
	"github.com/ReanSn0w/horizon/internal/session"
)

type reviewStub struct {
	verdict decision.Verdict
	err     error
}

func (r reviewStub) Review(context.Context, decision.Command) (decision.Verdict, error) {
	return r.verdict, r.err
}

type forbiddenReviewer struct{ calls int }

type countingReviewer struct{ calls int }

func (r *countingReviewer) Review(context.Context, decision.Command) (decision.Verdict, error) {
	r.calls++
	return decision.Verdict{Allowed: true, ID: "managed-decision"}, nil
}

func (r *forbiddenReviewer) Review(context.Context, decision.Command) (decision.Verdict, error) {
	r.calls++
	return decision.Verdict{}, errors.New("full mode must not call Jev")
}

func TestExecutorFullRunsWithoutJev(t *testing.T) {
	store := session.NewStore(t.TempDir())
	workspace, err := store.ResolveWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(workspace)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := store.LockSession(workspace, created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	turnID := strings.Repeat("c", 32)
	if err := locked.StartTurn(session.Turn{ID: turnID, Status: session.StatusActive, StartedAt: time.Now().UTC(), Model: session.ModelProfile{Name: "test", Model: "test", CompactThreshold: 1000}}); err != nil {
		t.Fatal(err)
	}
	reviewer := &forbiddenReviewer{}
	for index, candidate := range []decision.Reviewer{reviewer, nil} {
		executor := NewExecutor(workspace.Dir, store.ArtifactsDir(workspace, created.SessionID), locked, turnID)
		executor.SetHome(store.Home)
		executor.SetCommandReview("full", candidate)
		output, err := executor.Execute(context.Background(), "full-"+strconv.Itoa(index), ShellExec, json.RawMessage(`{"command":"printf full-mode","timeout_ms":null,"max_output_chars":null}`))
		if err != nil {
			t.Fatal(err)
		}
		var response struct {
			OK   bool `json:"ok"`
			Data struct {
				Stdout     string `json:"stdout"`
				DecisionID string `json:"decision_id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(output, &response); err != nil || !response.OK || response.Data.Stdout != "full-mode" || response.Data.DecisionID != "" {
			t.Fatalf("result=%s err=%v", output, err)
		}
	}
	if reviewer.calls != 0 {
		t.Fatalf("Jev reviewer called %d times", reviewer.calls)
	}
}

func TestExecutorManagedShellReviewsOnlyLaunchAndJournalsWait(t *testing.T) {
	store := session.NewStore(t.TempDir())
	workspace, err := store.ResolveWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(workspace)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := store.LockSession(workspace, created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	turnID := strings.Repeat("a", 32)
	if err := locked.StartTurn(session.Turn{ID: turnID, Status: session.StatusActive, StartedAt: time.Now().UTC(), Model: session.ModelProfile{Name: "test", Model: "test", CompactThreshold: 1000}}); err != nil {
		t.Fatal(err)
	}
	executor := NewExecutor(workspace.Dir, store.ArtifactsDir(workspace, created.SessionID), locked, turnID)
	defer executor.Close()
	reviewer := &countingReviewer{}
	executor.SetHome(store.Home)
	executor.SetCommandReview("write", reviewer)
	started, err := executor.Execute(context.Background(), "start", ShellExec, json.RawMessage(`{"command":"sleep 1; printf done","timeout_ms":null,"max_output_chars":null,"yield_time_ms":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var first struct {
		OK   bool             `json:"ok"`
		Data managedShellData `json:"data"`
	}
	if err := json.Unmarshal(started, &first); err != nil || !first.OK || first.Data.Status != "running" || first.Data.DecisionID != "managed-decision" {
		t.Fatalf("start = %s, %v", started, err)
	}
	waitArgs, _ := json.Marshal(shellWaitArgs{ProcessID: first.Data.ProcessID, WaitMS: 2000})
	finished, err := executor.Execute(context.Background(), "wait", ShellWait, waitArgs)
	if err != nil {
		t.Fatal(err)
	}
	var final struct {
		OK   bool             `json:"ok"`
		Data managedShellData `json:"data"`
	}
	if err := json.Unmarshal(finished, &final); err != nil || !final.OK || final.Data.Status != "completed" || final.Data.Stdout != "done" || reviewer.calls != 1 {
		t.Fatalf("wait=%s reviews=%d err=%v", finished, reviewer.calls, err)
	}
	value, err := locked.Load()
	if err != nil || len(value.Turns[0].ToolCalls) != 2 || value.Turns[0].ToolCalls[0].ResultState != session.ToolResultKnown || value.Turns[0].ToolCalls[1].ResultState != session.ToolResultKnown {
		t.Fatalf("journal = %+v, %v", value, err)
	}
}

func TestExecutorDoesNotRunDeniedOrUnavailableCommand(t *testing.T) {
	store := session.NewStore(t.TempDir())
	workspace, err := store.ResolveWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(workspace)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := store.LockSession(workspace, created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	turnID := strings.Repeat("d", 32)
	if err := locked.StartTurn(session.Turn{ID: turnID, Status: session.StatusActive, StartedAt: time.Now().UTC(), Model: session.ModelProfile{Name: "test", Model: "test", CompactThreshold: 1000}}); err != nil {
		t.Fatal(err)
	}
	command := json.RawMessage(`{"command":"touch blocked","timeout_ms":null,"max_output_chars":null}`)
	for _, test := range []struct {
		name     string
		reviewer decision.Reviewer
		code     string
	}{
		{"denied", reviewStub{verdict: decision.Verdict{Reason: "not allowed", ID: "decision-1"}}, "decision_denied"},
		{"failed", reviewStub{err: errors.New("provider unavailable")}, "decision_unavailable"},
		{"missing", nil, "decision_unavailable"},
	} {
		executor := NewExecutor(workspace.Dir, store.ArtifactsDir(workspace, created.SessionID), locked, turnID)
		executor.SetHome(store.Home)
		executor.SetCommandReview("write", test.reviewer)
		output, err := executor.Execute(context.Background(), test.name, ShellExec, command)
		if err != nil {
			t.Fatal(err)
		}
		var response Response
		if err := json.Unmarshal(output, &response); err != nil || response.OK || response.Error == nil || response.Error.Code != test.code {
			t.Fatalf("%s: %s %v", test.name, output, err)
		}
		if _, err := os.Stat(filepath.Join(workspace.Dir, "blocked")); !os.IsNotExist(err) {
			t.Fatalf("%s executed command: %v", test.name, err)
		}
	}
}

func TestExecutorPersistsValidationResultAndStopsBeforeUnrecordedEffect(t *testing.T) {
	store := session.NewStore(t.TempDir())
	workspace, err := store.ResolveWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(workspace)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := store.LockSession(workspace, created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	turnID := strings.Repeat("f", 32)
	at := time.Now().UTC()
	if err := locked.StartTurn(session.Turn{ID: turnID, Status: session.StatusActive, StartedAt: at, Model: session.ModelProfile{Name: "test", Model: "test", CompactThreshold: 1000}}); err != nil {
		t.Fatal(err)
	}
	executor := NewExecutor(workspace.Dir, store.ArtifactsDir(workspace, created.SessionID), locked, turnID)
	result, err := executor.Execute(context.Background(), "bad-call", ShellExec, json.RawMessage(`{"command":"touch invalid.txt","timeout_ms":null,"max_output_chars":null,"extra":true}`))
	if err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := json.Unmarshal(result, &response); err != nil {
		t.Fatal(err)
	}
	if response.OK || response.Error == nil || response.Error.Code != "invalid_arguments" {
		t.Fatalf("invalid tool response = %s", result)
	}
	value, err := locked.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Turns[0].ToolCalls) != 1 || value.Turns[0].ToolCalls[0].ResultState != session.ToolResultKnown {
		t.Fatalf("persisted invalid call = %+v", value.Turns[0].ToolCalls)
	}
	if _, err := os.Stat(filepath.Join(workspace.Dir, "invalid.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid call changed filesystem: %v", err)
	}
	if err := locked.Close(); err != nil {
		t.Fatal(err)
	}

	executor = NewExecutor(workspace.Dir, store.ArtifactsDir(workspace, created.SessionID), locked, turnID)
	if _, err := executor.Execute(context.Background(), "unrecorded", ShellExec, json.RawMessage(`{"command":"touch never.txt","timeout_ms":null,"max_output_chars":null}`)); err == nil || !strings.Contains(err.Error(), "record tool call") {
		t.Fatalf("execute with closed session lock error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace.Dir, "never.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unrecorded call changed filesystem: %v", err)
	}
}

func TestExecutorRejectsRemovedFileTool(t *testing.T) {
	store := session.NewStore(t.TempDir())
	workspace, err := store.ResolveWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(workspace)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := store.LockSession(workspace, created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	turnID := strings.Repeat("a", 32)
	if err := locked.StartTurn(session.Turn{ID: turnID, Status: session.StatusActive, StartedAt: time.Now().UTC(), Model: session.ModelProfile{Name: "test", Model: "test", CompactThreshold: 1000}}); err != nil {
		t.Fatal(err)
	}
	executor := NewExecutor(workspace.Dir, store.ArtifactsDir(workspace, created.SessionID), locked, turnID)
	output, err := executor.Execute(context.Background(), "legacy", "file_create", json.RawMessage(`{"path":"marker","content":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := json.Unmarshal(output, &response); err != nil || response.OK || response.Error == nil || response.Error.Code != "unknown_tool" {
		t.Fatalf("removed tool result = %s: %v", output, err)
	}
	if _, err := os.Stat(filepath.Join(workspace.Dir, "marker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed tool changed filesystem: %v", err)
	}
}

func TestShellExecLargeStreamsExitCodeAndRawArtifacts(t *testing.T) {
	workspace := t.TempDir()
	artifacts := filepath.Join(t.TempDir(), "artifacts")
	args := shellArgs("i=0; while [ $i -lt 2000 ]; do printf o; printf e >&2; i=$((i+1)); done; exit 7", nil, intPointer(1000))
	result := shellExecHandler(context.Background(), args, environment{workspace: workspace, artifactsDir: artifacts, callID: "large"})
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	data := result.Data.(shellExecData)
	if data.ExitCode == nil || *data.ExitCode != 7 || data.Signal != nil || !data.Truncated || data.TimedOut {
		t.Fatalf("shell result = %+v", data)
	}
	if len([]rune(data.Stdout))+len([]rune(data.Stderr)) > 1000 {
		t.Fatalf("combined output contains %d runes", len([]rune(data.Stdout))+len([]rune(data.Stderr)))
	}
	stdout, err := os.ReadFile(data.StdoutPath)
	if err != nil || len(stdout) != 2000 {
		t.Fatalf("stdout artifact length=%d error=%v", len(stdout), err)
	}
	stderr, err := os.ReadFile(data.StderrPath)
	if err != nil || len(stderr) != 2000 {
		t.Fatalf("stderr artifact length=%d error=%v", len(stderr), err)
	}

	invalid := shellExecHandler(context.Background(), shellArgs("printf '\\377A'", nil, intPointer(1000)), environment{workspace: workspace, artifactsDir: artifacts, callID: "invalid-utf8"})
	if invalid.Error != nil {
		t.Fatal(invalid.Error)
	}
	invalidData := invalid.Data.(shellExecData)
	if !strings.Contains(invalidData.Stdout, "�A") {
		t.Fatalf("sanitized stdout = %q", invalidData.Stdout)
	}
	raw, err := os.ReadFile(invalidData.StdoutPath)
	if err != nil || len(raw) != 2 || raw[0] != 0xff {
		t.Fatalf("raw stdout = %v, %v", raw, err)
	}
}

func TestShellExecInheritsPATHAndProcessEnvironment(t *testing.T) {
	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "swag"), []byte("#!/bin/sh\nprintf 'swag-ready\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PROJECT_TOOL_TEST", "inherited")
	home := filepath.Join(t.TempDir(), "selected-home")
	result := shellExecHandler(context.Background(), shellArgs("swag; printf '%s\\n' \"$PROJECT_TOOL_TEST\"; printf '%s\\n' \"$HORIZON_HOME\"", nil, intPointer(2000)), environment{
		workspace: t.TempDir(), artifactsDir: filepath.Join(t.TempDir(), "artifacts"), callID: "inherited-env", home: home,
	})
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	data := result.Data.(shellExecData)
	if data.ExitCode == nil || *data.ExitCode != 0 || data.Stdout != "swag-ready\ninherited\n"+home+"\n" {
		t.Fatalf("shell result = %+v", data)
	}
}

func TestShellExecReplacesInheritedAccessForNestedHorizon(t *testing.T) {
	t.Setenv("HORIZON_INHERITED_ACCESS", "full")
	workspace := t.TempDir()
	home := t.TempDir()
	result := shellExecHandler(context.Background(), shellArgs("printf '%s' \"$HORIZON_INHERITED_ACCESS:$HORIZON_HOME\"", nil, nil), environment{
		workspace: workspace, artifactsDir: filepath.Join(t.TempDir(), "artifacts"), callID: "nested", home: home, access: "read",
	})
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	if got := result.Data.(shellExecData).Stdout; got != "read:"+home {
		t.Fatalf("inherited access and home = %q", got)
	}
}

func TestShellStartFailureIsDifferentFromExitCode(t *testing.T) {
	previous := shellExecutable
	shellExecutable = filepath.Join(t.TempDir(), "missing-shell")
	defer func() { shellExecutable = previous }()
	result := shellExecHandler(context.Background(), shellArgs("exit 7", nil, intPointer(1000)), environment{workspace: t.TempDir(), artifactsDir: filepath.Join(t.TempDir(), "artifacts"), callID: "start-failure"})
	if result.Error == nil || result.Error.Code != "process_start_failed" {
		t.Fatalf("start failure = %+v", result)
	}
}

func TestShellExecWorkspaceIsolationTimeoutAndCancellation(t *testing.T) {
	workspace := t.TempDir()
	artifacts := filepath.Join(t.TempDir(), "artifacts")
	first := shellExecHandler(context.Background(), shellArgs("cd /; export HORIZON_TOOL_TEST=value; pwd", nil, intPointer(2000)), environment{workspace: workspace, artifactsDir: artifacts, callID: "first"})
	if first.Error != nil {
		t.Fatal(first.Error)
	}
	second := shellExecHandler(context.Background(), shellArgs("pwd; printf '%s' \"$HORIZON_TOOL_TEST\"", nil, intPointer(2000)), environment{workspace: workspace, artifactsDir: artifacts, callID: "second"})
	if second.Error != nil {
		t.Fatal(second.Error)
	}
	secondData := second.Data.(shellExecData)
	if secondData.Stdout != workspace+"\n" || secondData.CWD != workspace {
		t.Fatalf("second shell stdout=%q cwd=%q", secondData.Stdout, secondData.CWD)
	}

	timeoutMS := 100
	timed := shellExecHandler(context.Background(), shellArgs("trap '' TERM; sleep 10 & echo $! > child.pid; wait", &timeoutMS, intPointer(2000)), environment{workspace: workspace, artifactsDir: artifacts, callID: "timeout"})
	if timed.Error != nil {
		t.Fatal(timed.Error)
	}
	timedData := timed.Data.(shellExecData)
	if !timedData.TimedOut || timedData.Signal == nil || timedData.DurationMS < 1900 || timedData.DurationMS > 4000 {
		t.Fatalf("timed shell result = %+v", timedData)
	}
	pidBytes, err := os.ReadFile(filepath.Join(workspace, "child.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("child process %d survived timeout", pid)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	cancelled := shellExecHandler(ctx, shellArgs("sleep 10", nil, intPointer(2000)), environment{workspace: workspace, artifactsDir: artifacts, callID: "cancel"})
	if cancelled.Error != nil {
		t.Fatal(cancelled.Error)
	}
	cancelledData := cancelled.Data.(shellExecData)
	if cancelledData.TimedOut || cancelledData.Signal == nil {
		t.Fatalf("cancelled shell result = %+v", cancelledData)
	}
}

func shellArgs(command string, timeout, max *int) json.RawMessage {
	data, err := json.Marshal(shellExecArgs{Command: command, TimeoutMS: timeout, MaxOutputChars: max})
	if err != nil {
		panic(err)
	}
	return data
}

func intPointer(value int) *int { return &value }
