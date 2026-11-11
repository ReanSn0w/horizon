package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ReanSn0w/horizon/internal/daemon"
	"github.com/ReanSn0w/horizon/internal/session"
	"github.com/ReanSn0w/horizon/internal/telegram"
)

func TestBuiltTelegramAdapterAndExplicitDelivery(t *testing.T) {
	binary := buildBinary(t)
	home, err := os.MkdirTemp("/tmp", "htg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	cwd := t.TempDir()
	makeUpdate := func(id, chat, author int64, kind, text string, mention bool) map[string]any {
		input := map[string]any{
			"message_id": id, "date": 1700000000 + id, "text": text,
			"chat": map[string]any{"id": chat, "type": kind, "title": "Fixture group"},
			"from": map[string]any{"id": author, "first_name": "Fixture participant"},
		}
		if mention {
			input["entities"] = []any{map[string]any{"type": "mention", "offset": 0, "length": 9}}
		}
		return map[string]any{"update_id": id, "message": input}
	}
	updates := []map[string]any{
		makeUpdate(1, 9, 9, "private", "FOREIGN_PRIVATE", false),
		makeUpdate(2, 1, 1, "private", "OWNER_SILENCE", false),
		makeUpdate(3, -10, 2, "group", "MENTION_BACKGROUND", false),
		makeUpdate(4, -10, 2, "group", "@test_bot MENTION_SILENCE", true),
		makeUpdate(5, -20, 2, "supergroup", "GROUP_BEGIN", false),
		makeUpdate(6, -30, 2, "group", "OWNER_ONLY_BACKGROUND", false),
		makeUpdate(7, -30, 1, "group", "OWNER_ONLY_TRIGGER", false),
	}
	type outgoing struct {
		Chat int64  `json:"chat_id"`
		Text string `json:"text"`
	}
	var mu sync.Mutex
	sent := []outgoing{}
	var offset, requests, decisions, groupTyping atomic.Int64
	var ownerSeen, mentionSeen, ownerOnlySeen, groupFinished, groupNextSeen atomic.Bool
	groupStarted := make(chan struct{})
	releaseGroup := make(chan struct{})
	var startOnce, releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseGroup) }) }
	defer release()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/responses" {
			var request struct {
				Model        string            `json:"model"`
				Input        []json.RawMessage `json:"input"`
				Instructions string            `json:"instructions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode provider request: %v", err)
				http.Error(w, "decode error", 400)
				return
			}
			if request.Model == "review" {
				decisions.Add(1)
				writeDecisionResult(w, "review-id", map[string]any{"file_write": true, "outside_write": false})
				return
			}
			number := int(requests.Add(1))
			if !strings.Contains(request.Instructions, "telegram__send_message") || strings.Contains(request.Instructions, "MENTION_BACKGROUND") {
				t.Error("Telegram instructions lack explicit tool or contain participant text")
			}
			raw, _ := json.Marshal(request.Input)
			input := string(raw)
			if strings.Contains(input, "FOREIGN_PRIVATE") {
				t.Error("foreign private input reached a model")
			}
			result := func(id string) string {
				for _, raw := range request.Input {
					var item struct {
						Type   string `json:"type"`
						CallID string `json:"call_id"`
						Output string `json:"output"`
					}
					if json.Unmarshal(raw, &item) == nil && item.Type == "function_call_output" && item.CallID == id {
						return item.Output
					}
				}
				return ""
			}
			final := func() { writeCompleted(w, number, message("FINAL_IN_HORIZON_ONLY")) }
			switch {
			case strings.Contains(input, "OWNER_AUTONOMY"):
				if response := result("owner-send"); response != "" {
					if !strings.Contains(response, `"chat_id":1`) || !strings.Contains(response, `"state":"confirmed"`) {
						t.Errorf("autonomous send result = %s", response)
					}
					final()
				} else {
					writeCompleted(w, number, []json.RawMessage{tgCall("owner-send", "telegram__send_message", `{"text":"autonomous owner"}`)})
				}
			case strings.Contains(input, "GROUP_AFTER"):
				if strings.Contains(request.Instructions, "group first") || strings.Contains(request.Instructions, "group second") {
					t.Error("confirmed outgoing messages already in the session were added to context again")
				}
				groupNextSeen.Store(true)
				final()
			case strings.Contains(input, "GROUP_FOLLOWUP"):
				if response := result("group-one"); response != "" {
					if !strings.Contains(response, `"chat_id":-20`) || !strings.Contains(response, `"state":"confirmed"`) ||
						!strings.Contains(result("group-two"), `"state":"confirmed"`) ||
						!strings.Contains(result("wrong-route"), `"code":"invalid_arguments"`) ||
						!strings.Contains(result("group-write"), `"code":"decision_denied"`) {
						t.Errorf("group tool outcomes: first=%s second=%s wrong route=%s filesystem=%s", response, result("group-two"), result("wrong-route"), result("group-write"))
					}
					groupFinished.Store(true)
					final()
				} else {
					if !strings.Contains(input, "GROUP_BEGIN") {
						t.Error("clarification lost its original input")
					}
					writeCompleted(w, number, []json.RawMessage{
						tgCall("group-one", "telegram__send_message", `{"text":"group first"}`),
						tgCall("wrong-route", "telegram__send_message", `{"text":"wrong route","chat_id":1}`),
						tgCall("group-two", "telegram__send_message", `{"text":"group second"}`),
						tgCall("group-write", "shell_exec", `{"command":"touch forbidden-write","timeout_ms":null,"max_output_chars":null,"yield_time_ms":null}`),
					})
				}
			case strings.Contains(input, "GROUP_BEGIN"):
				startOnce.Do(func() { close(groupStarted) })
				select {
				case <-releaseGroup:
					final()
				case <-r.Context().Done():
				}
			case strings.Contains(input, "MENTION_SILENCE"):
				if !strings.Contains(input, "MENTION_BACKGROUND") {
					t.Error("group context was omitted from mention")
				}
				mentionSeen.Store(true)
				final()
			case strings.Contains(input, "OWNER_ONLY_TRIGGER"):
				if !strings.Contains(input, "OWNER_ONLY_BACKGROUND") {
					t.Error("owner_only participant context was omitted")
				}
				ownerOnlySeen.Store(true)
				final()
			case strings.Contains(input, "OWNER_SILENCE"):
				ownerSeen.Store(true)
				final()
			default:
				t.Error("unexpected Telegram input reached a model")
				final()
			}
			return
		}
		switch r.URL.Path {
		case "/bot99:fixture/getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":99,"is_bot":true,"username":"test_bot"}}`)
		case "/bot99:fixture/getWebhookInfo":
			fmt.Fprint(w, `{"ok":true,"result":{"url":""}}`)
		case "/bot99:fixture/getUpdates":
			var arg struct {
				Offset int64 `json:"offset"`
			}
			_ = json.NewDecoder(r.Body).Decode(&arg)
			offset.Store(arg.Offset)
			mu.Lock()
			batch := []map[string]any{}
			for _, u := range updates {
				if u["update_id"].(int64) >= arg.Offset {
					batch = append(batch, u)
				}
			}
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": batch})
		case "/bot99:fixture/sendChatAction":
			var body struct {
				Chat   int64  `json:"chat_id"`
				Action string `json:"action"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Chat == 9 || body.Action != "typing" {
				t.Errorf("unexpected Telegram action %#v", body)
			}
			if body.Chat == -20 {
				groupTyping.Add(1)
			}
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		case "/bot99:fixture/sendMessage":
			var body outgoing
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			sent = append(sent, body)
			id := 100 + len(sent)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": id, "chat": map[string]any{"id": body.Chat}}})
		default:
			t.Error("unexpected local API endpoint")
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	telegramBins(t, home, binary, server.URL)
	cfg := fmt.Sprintf(`mode: unit
providers:
  local:
    url: %s
    key: fixture
default_model: chat
decision_model: review
models:
  chat:
    provider: local
    model: fixture
    compact_threshold: 100000
  review:
    provider: local
    model: review
    compact_threshold: 100000
agent_plugins: [telegram]
agent_communication: [telegram]
plugins:
  telegram:
    bot_token: 99:fixture
    owner_user_id: 1
  daemon:
    adapters: [telegram]
  agent-telegram:
    workspace_dir: chats
    idle_compact_after: 0s
    groups:
      "-10": {owner_only: false, response_mode: mention}
      "-20": {owner_only: false, response_mode: conversation}
      "-30": {owner_only: true, response_mode: conversation}
limits:
  max_model_requests: 12
  max_turn_duration: 30s
`, server.URL)
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}

	stopDaemon := startTelegramDaemon(t, binary, home, cwd)
	defer func() { release(); stopDaemon() }()
	select {
	case <-groupStarted:
	case <-time.After(12 * time.Second):
		status, _ := os.ReadFile(filepath.Join(home, "gateway", "telegram-adapter.error"))
		t.Fatalf("group turn did not start, Telegram adapter status %q", status)
	}
	telegramEventually(t, "typing during active group turn", func() bool { return groupTyping.Load() > 0 })
	mu.Lock()
	updates = append(updates, makeUpdate(8, -20, 3, "supergroup", "GROUP_FOLLOWUP", false))
	mu.Unlock()
	telegramEventually(t, "durable acknowledgement of clarification", func() bool { return offset.Load() == 9 })
	release()
	telegramEventually(t, "all Telegram turns", func() bool {
		return ownerSeen.Load() && mentionSeen.Load() && ownerOnlySeen.Load() && groupFinished.Load()
	})
	groupPath := filepath.Join(home, "chats", "telegram_99_-20")
	telegramEventually(t, "completed group session", func() bool {
		store := session.NewStore(home)
		workspace, err := store.ResolveWorkspace(groupPath)
		if err != nil {
			return false
		}
		sessions, err := store.List(workspace, false)
		if err != nil || len(sessions) != 1 || len(sessions[0].Session.Turns) != 1 {
			return false
		}
		_, ok := sessions[0].Session.LastCompletedTurn()
		return ok
	})
	mu.Lock()
	updates = append(updates, makeUpdate(9, -20, 3, "supergroup", "GROUP_AFTER", false))
	mu.Unlock()
	telegramEventually(t, "next turn and confirmed send context", func() bool {
		store := session.NewStore(home)
		workspace, err := store.ResolveWorkspace(groupPath)
		if err != nil {
			return false
		}
		sessions, err := store.List(workspace, false)
		return groupNextSeen.Load() && err == nil && len(sessions) == 1 && len(sessions[0].Session.Turns) == 2 &&
			sessions[0].Session.Turns[1].Status == session.StatusCompleted
	})
	if _, err := os.Stat(filepath.Join(groupPath, "forbidden-write")); !os.IsNotExist(err) {
		t.Fatal("group read access permitted a file write")
	}
	if decisions.Load() != 1 {
		t.Fatalf("decision model called %d times; expected only file policy check", decisions.Load())
	}
	out, errOut, err := run(binary, cwd, "", "--home", home, "resume", "--access", "read", "--mode", "plain", "-m", "OWNER_AUTONOMY")
	if err != nil || strings.TrimSpace(out) != "FINAL_IN_HORIZON_ONLY" {
		t.Fatalf("autonomous turn: %v %q %q", err, out, errOut)
	}
	out, errOut, err = run(binary, cwd, "", "--home", home, "telegram", "list", "--json")
	if err != nil || strings.Contains(out, `"chat_id":9`) || !strings.Contains(out, `"chat_id":-20`) {
		t.Fatalf("Telegram list: %v %q %q", err, out, errOut)
	}
	mu.Lock()
	got := slices.Clone(sent)
	mu.Unlock()
	want := []outgoing{{-20, "group first"}, {-20, "group second"}, {1, "autonomous owner"}}
	if !slices.Equal(got, want) {
		t.Fatalf("Telegram sent %#v, expected only explicit sends %#v", got, want)
	}
}

func tgCall(id, tool, args string) json.RawMessage {
	raw, _ := json.Marshal(map[string]string{"type": "function_call", "call_id": id, "name": tool, "arguments": args})
	return raw
}

func telegramBins(t *testing.T, home, binary, endpoint string) {
	t.Helper()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(binary, filepath.Join(bin, "horizon")); err != nil {
		t.Fatal(err)
	}
	for _, plugin := range []struct {
		name, pkg string
		local     bool
	}{
		{"horizon-daemon", "daemon", false},
		{"horizon-telegram", "telegram", true},
		{"agent-telegram", "agent-telegram", true},
	} {
		args := []string{"build", "-o", filepath.Join(bin, plugin.name)}
		if plugin.local {
			args = append(args, "-ldflags", "-X github.com/ReanSn0w/horizon/internal/telegram.endpoint="+endpoint)
		}
		args = append(args, "./plugins/"+plugin.pkg)
		cmd := exec.Command("go", args...)
		cmd.Dir = "../.."
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v %s", plugin.name, err, out)
		}
	}
}

func telegramEventually(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func startTelegramDaemon(t *testing.T, binary, home, cwd string) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, binary, "--home", home, "daemon", "start")
	cmd.Dir = cwd
	var diag bytes.Buffer
	cmd.Stderr = &diag
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return func() {
		stopCtx, stop := context.WithTimeout(context.Background(), time.Second)
		_, _ = daemon.Call(stopCtx, home, daemon.Request{ID: "stop", Method: "stop"})
		stop()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("daemon exit: %v %s", err, diag.String())
			}
		case <-time.After(4 * time.Second):
			cancel()
			<-done
			t.Errorf("daemon did not stop: %s", diag.String())
		}
		cancel()
	}
}

func TestBuiltTelegramMigrationRunsExistingQueueAndIdleCompaction(t *testing.T) {
	binary := buildBinary(t)
	home, err := os.MkdirTemp("/tmp", "htgm-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	var modelCalls, telegramCalls, compactions atomic.Int64
	var pendingSeen atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/responses":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				http.Error(w, "read failed", 400)
				return
			}
			number := int(modelCalls.Add(1))
			switch {
			case bytes.Contains(body, []byte("PENDING_AFTER_UNKNOWN")):
				if !bytes.Contains(body, []byte("MIGRATION_EXISTING")) {
					t.Error("migrated turn did not resume its existing checkpoint")
				}
				pendingSeen.Store(true)
				writeCompleted(w, number, message("MIGRATED_FINAL_STAYS_LOCAL"))
			case bytes.Contains(body, []byte("MIGRATION_COMPACT")):
				writeCompleted(w, number, message(strings.Repeat("x", 136*1024)))
			case bytes.Contains(body, []byte("MIGRATION_EXISTING")):
				writeCompleted(w, number, message("previous final"))
			default:
				t.Error("unexpected migrated input")
				http.Error(w, "unexpected input", 400)
			}
		case "/responses/compact":
			compactions.Add(1)
			fmt.Fprint(w, `{"id":"compact","object":"response.compaction","output":[{"type":"compaction","id":"summary","encrypted_content":"opaque"}]}`)
		case "/bot99:fixture/getMe":
			telegramCalls.Add(1)
			fmt.Fprint(w, `{"ok":true,"result":{"id":99,"is_bot":true,"username":"test_bot"}}`)
		case "/bot99:fixture/getWebhookInfo":
			telegramCalls.Add(1)
			fmt.Fprint(w, `{"ok":true,"result":{"url":""}}`)
		case "/bot99:fixture/getUpdates":
			telegramCalls.Add(1)
			fmt.Fprint(w, `{"ok":true,"result":[]}`)
		case "/bot99:fixture/sendChatAction":
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		case "/bot99:fixture/sendMessage":
			t.Error("migration sent an old generated result or the new final answer")
			http.Error(w, "unexpected send", 500)
		default:
			t.Error("unexpected local API endpoint during migration")
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	telegramBins(t, home, binary, server.URL)
	base := filepath.Join(home, "legacy-chats")
	oldConfig := fmt.Sprintf(`mode: unit
providers:
  local: {url: %s, key: fixture}
default_model: fixture
decision_model: fixture
models:
  fixture:
    provider: local
    model: fixture
    compact_threshold: 1000000
plugins:
  telegram:
    telegram:
      bot_token: 99:fixture
      owner_user_id: 1
    workspace_dir: %q
    group_access: read
    group_defaults: {owner_only: false, response_mode: conversation}
    conversation: {idle_compact_after: 1h}
`, server.URL, base)
	configPath := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(configPath, []byte(oldConfig), 0600); err != nil {
		t.Fatal(err)
	}

	store := session.NewStore(home)
	prepare := func(origin int64, input string) (session.Workspace, string, string) {
		t.Helper()
		path, err := telegram.Workspace(base, 99, telegram.Conversation{Origin: origin})
		if err != nil {
			t.Fatal(err)
		}
		if _, diag, err := run(binary, path, "", "--home", home, "resume", "--access", "full", "--mode", "plain", "-m", input); err != nil {
			t.Fatalf("prepare legacy session: %v %s", err, diag)
		}
		workspace, err := store.ResolveWorkspace(path)
		if err != nil {
			t.Fatal(err)
		}
		sessions, err := store.List(workspace, false)
		if err != nil || len(sessions) != 1 {
			t.Fatalf("legacy sessions: %d %v", len(sessions), err)
		}
		id := sessions[0].Session.SessionID
		sessionPath, err := store.SessionPath(workspace, id)
		if err != nil {
			t.Fatal(err)
		}
		return workspace, id, sessionPath
	}
	workspace, id, sessionPath := prepare(-2, "MIGRATION_EXISTING")
	idleWorkspace, idleID, _ := prepare(-8, "MIGRATION_COMPACT")
	original, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	gatewayDir := filepath.Join(home, "gateway", "telegram", "99")
	if err := os.MkdirAll(gatewayDir, 0700); err != nil {
		t.Fatal(err)
	}
	legacy := fmt.Sprintf(`version: 1
bot_id: 99
offset: 202
chats:
  "-22":
    origin: -2
    id: -22
    type: supergroup
    available: true
    workspace: %q
    session: %s
    last_received_at: 2024-01-02T00:00:00Z
    jobs:
      - {id: unknown-job, chat_id: -22, status: unknown}
      - {id: generated-job, chat_id: -22, status: generated, response: OLD_FINAL_DO_NOT_SEND}
      - id: pending-job
        chat_id: -22
        status: queued
        input: {id: 13, author: 2, name: Participant, text: PENDING_AFTER_UNKNOWN}
  "-8":
    origin: -8
    id: -8
    type: group
    available: true
    workspace: %q
    session: %s
    last_received_at: 2024-01-02T00:00:00Z
aliases: {"-2": "-22"}
`, workspace.Dir, id, idleWorkspace.Dir, idleID)
	if err := os.WriteFile(filepath.Join(gatewayDir, "state.yaml"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"--check", "--apply", "--apply"} {
		out, diag, err := run(binary, workspace.Dir, "", "--home", home, "telegram", "migrate", mode)
		if err != nil || !strings.Contains(out, "queued 1, unknown 1, archived 1") {
			t.Fatalf("migration %s: %v %q %q", mode, err, out, diag)
		}
	}
	if modelCalls.Load() != 2 || telegramCalls.Load() != 0 {
		t.Fatal("offline migration called an external API")
	}
	restored, err := os.ReadFile(sessionPath)
	if err != nil || !bytes.Equal(restored, original) {
		t.Fatal("migration altered the existing session history")
	}
	raw, err := os.ReadFile(filepath.Join(home, "daemon", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var queue struct {
		Tasks map[string]daemon.Task `json:"tasks"`
	}
	if err := json.Unmarshal(raw, &queue); err != nil {
		t.Fatal(err)
	}
	unknown := ""
	for taskID, task := range queue.Tasks {
		if task.State == "unknown" && task.Session == id {
			unknown = taskID
		}
	}
	if unknown == "" {
		t.Fatal("legacy unknown task was omitted")
	}
	stopDaemon := startTelegramDaemon(t, binary, home, workspace.Dir)
	defer stopDaemon()
	telegramEventually(t, "idle checkpoint compaction", func() bool {
		value, err := store.ReadSnapshot(idleWorkspace, idleID)
		return err == nil && value.LatestCompletedTurnCompacted()
	})
	if pendingSeen.Load() || compactions.Load() != 1 {
		t.Fatal("unknown legacy task did not block its dialog or idle compaction repeated")
	}
	if out, diag, err := run(binary, workspace.Dir, "", "--home", home, "daemon", "resolve-input", unknown); err != nil {
		t.Fatalf("resolve legacy unknown: %v %q %q", err, out, diag)
	}
	telegramEventually(t, "migrated input on original session", func() bool {
		value, err := store.ReadSnapshot(workspace, id)
		return err == nil && len(value.Turns) == 2 && value.Turns[1].Status == session.StatusCompleted
	})
}
