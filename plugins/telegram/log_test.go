package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func TestMessageDiagnosticsCorrelateWithoutText(t *testing.T) {
	home := readyHome(t)
	cfg, _ := loadSettings(home, true)
	s := newStore(home, 99)
	var out bytes.Buffer
	journal := &diagnosticLog{out: &out, runID: "run", secrets: []string{"test-token"}}
	observe := func(name string, data map[string]any) { journal.event("info", name, data) }
	u := update{ID: 5, Message: &tgMessage{ID: 42, Chat: tgChat{ID: 1, Type: "private"}, From: &tgUser{ID: 1}, Text: "PRIVATE_MARKER"}}
	if err := ingest(home, s, cfg, tgUser{ID: 99}, u, observe); err != nil {
		t.Fatal(err)
	}
	v, _ := s.snapshot()
	c := v.Chats["1"]
	j := c.Jobs[0]
	g := &bridge{home: home, cfg: cfg, store: s, journal: journal, run: func(_ context.Context, _ string, args []string, _ string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "sessions create") {
			return "session", nil
		}
		return "REPLY_MARKER", nil
	}}
	if err := g.process(context.Background(), c, j, cfg); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"PRIVATE_MARKER", "REPLY_MARKER", "test-token"} {
		if bytes.Contains(out.Bytes(), []byte(marker)) {
			t.Fatal("private content leaked", marker)
		}
	}
	for _, name := range []string{"message_queued", "offset_saved", "reply_decision", "job_status"} {
		if !bytes.Contains(out.Bytes(), []byte(`"event":"`+name+`"`)) {
			t.Fatal("missing event", name, out.String())
		}
	}
	if !bytes.Contains(out.Bytes(), []byte(`"request_id":"`+j.ID+`"`)) {
		t.Fatal("missing correlation")
	}
	cfg.Defaults = groupSettings{false, "conversation"}
	c.Type = "group"
	c.ID = -1
	j.Input.Author = 2
	_, err := shouldReply(context.Background(), home, cfg, c, j, "jev", func(context.Context, string, []string, string) (string, error) {
		return `{"answers":{"should_reply":{"type":"noul","noul":0.1}}}`, nil
	}, func(data map[string]any) {
		if data["reply"] != false || data["score"] != 0.1 {
			t.Fatal(data)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}
