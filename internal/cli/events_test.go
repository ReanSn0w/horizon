package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/ReanSn0w/horizon/internal/eventstream"
)

func TestJSONLEventPublisherKeepsEnvelopeAndNullCompactTurn(t *testing.T) {
	var stdout, stderr bytes.Buffer
	publish := newEventPublisher("jsonl", false, &stdout, &stderr)
	publish(eventstream.New("compaction_completed", "session-1", nil, map[string]any{"compacted": true}))
	if stderr.Len() != 0 {
		t.Fatalf("JSONL wrote text diagnostics: %q", stderr.String())
	}
	var event struct {
		Type      string          `json:"type"`
		Timestamp string          `json:"timestamp"`
		SessionID string          `json:"session_id"`
		TurnID    *string         `json:"turn_id"`
		Data      json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &event); err != nil {
		t.Fatalf("JSONL event is invalid: %v: %s", err, stdout.Bytes())
	}
	if event.Type != "compaction_completed" || event.Timestamp == "" || event.SessionID != "session-1" || event.TurnID != nil || !bytes.Contains(event.Data, []byte(`"compacted":true`)) {
		t.Fatalf("JSONL envelope = %+v data=%s", event, event.Data)
	}
}

func TestTextEventPublisherShowsTargetShellStatusAndVerboseLogs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	publish := newEventPublisher("text", true, &stdout, &stderr)
	turnID := "turn-1"
	publish(eventstream.New("tool_started", "session-1", &turnID, map[string]any{
		"name": "shell_exec", "arguments": json.RawMessage(`{"command":"go test ./..."}`),
	}))
	publish(eventstream.New("tool_completed", "session-1", &turnID, map[string]any{
		"name": "shell_exec", "duration_ms": int64(42),
		"result": json.RawMessage(`{"ok":true,"data":{"exit_code":1,"truncated":true,"stdout_path":"/tmp/stdout","stderr_path":"/tmp/stderr"}}`),
	}))
	if stdout.Len() != 0 {
		t.Fatalf("text progress leaked to stdout: %q", stdout.String())
	}
	for _, expected := range []string{"shell_exec: go test ./...", "готово", "exit 1", "результат сокращён", "42 ms", "/tmp/stdout", "/tmp/stderr"} {
		if !bytes.Contains(stderr.Bytes(), []byte(expected)) {
			t.Fatalf("text events miss %q: %s", expected, stderr.Bytes())
		}
	}
}

func TestTextEventPublisherFiltersModelStreamAndUsage(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		var out, diagnostics bytes.Buffer
		publish := newEventPublisher("text", verbose, &out, &diagnostics)
		publish(eventstream.New("progress", "s", nil, map[string]any{"text": "stream-sentinel"}))
		publish(eventstream.New("turn_completed", "s", nil, map[string]any{"usage": json.RawMessage(`{"total_tokens":12}`)}))
		if out.Len() != 0 || bytes.Contains(diagnostics.Bytes(), []byte("stream-sentinel")) ||
			bytes.Contains(diagnostics.Bytes(), []byte("usage:")) != verbose {
			t.Fatalf("verbose=%t stdout=%q stderr=%q", verbose, out.String(), diagnostics.String())
		}
	}
}

func TestOutputModes(t *testing.T) {
	for _, mode := range []string{"text", "plain", "jsonl"} {
		for _, verbose := range []bool{false, true} {
			var out, diagnostics bytes.Buffer
			publish := newEventPublisher(mode, verbose, &out, &diagnostics)
			for _, event := range []eventstream.Event{
				eventstream.New("turn_started", "s", nil, map[string]any{"model": "m"}),
				eventstream.New("progress", "s", nil, map[string]any{"text": "DELTA"}),
				eventstream.New("tool_started", "s", nil, map[string]any{"name": "file_read", "arguments": json.RawMessage(`{"path":"go.mod"}`)}),
				eventstream.New("tool_completed", "s", nil, map[string]any{"name": "file_read", "result": json.RawMessage(`{"ok":true,"data":{}}`), "duration_ms": 10}),
				eventstream.New("turn_completed", "s", nil, map[string]any{"text": "ANSWER", "usage": json.RawMessage(`{"total_tokens":12}`)}),
			} {
				publish(event)
			}
			switch mode {
			case "plain":
				if out.Len() != 0 || diagnostics.Len() != 0 {
					t.Fatalf("plain verbose=%t: %q %q", verbose, out.String(), diagnostics.String())
				}
			case "text":
				if out.Len() != 0 || !bytes.Contains(diagnostics.Bytes(), []byte("✓ file_read")) || bytes.Contains(diagnostics.Bytes(), []byte("DELTA")) || bytes.Contains(diagnostics.Bytes(), []byte("usage:")) != verbose {
					t.Fatalf("text: %q %q", out.String(), diagnostics.String())
				}
			case "jsonl":
				if diagnostics.Len() != 0 || !bytes.Contains(out.Bytes(), []byte("DELTA")) || !bytes.Contains(out.Bytes(), []byte("total_tokens")) {
					t.Fatalf("jsonl: %q %q", out.String(), diagnostics.String())
				}
			}
		}
	}
}

func TestFinalizeFailureDiagnostic(t *testing.T) {
	for _, mode := range []string{"text", "plain", "jsonl"} {
		var out, diagnostics bytes.Buffer
		publish := newEventPublisher(mode, false, &out, &diagnostics)
		publish(eventstream.New("plugin_finalize_failed", "s", nil, map[string]any{"plugin": "browser", "message": "offline"}))
		if mode == "jsonl" {
			if !bytes.Contains(out.Bytes(), []byte("plugin_finalize_failed")) || diagnostics.Len() != 0 {
				t.Fatalf("jsonl: %q %q", out.String(), diagnostics.String())
			}
		} else if out.Len() != 0 || !bytes.Contains(diagnostics.Bytes(), []byte("browser")) || !bytes.Contains(diagnostics.Bytes(), []byte("offline")) {
			t.Fatalf("%s: %q %q", mode, out.String(), diagnostics.String())
		}
	}
}
