package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHealthSnapshotDistinguishesFreshStaleAndMissing(t *testing.T) {
	home := t.TempDir()
	var out bytes.Buffer
	a := &app{home: home, out: &out}
	printHealth(a)
	if !strings.Contains(out.String(), "unavailable") {
		t.Fatal(out.String())
	}
	f, err := lockFile(filepath.Join(home, "gateway", "process.lock"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock(f)
	h := newHealth("first")
	h.event("job_started", map[string]any{"request_id": "j", "chat_id": int64(1)})
	h.event("horizon_tool_started", map[string]any{"request_id": "j"})
	v := h.snapshot()
	if len(v.Active) != 1 || v.Active[0].Stage != "tool" {
		t.Fatal(v)
	}
	save := func() {
		data, _ := json.Marshal(v)
		if err := atomicFile(filepath.Join(home, "gateway", "health.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save()
	out.Reset()
	printHealth(a)
	if !strings.Contains(out.String(), "Diagnostics: fresh") || !strings.Contains(out.String(), "request j; tool") {
		t.Fatal(out.String())
	}
	v.Updated = time.Now().Add(-time.Minute)
	save()
	out.Reset()
	printHealth(a)
	if !strings.Contains(out.String(), "Diagnostics: stale") {
		t.Fatal(out.String())
	}
	v = newHealth("second").snapshot()
	save()
	got, err := readHealth(home)
	if err != nil || got.RunID != "second" || len(got.Active) != 0 {
		t.Fatal(got, err)
	}
}

func TestReadHealthRejectsOversizedSnapshot(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "gateway"), 0700); err != nil {
		t.Fatal(err)
	}
	data := `{"schema_version":1,"run_id":"` + strings.Repeat("x", 130*1024) + `"}`
	if err := os.WriteFile(filepath.Join(home, "gateway", "health.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readHealth(home); err == nil {
		t.Fatal("oversized diagnostic snapshot accepted")
	}
}

func TestListCurrentJobDoesNotUseLastQueuedJob(t *testing.T) {
	home := readyHome(t)
	lock, err := lockFile(filepath.Join(home, "gateway", "process.lock"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock(lock)
	if err := atomicIdentity(home, 99); err != nil {
		t.Fatal(err)
	}
	s := newStore(home, 99)
	if err := s.update(func(v *state) error {
		v.Chats["1"] = &chat{ID: 1, Origin: 1, Type: "private", Jobs: []*job{{ID: "current", ChatID: 1, Status: "generating"}, {ID: "last", ChatID: 1, Status: "queued", QueuedAt: time.Now().Add(-time.Second)}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h := newHealth("run")
	h.event("job_started", map[string]any{"request_id": "current", "chat_id": int64(1)})
	data, _ := json.Marshal(h.snapshot())
	if err := atomicFile(filepath.Join(home, "gateway", "health.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := listChats(&app{home: home, out: &out}, &listCommand{JSON: true}); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Chats []listedChat `json:"chats"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	c := result.Chats[0]
	if c.CurrentRequest != "current" || c.LastJob != "last" || c.QueueWaitMS == nil || *c.QueueWaitMS < 1000 {
		t.Fatal(c)
	}
}
