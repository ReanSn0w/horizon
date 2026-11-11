package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/daemon"
	"github.com/ReanSn0w/horizon/internal/maintenance"
	"github.com/ReanSn0w/horizon/internal/session"
	"gopkg.in/yaml.v3"
)

func migrationFixture(t *testing.T) (string, string, []byte) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "bin")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	binaries := map[string]string{
		"horizon":          "{}",
		"horizon-daemon":   `{"protocol_version":1,"description":"daemon","version":"1"}`,
		"horizon-telegram": `{"protocol_version":1,"agent_protocol_version":2,"description":"telegram","version":"1"}`,
		"agent-telegram":   `{"protocol_version":1,"description":"telegram input","version":"1","capabilities":["active-input"]}`,
	}
	for binary, metadata := range binaries {
		if err := os.WriteFile(filepath.Join(dir, binary), []byte("#!/bin/sh\nprintf '%s\\n' '"+metadata+"'\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	base := filepath.Join(home, "legacy-workspaces")
	oldConfig := []byte(fmt.Sprintf(`# existing home
mode: unit
providers:
  local:
    url: http://127.0.0.1:1
    key: fixture-key
models:
  local:
    provider: local
    model: fixture
    compact_threshold: 100
default_model: local
decision_model: local
agent_plugins: [browser]
plugins:
  telegram:
    telegram:
      bot_token: 99:fixture
      owner_user_id: 1
    workspace_dir: %q
    group_access: read
    max_parallel_chats: 2
    progress_messages: all
    groups:
      "-22":
        owner_only: false
        response_mode: conversation
  daemon:
    adapters: [other]
`, base))
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), oldConfig, 0600); err != nil {
		t.Fatal(err)
	}
	workspace, err := Workspace(base, 99, Conversation{ID: -22, Origin: -2})
	if err != nil {
		t.Fatal(err)
	}
	store := session.NewStore(home)
	resolved, err := store.ResolveWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.Create(resolved)
	if err != nil {
		t.Fatal(err)
	}
	sessionPath, err := store.SessionPath(resolved, current.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	legacy := legacyGateway{Version: 1, BotID: 99, Offset: 812, Chats: map[string]*legacyChat{
		"-22": {Origin: -2, ID: -22, Type: "supergroup", Title: "Migrated", Available: true, Workspace: workspace, Session: current.SessionID,
			Jobs: []*legacyJob{
				{ID: "unsafe", ChatID: -22, State: "unknown"},
				{ID: "old-sending", ChatID: -22, State: "sending"},
				{ID: "generated", ChatID: -22, State: "generated"},
				{ID: "manual", ChatID: -22, State: "queued", Manual: true},
				{ID: "pending", ChatID: -22, State: "queued", Input: legacyRecord{Entry: Entry{ID: 18, Author: 2, Text: "preserved input"}}},
			},
		},
	}}
	data, err := yaml.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(legacyStatePath(home, 99), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(filepath.Join(home, "gateway", "service.yaml"), []byte("manager: launchd\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return home, sessionPath, oldConfig
}

func TestMigrateLegacyQueueAndExistingSessionIdempotently(t *testing.T) {
	home, sessionPath, oldConfig := migrationFixture(t)
	originalSession, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	lockPath, err := PollLock(home)
	if err != nil {
		t.Fatal(err)
	}
	release, err := maintenance.Lock(lockPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(ctx, home, "--check"); err == nil || !strings.Contains(err.Error(), "stop the old") {
		t.Fatalf("running legacy process not detected: %v", err)
	}
	release()
	checked, err := Migrate(ctx, home, "--check")
	if err != nil || checked.State != "ready" || checked.Queued != 1 || checked.Unknown != 2 || checked.Archived != 2 || !checked.LegacyService {
		t.Fatalf("check = %#v, error = %v", checked, err)
	}
	unchanged, err := os.ReadFile(filepath.Join(home, "config.yaml"))
	if err != nil || !bytes.Equal(unchanged, oldConfig) {
		t.Fatal("preflight modified config")
	}
	report, err := Migrate(ctx, home, "--apply")
	if err != nil || report.State != "complete" {
		t.Fatalf("apply = %#v, error = %v", report, err)
	}
	for _, name := range []string{"config.yaml", "legacy-state.yaml", "legacy-service.yaml", "manifest.json"} {
		info, err := os.Stat(filepath.Join(report.Backup, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("backup %s mode or read error: %v %v", name, info, err)
		}
	}
	cfg, err := config.Load(home)
	if err != nil || strings.Join(cfg.AgentPlugins, ",") != "browser,telegram" || strings.Join(cfg.AgentCommunication, ",") != "telegram" {
		t.Fatalf("agent configuration failed validation: %v", err)
	}
	settings, err := ReadAdapter(cfg, home)
	if err != nil || settings.Groups["-22"].Mode != "conversation" || settings.Groups["-22"].OwnerOnly {
		t.Fatalf("group settings = %#v, error = %v", settings, err)
	}
	registry, err := NewStore(home, 99).Snapshot()
	if err != nil || registry.Offset != 812 || registry.Chats[-2].ID != -22 {
		t.Fatalf("registry = %#v, error = %v", registry, err)
	}
	statePath := filepath.Join(home, "daemon", "state.json")
	queueData, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Tasks      map[string]daemon.Task      `json:"tasks"`
		Operations map[string]daemon.Operation `json:"operations"`
	}
	if err := json.Unmarshal(queueData, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Tasks) != 4 || len(state.Operations) != 3 {
		t.Fatalf("expected route binding and 3 legacy tasks, found %d tasks and %d operations", len(state.Tasks), len(state.Operations))
	}
	id := strings.TrimSuffix(filepath.Base(sessionPath), ".json")
	for _, task := range state.Tasks {
		if task.Route != "-2" || task.Session != id || !task.Channel {
			t.Fatalf("existing session route was lost: %#v", task)
		}
	}
	unknown := state.Tasks[state.Operations["adapter:telegram:telegram:99:legacy:unsafe"].TaskID]
	pending := state.Tasks[state.Operations["adapter:telegram:telegram:99:legacy:pending"].TaskID]
	if unknown.State != "unknown" || pending.State != "queued" || pending.Child != unknown.Child || pending.Sequence <= unknown.Sequence {
		t.Fatalf("legacy unknown does not block pending task: %#v %#v", unknown, pending)
	}
	if repeat, err := Migrate(ctx, home, "--apply"); err != nil || repeat != report {
		t.Fatalf("repeat = %#v, error = %v", repeat, err)
	}
	currentQueue, err := os.ReadFile(statePath)
	currentSession, e2 := os.ReadFile(sessionPath)
	if err != nil || e2 != nil || !bytes.Equal(queueData, currentQueue) || !bytes.Equal(currentSession, originalSession) {
		t.Fatal("migration rewrote a session or duplicated daemon state")
	}
	if _, err := Migrate(ctx, home, "--rollback"); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(filepath.Join(home, "config.yaml"))
	if err != nil || !bytes.Equal(restored, oldConfig) {
		t.Fatal("original config was not restored")
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatal("rollback left imported daemon state")
	}
	if _, err := Migrate(ctx, home, "--apply"); err != nil {
		t.Fatal(err)
	}
	if err := MarkUse(home); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(ctx, home, "--rollback"); err == nil || !strings.Contains(err.Error(), "polling or delivery") {
		t.Fatalf("rollback not blocked after activation: %v", err)
	}
}

func TestResumeInterruptedMigrationAndRollbackThroughConfigSymlink(t *testing.T) {
	home, _, oldConfig := migrationFixture(t)
	ctx := context.Background()
	configPath := filepath.Join(home, "config.yaml")
	target := filepath.Join(home, "actual.yaml")
	if err := os.Rename(configPath, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("actual.yaml", configPath); err != nil {
		t.Fatal(err)
	}
	report, err := Migrate(ctx, home, "--apply")
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(report.Backup, "manifest.json")
	var manifest migrationManifest
	if err := loadJSON(manifestPath, &manifest); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(daemon.MigrationBarrier(home), []byte("interrupted migration\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// All staged files and operations were committed before the manifest.
	manifest.State = "prepared"
	if err := saveJSON(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	if err := MarkUse(home); err == nil {
		t.Fatal("external actions allowed during migration")
	}
	checked, err := Migrate(ctx, home, "--check")
	if err != nil || checked.State != "resume required" {
		t.Fatalf("check = %#v, error = %v", checked, err)
	}
	if _, err := Migrate(ctx, home, "--apply"); err != nil {
		t.Fatal(err)
	}

	// The daemon state was restored, but the registry and config were not.
	if err := loadJSON(manifestPath, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.State = "rolling_back"
	if err := saveJSON(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(daemon.MigrationBarrier(home), []byte("interrupted rollback\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(home, "daemon", "state.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(ctx, home, "--apply"); err == nil || !strings.Contains(err.Error(), "finish") {
		t.Fatalf("apply released an interrupted rollback: %v", err)
	}
	if _, err := Migrate(ctx, home, "--rollback"); err != nil {
		t.Fatal(err)
	}
	if link, err := os.Readlink(configPath); err != nil || link != "actual.yaml" {
		t.Fatalf("config symlink changed: %q, %v", link, err)
	}
	if original, err := os.ReadFile(target); err != nil || !bytes.Equal(original, oldConfig) {
		t.Fatal("symlink target configuration was not restored")
	}
}

func TestMigrateRefusesConflictingAdapterOverride(t *testing.T) {
	home, _, old := migrationFixture(t)
	path := filepath.Join(home, "config.yaml")
	config := append(append([]byte{}, old...), []byte("  agent-telegram:\n    group_access: full\n")...)
	if err := os.WriteFile(path, config, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(context.Background(), home, "--check"); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("override silently replaced: %v", err)
	}
	if saved, err := os.ReadFile(path); err != nil || !bytes.Equal(saved, config) {
		t.Fatal("conflicting config was modified")
	}
}
