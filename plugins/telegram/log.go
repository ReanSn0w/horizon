package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type rotatingLog struct {
	mu    sync.Mutex
	path  string
	limit int64
}

func (l *rotatingLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if int64(len(p)) > l.limit {
		return 0, fmt.Errorf("diagnostic record exceeds journal limit")
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0700); err != nil {
		return 0, err
	}
	if info, err := os.Stat(l.path); err == nil && info.Size()+int64(len(p)) > l.limit {
		if err = os.Rename(l.path, l.path+".1"); err != nil {
			return 0, err
		}
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return 0, err
	}
	return f.Write(p)
}

type synchronizedWriter struct {
	mu  sync.Mutex
	out io.Writer
}

func (w *synchronizedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.out.Write(p)
}

// diagnosticLog owns the service journal. Fallback never calls this writer.
type diagnosticLog struct {
	mu            sync.Mutex
	out, fallback io.Writer
	runID         string
	failed        bool
	secrets       []string
}

func (l *diagnosticLog) event(level, name string, fields map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	data, err := json.Marshal(struct {
		Version int            `json:"schema_version"`
		Time    time.Time      `json:"timestamp"`
		Level   string         `json:"level"`
		Event   string         `json:"event"`
		RunID   string         `json:"run_id"`
		Data    map[string]any `json:"data,omitempty"`
	}{1, time.Now().UTC(), level, name, l.runID, fields})
	if err != nil {
		return
	}
	for _, secret := range l.secrets {
		if secret != "" {
			data = []byte(strings.ReplaceAll(string(data), secret, "[redacted]"))
		}
	}
	if len(data) > 64*1024 {
		data, _ = json.Marshal(map[string]any{"schema_version": 1, "timestamp": time.Now().UTC(), "level": "error", "event": "diagnostic_record_oversized", "run_id": l.runID, "dropped_event": name})
	}
	data = append(data, '\n')
	if _, err = l.out.Write(data); err != nil {
		if !l.failed && l.fallback != nil {
			fmt.Fprintf(l.fallback, "telegram: diagnostic journal failed: %v\n", err)
		}
		l.failed = true
		if l.fallback != nil {
			_, _ = l.fallback.Write(data)
		}
	} else if l.failed {
		l.failed = false
		if l.fallback != nil {
			fmt.Fprintln(l.fallback, "telegram: diagnostic journal recovered")
		}
	}
}
func (l *diagnosticLog) Write(p []byte) (int, error) {
	l.event("error", "diagnostic", map[string]any{"message": journalError(string(p))})
	return len(p), nil
}
func openDiagnosticLog(path string, fallback io.Writer) (*diagnosticLog, error) {
	var out io.Writer = fallback
	if path != "" {
		w := &rotatingLog{path: path, limit: 1024 * 1024}
		// Archive old text journals before emitting the first JSONL record.
		f, err := os.Open(path)
		if err == nil {
			var first [1]byte
			n, readErr := f.Read(first[:])
			_ = f.Close()
			if readErr != nil && readErr != io.EOF {
				return nil, readErr
			}
			if n > 0 && first[0] != '{' {
				if err = os.Rename(path, path+".1"); err != nil {
					return nil, err
				}
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		if _, err := w.Write(nil); err != nil {
			return nil, fmt.Errorf("open diagnostic journal %q: %w", path, err)
		}
		out = w
	}
	if out == nil {
		out = io.Discard
	}
	return &diagnosticLog{out: out, fallback: fallback, runID: requestID()}, nil
}

// Provider stderr can echo request data. Full bounded diagnostics remain in
// local telegram state; the service journal records only the failure category.
func journalError(value string) string {
	for _, prefix := range []string{"Horizon process failed (exit ", "Telegram HTTP/API "} {
		if i := strings.Index(value, prefix); i >= 0 {
			if colon := strings.IndexByte(value[i:], ':'); colon >= 0 {
				return value[:i+colon] + "; inspect local Telegram state/session"
			}
		}
	}
	return value
}
