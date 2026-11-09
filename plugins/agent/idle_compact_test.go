package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ReanSn0w/horizon/internal/session"
)

func TestIdleCompactAfterConfig(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		want        time.Duration
		invalid     bool
	}{
		{name: "legacy", want: 12 * time.Hour},
		{name: "six_hours", value: "6h", want: 6 * time.Hour},
		{name: "disabled", value: "0"},
		{name: "too_short", value: "30m", invalid: true},
		{name: "negative", value: "-1h", invalid: true},
		{name: "invalid", value: "later", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			payload := "plugins:\n  telegram:\n    conversation: {}\n"
			if tc.value != "" {
				payload = "plugins:\n  telegram:\n    conversation:\n      idle_compact_after: " + tc.value + "\n"
			}
			if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(payload), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := loadSettings(home, false)
			if tc.invalid {
				if err == nil {
					t.Fatal("accepted invalid idle compact duration")
				}
				return
			}
			if err != nil || cfg.idleCompactAfter() != tc.want {
				t.Fatalf("duration=%s err=%v", cfg.idleCompactAfter(), err)
			}
		})
	}
}

func TestIdleChatEligibility(t *testing.T) {
	now := time.Date(2026, time.October, 8, 12, 0, 0, 0, time.UTC)
	c := &chat{Available: true, Workspace: "/workspace", Session: "session", LastAt: now.Add(-24 * time.Hour)}
	if !idleChatEligible(c, now, 12*time.Hour) {
		t.Fatal("old chat state was not eligible")
	}
	c.LastReceivedAt = now.Add(-6 * time.Hour)
	if idleChatEligible(c, now, 12*time.Hour) || !idleChatEligible(c, now, 6*time.Hour) {
		t.Fatal("new receive time was not used")
	}
	c.Jobs = []*job{{Status: "queued"}}
	if idleChatEligible(c, now, 6*time.Hour) {
		t.Fatal("queued chat was eligible")
	}
	c.Jobs = nil
	if idleChatEligible(c, now, 0) {
		t.Fatal("disabled compaction was eligible")
	}
}

func idleSession(t *testing.T, home, workspace string, itemBytes int) string {
	t.Helper()
	store := session.NewStore(home)
	resolved, err := store.ResolveWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	value, err := store.Create(resolved)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := store.LockSession(resolved, value.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	now := time.Now().UTC()
	value.Turns = []session.Turn{{ID: "turn-1", Status: session.StatusCompleted, StartedAt: now, CompletedAt: &now, Model: session.ModelProfile{Name: "chat", Model: "model", CompactThreshold: 1000}}}
	value.Checkpoints = []session.ContextCheckpoint{{TurnID: "turn-1", CreatedAt: now, Items: []json.RawMessage{json.RawMessage(`"` + strings.Repeat("x", itemBytes) + `"`)}}}
	if err := locked.Save(value); err != nil {
		t.Fatal(err)
	}
	return value.SessionID
}

func TestIdleSessionInspectionAndRetry(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	id := idleSession(t, home, workspace, minIdleCheckpointBytes)
	g := &bridge{home: home}
	c := &chat{ID: 1, Origin: 1, Available: true, Workspace: workspace, Session: id}
	candidate, reason, err := g.inspectIdleSession(c)
	if err != nil || reason != "" || candidate.boundary != "turn-1" || candidate.bytes < minIdleCheckpointBytes {
		t.Fatalf("candidate=%+v reason=%q err=%v", candidate, reason, err)
	}
	now := time.Now()
	c.LastReceivedAt = now.Add(-12 * time.Hour)
	c.LastCompactAttemptAt = now.Add(-30 * time.Minute)
	if idleCompactDue(c, now, 6*time.Hour) {
		t.Fatal("retry delay ignored")
	}
	c.LastCompactAttemptAt = now.Add(-time.Hour)
	if !idleCompactDue(c, now, 6*time.Hour) {
		t.Fatal("retry did not become due")
	}
	store := session.NewStore(home)
	resolved, _ := store.ResolveWorkspace(workspace)
	locked, err := store.LockSession(resolved, id)
	if err != nil {
		t.Fatal(err)
	}
	_, reason, err = g.inspectIdleSession(c)
	_ = locked.Close()
	if err != nil || reason != "session_busy" {
		t.Fatalf("busy reason=%q err=%v", reason, err)
	}
}

func TestParseIdleCompactResult(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  bool
		bad   bool
	}{
		{`{"type":"compaction_completed","data":{"compaction_id":"c1"}}`, true, false},
		{`{"type":"compaction_completed","data":{"compacted":false}}`, false, false},
		{`{"type":"turn_completed"}`, false, true},
	} {
		got, err := parseIdleCompactResult(tc.input)
		if got != tc.want || (err != nil) != tc.bad {
			t.Fatalf("input=%s got=%v err=%v", tc.input, got, err)
		}
	}
}
