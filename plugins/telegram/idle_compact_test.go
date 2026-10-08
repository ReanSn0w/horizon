package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
