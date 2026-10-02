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
	l.event("error", "diagnostic", map[string]any{"message": string(p)})
	return len(p), nil
}
func openDiagnosticLog(path string, fallback io.Writer) (*diagnosticLog, error) {
	var out io.Writer = fallback
	if path != "" {
		w := &rotatingLog{path: path, limit: 1024 * 1024}
		// Archive old text journals before emitting the first JSONL record.
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 && data[0] != '{' {
			if err = os.Rename(path, path+".1"); err != nil {
				return nil, err
			}
		} else if err != nil && !os.IsNotExist(err) {
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
