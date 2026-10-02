package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("disk failed") }

func TestDiagnosticLogFailureAndRecovery(t *testing.T) {
	var fallback bytes.Buffer
	l := &diagnosticLog{out: brokenWriter{}, fallback: &fallback, runID: "run"}
	l.event("info", "first", nil)
	l.event("info", "second", nil)
	if bytes.Count(fallback.Bytes(), []byte("journal failed")) != 1 || !bytes.Contains(fallback.Bytes(), []byte(`"event":"second"`)) {
		t.Fatal(fallback.String())
	}
	l.out = io.Discard
	l.event("info", "third", nil)
	if !bytes.Contains(fallback.Bytes(), []byte("journal recovered")) {
		t.Fatal(fallback.String())
	}
}

func TestDiagnosticLogPreflightAndConcurrentRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "service.log")
	l, err := openDiagnosticLog(path, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	l.out.(*rotatingLog).limit = 600
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { l.event("info", "event", map[string]any{"value": 1}) })
	}
	wg.Wait()
	for _, name := range []string{path, path + ".1"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
			if !json.Valid(line) {
				t.Fatalf("corrupt log: %s", line)
			}
		}
		info, _ := os.Stat(name)
		if info.Mode().Perm() != 0600 || info.Size() > 600 {
			t.Fatal(info)
		}
	}
	if _, err := openDiagnosticLog(filepath.Dir(path), io.Discard); err == nil {
		t.Fatal("accepted a directory as journal")
	}
}

func TestDiagnosticLogArchivesLegacyText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	if err := os.WriteFile(path, []byte("legacy\n"), 0600); err != nil {
		t.Fatal(err)
	}
	l, err := openDiagnosticLog(path, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	l.event("info", "new", nil)
	old, _ := os.ReadFile(path + ".1")
	if string(old) != "legacy\n" {
		t.Fatal(string(old))
	}
	data, _ := os.ReadFile(path)
	if !json.Valid(bytes.TrimSpace(data)) {
		t.Fatal(string(data))
	}
}
