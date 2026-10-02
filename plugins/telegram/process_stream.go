package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

type childEvent struct {
	Type    string                     `json:"type"`
	Session string                     `json:"session_id"`
	Turn    *string                    `json:"turn_id"`
	Data    map[string]json.RawMessage `json:"data"`
}

type eventCollector struct {
	buffer    []byte
	text      string
	completed bool
	err       error
	observe   func(childEvent)
	cancel    context.CancelFunc
}

func (w *eventCollector) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		if end < 0 {
			end = len(p)
		}
		if len(w.buffer)+end > 4*1024*1024 {
			return 0, w.fail(errors.New("Horizon event exceeds the telegram limit"))
		}
		w.buffer = append(w.buffer, p[:end]...)
		if end == len(p) {
			break
		}
		if err := w.line(w.buffer); err != nil {
			return 0, w.fail(err)
		}
		w.buffer = w.buffer[:0]
		p = p[end+1:]
	}
	return n, nil
}
func (w *eventCollector) fail(err error) error { w.err = err; w.cancel(); return err }
func (w *eventCollector) line(data []byte) error {
	var e childEvent
	if json.Unmarshal(data, &e) != nil || e.Type == "" {
		return errors.New("invalid Horizon event stream")
	}
	if e.Type == "turn_completed" {
		if w.completed {
			return errors.New("duplicate Horizon completion")
		}
		if json.Unmarshal(e.Data["text"], &w.text) != nil || len(w.text) > 2*1024*1024 {
			return errors.New("invalid Horizon final response")
		}
		w.completed = true
	}
	if w.observe != nil {
		w.observe(e)
	}
	return nil
}
func streamedRunner(binary, home string, observe func(childEvent)) runProcess {
	return func(ctx context.Context, dir string, args []string, input string) (string, error) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		w := &eventCollector{observe: observe, cancel: cancel}
		err := executeProcess(ctx, binary, home, dir, args, input, w)
		if w.err != nil {
			return "", w.err
		}
		if err != nil {
			return "", err
		}
		if len(w.buffer) > 0 {
			return "", errors.New("truncated Horizon event stream")
		}
		if !w.completed {
			return "", errors.New("Horizon exited without a completed turn")
		}
		return w.text, nil
	}
}

// Only selected metadata enters the service journal; raw event payloads can
// contain user text, command arguments, tool output and encrypted API items.
func (g *bridge) childDiagnostic(e childEvent, correlation ...map[string]any) {
	keys := map[string][]string{
		"turn_started":   {"model_profile", "model", "home", "skills", "instructions_sha256"},
		"turn_completed": {},
		"turn_failed":    {"code"}, "turn_cancelled": {"code"},
		"model_request_started":   {"attempt"},
		"model_request_completed": {"attempt", "duration_ms", "ok", "code"},
		"tool_started":            {"name", "call_id"},
		"tool_completed":          {"name", "call_id", "duration_ms"},
		"compaction_started":      {}, "compaction_completed": {},
	}
	allowed, ok := keys[e.Type]
	if !ok {
		return
	}
	fields := map[string]any{"session_id": e.Session, "turn_id": e.Turn}
	for _, data := range correlation {
		for key, value := range data {
			fields[key] = value
		}
	}
	fields["session_id"] = e.Session
	for _, key := range allowed {
		if value, ok := e.Data[key]; ok {
			fields[key] = value
		}
	}
	if e.Type == "tool_completed" {
		var result struct {
			OK    bool                       `json:"ok"`
			Data  map[string]json.RawMessage `json:"data"`
			Error struct {
				Code    string                     `json:"code"`
				Details map[string]json.RawMessage `json:"details"`
			} `json:"error"`
		}
		if json.Unmarshal(e.Data["result"], &result) == nil {
			fields["ok"] = result.OK
			if !result.OK {
				fields["code"] = result.Error.Code
			}
			for _, key := range []string{"exit_code", "timed_out", "stdout_path", "stderr_path"} {
				if value, ok := result.Data[key]; ok {
					fields[key] = value
				} else if value, ok := result.Error.Details[key]; ok {
					fields[key] = value
				}
			}
		}
	}
	g.emit("info", fmt.Sprintf("horizon_%s", e.Type), fields)
}
