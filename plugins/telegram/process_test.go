package main

import (
	"bytes"
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
)

func TestProcessCancellationKillsIgnoringTerm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	output, err := processRunner("/bin/sh", "")(ctx, "", []string{"-c", "printf %s $$ > '" + strings.ReplaceAll(path, "'", "'\"'\"'") + "'; trap '' TERM; exec sleep 30"}, "")
	if !errors.Is(err, context.DeadlineExceeded) || output != "" || time.Since(started) > 4*time.Second {
		t.Fatalf("cancellation: %v %q %s", err, output, time.Since(started))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("managed process survived cancellation: %v", err)
	}
}

func TestStreamedRunnerObservesBeforeExitAndFiltersContent(t *testing.T) {
	home := readyHome(t)
	gate := filepath.Join(t.TempDir(), "gate")
	script := filepath.Join(t.TempDir(), "script")
	code := "printf '%s\\n' '{\"type\":\"tool_started\",\"session_id\":\"s\",\"turn_id\":\"t\",\"data\":{\"name\":\"shell_exec\",\"call_id\":\"c\",\"arguments\":{\"command\":\"SECRET_COMMAND\"}}}'\nwhile [ ! -f '" + gate + "' ]; do sleep 0.01; done\nprintf '%s\\n' '{\"type\":\"progress\",\"data\":{\"text\":\"PRIVATE_PROGRESS\"}}' '{\"type\":\"turn_completed\",\"data\":{\"text\":\"FINAL\"}}'\n"
	if err := os.WriteFile(script, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	var journal bytes.Buffer
	g := &bridge{journal: &diagnosticLog{out: &journal, runID: "run"}}
	observed := 0
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := streamedRunner("/bin/sh", home, func(e childEvent) {
		observed++
		g.childDiagnostic(e)
		if e.Type == "tool_started" {
			if err := os.WriteFile(gate, nil, 0600); err != nil {
				t.Error(err)
			}
		}
	})(ctx, "", []string{script}, "")
	if err != nil || output != "FINAL" || observed != 3 {
		t.Fatal(output, err, observed)
	}
	if strings.Contains(journal.String(), "SECRET_COMMAND") || strings.Contains(journal.String(), "PRIVATE_PROGRESS") || strings.Contains(journal.String(), "FINAL") {
		t.Fatal(journal.String())
	}
}

func TestChildDiagnosticLogsOnlyNumericUsage(t *testing.T) {
	var journal bytes.Buffer
	g := &bridge{journal: &diagnosticLog{out: &journal, runID: "run"}}
	g.childDiagnostic(childEvent{Type: "turn_completed", Session: "session", Data: map[string]json.RawMessage{
		"text":  json.RawMessage(`"PRIVATE_ANSWER"`),
		"usage": json.RawMessage(`{"input_tokens":123,"output_tokens":7,"private":"SECRET_USAGE"}`),
	}})
	output := journal.String()
	if !strings.Contains(output, `"input_tokens":123`) || !strings.Contains(output, `"output_tokens":7`) {
		t.Fatalf("usage counters missing: %s", output)
	}
	if strings.Contains(output, "PRIVATE_ANSWER") || strings.Contains(output, "SECRET_USAGE") {
		t.Fatalf("private data entered journal: %s", output)
	}
}

func TestStreamedRunnerRejectsInvalidCompletion(t *testing.T) {
	for _, tc := range []struct{ name, script string }{
		{"malformed", "printf 'invalid\\n'; sleep 30"},
		{"truncated", `printf '{"type":"turn_completed","data":{"text":"answer"}}'`},
		{"nonzero", `printf '%s\n' '{"type":"turn_completed","data":{"text":"answer"}}'; printf 'test-token unused CLI failure' >&2; exit 2`},
		{"missing", `printf '%s\n' '{"type":"future_event","data":{}}'`},
		{"duplicate", `printf '%s\n' '{"type":"turn_completed","data":{"text":"answer"}}' '{"type":"turn_completed","data":{"text":"answer"}}'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			out, err := streamedRunner("/bin/sh", readyHome(t), nil)(ctx, "", []string{"-c", tc.script}, "")
			if err == nil || out != "" || strings.Contains(err.Error(), "test-token") || strings.Contains(err.Error(), "unused") {
				t.Fatal(out, err)
			}
		})
	}
}

func TestEventCollectorSizeLimit(t *testing.T) {
	cancelled := false
	w := &eventCollector{cancel: func() { cancelled = true }}
	if _, err := w.Write(bytes.Repeat([]byte("x"), 4*1024*1024+1)); err == nil || !cancelled {
		t.Fatal("unbounded event")
	}
}

func TestStreamedRunnerDrainsLargeStderr(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	code := `i=0; while [ "$i" -lt 10000 ]; do printf 'stderr-payload\n' >&2; i=$((i+1)); done; printf '%s\n' '{"type":"turn_completed","data":{"text":"answer"}}'`
	out, err := streamedRunner("/bin/sh", "", nil)(ctx, "", []string{"-c", code}, "")
	if err == nil || errors.Is(err, context.DeadlineExceeded) || out != "" {
		t.Fatal("stderr was not bounded and drained", out, err)
	}
}
