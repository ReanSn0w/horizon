package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ReanSn0w/horizon/internal/responses"
	"github.com/ReanSn0w/horizon/internal/session"
	"github.com/ReanSn0w/horizon/internal/timer"
)

// The provider is scripted: this verifies the nap task can use global session
// commands and memory in an isolated home, without claiming a model will make
// the same editorial choices on every run.
func TestNapTaskOfflinePlumbing(t *testing.T) {
	binary := buildBinary(t)
	home, nap, other := t.TempDir(), t.TempDir(), t.TempDir()
	var mu sync.Mutex
	calls := 0
	var sessionID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var req responses.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		calls++
		if calls == 2 || calls == 3 {
			found := false
			for _, item := range req.Input {
				found = found || strings.Contains(string(item), "function_call_output")
			}
			if !found {
				t.Errorf("step %d did not receive prior command result: %+v", calls, req.Input)
			}
			if calls == 2 {
				seenID := false
				for _, item := range req.Input {
					seenID = seenID || strings.Contains(string(item), strings.TrimSpace(sessionID))
				}
				if !seenID {
					t.Error("global list did not return the other workspace session")
				}
			}
		}
		var output []json.RawMessage
		switch calls {
		case 1:
			output = []json.RawMessage{napTool(t, "list", "shell_exec", map[string]any{"command": "horizon sessions list --all --since 24h --json", "timeout_ms": nil, "max_output_chars": nil})}
		case 2:
			output = []json.RawMessage{napTool(t, "read", "shell_exec", map[string]any{"command": "horizon sessions read --all --id " + strings.TrimSpace(sessionID), "timeout_ms": nil, "max_output_chars": nil})}
		case 3:
			output = []json.RawMessage{napTool(t, "note", "memory__add", map[string]any{"scope": "agent", "text": "Observed repeated request: keep replies concise"})}
		default:
			output = message("done")
		}
		writeCompleted(w, calls, output)
	}))
	defer server.Close()
	writeConfig(t, home, server.URL)
	configPath := filepath.Join(home, "config.yaml")
	base, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, append(base, []byte("\nmemory:\n  enabled: true\n  threshold: 5\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	out, diag, err := run(binary, other, "", "--home", home, "sessions", "create")
	if err != nil {
		t.Fatal(out, diag, err)
	}
	mu.Lock()
	sessionID = out
	mu.Unlock()
	template, err := os.ReadFile(filepath.Join("..", "..", "examples", "timers", "nap.md"))
	if err != nil {
		t.Fatal(err)
	}
	task := string(template)
	for _, required := range []string{"pending.json", "processed.json", "sessions list --all --since 24h --json", "sessions read --all --id ID --turns 8", "memory__add"} {
		if !strings.Contains(task, required) {
			t.Fatalf("nap task missing %q", required)
		}
	}
	if index := strings.Index(task, "\n---\n"); index >= 0 {
		task = task[index+5:]
	}
	for runNumber := 0; runNumber < 2; runNumber++ {
		out, diag, err := run(binary, nap, "", "--home", home, "resume", "--access", "full", "--mode", "plain", "-m", task)
		if err != nil || out != "done\n" {
			t.Fatalf("nap %d: %q %q %v", runNumber, out, diag, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(home, "memory", "agent.json"))
	if err != nil || strings.Count(string(data), "Observed repeated request") != 1 {
		t.Fatalf("memory: %v %q", err, data)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 5 {
		t.Fatalf("unexpected response count %d", calls)
	}
	if _, err := os.Stat(filepath.Join(home, "bin", "horizon-telegram")); !os.IsNotExist(err) {
		t.Fatal("telegram plugin appeared in test home")
	}
}

func napTool(t *testing.T, id, name string, args any) json.RawMessage {
	t.Helper()
	arguments, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	item, err := json.Marshal(map[string]any{"type": "function_call", "call_id": id, "name": name, "arguments": string(arguments)})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

// These scripted choices test the available plumbing, not model judgement or a
// Go implementation of nap's editorial policy (the policy lives in its prompt).
func TestNapPersistentNotesAndDraftsOffline(t *testing.T) {
	binary := buildBinary(t)
	home, workspace, sourceWorkspace := t.TempDir(), t.TempDir(), t.TempDir()
	installTimer(t, home)
	installAgentCLI(t, home)
	store := session.NewStore(home)
	source, err := store.ResolveWorkspace(sourceWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	locked, value, err := store.AcquireForResume(source, created.SessionID, "source")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	value.LastAccessedAt = old
	value.Turns = append(value.Turns, session.Turn{ID: "source-turn", Status: session.StatusCompleted, StartedAt: old, CompletedAt: &old, Model: session.ModelProfile{Name: "test", Model: "test", CompactThreshold: 1000}, Messages: []session.Message{{Role: "user", Text: "Keep the answer brief."}}})
	if err := locked.Save(value); err != nil {
		t.Fatal(err)
	}
	locked.Close()
	timerSession, err := store.CreateTimer(source, session.TimerOrigin{TimerID: "nap", TaskID: strings.Repeat("a", 32)})
	if err != nil {
		t.Fatal(err)
	}
	pending := "[\"" + created.SessionID + "\"]\n"
	write := func(name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("pending.json", pending)
	write("processed.json", "{}\n")
	note := "observation; confirmations=1; source=" + created.SessionID + "/source-turn"
	commands := []string{
		// The old queued ID is still readable outside discovery's 24-hour window.
		"set -e\ncat pending.json\nhorizon sessions read --all --id " + created.SessionID + " --turns 8\nprintf '%s\\n' " + shellQuote(note) + " > .notes-next\nmv .notes-next notes.md\nprintf '%s\\n' " + shellQuote("{\""+created.SessionID+"\":\"source-turn\"}") + " > .processed-next\nmv .processed-next processed.json\nprintf '[]\\n' > .pending-next\nmv .pending-next pending.json",
		// A second look at the same source only reads existing evidence.
		"cat notes.md processed.json; horizon sessions read --all --id " + created.SessionID + " --turns 8",
		`set -e
mkdir -p "$HORIZON_HOME/skills/nap-review" "$HORIZON_HOME/timers"
cat > "$HORIZON_HOME/skills/nap-review/SKILL.md" <<'SKILL'
---
name: nap-review
description: Review repeated evidence before changing instructions
---
Check independent source turn IDs and contradictions.
SKILL
horizon skills validate --id nap-review
cat > "$HORIZON_HOME/timers/nap-draft.md" <<'TIMER'
---
name: Draft
description: Proposed recurring review
schedule:
  every: 24h
  timezone: UTC
enabled: false
---
Review local notes when explicitly enabled.
TIMER
horizon agent timer validate nap-draft`,
		// Failed atomic replacement must not advance the cursor or pending list.
		"printf 'replacement' > .notes-next && mv .notes-next absent-directory/notes.md && printf '{}' > processed.json && printf '[]' > pending.json",
		// A corrupt state is surfaced, not overwritten by initialization.
		"cat pending.json processed.json",
	}
	var call atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req responses.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		n := int(call.Add(1))
		if n > 2*len(commands) {
			t.Errorf("unexpected request %d", n)
			writeCompleted(w, n, message("unexpected"))
			return
		}
		if n%2 == 1 {
			writeCompleted(w, n, []json.RawMessage{napTool(t, "nap-action", "shell_exec", map[string]any{"command": commands[(n-1)/2], "timeout_ms": nil, "max_output_chars": nil})})
			return
		}
		data, _ := json.Marshal(req.Input)
		expected := map[int]string{2: "Keep the answer brief.", 4: "confirmations=1", 6: "valid (disabled)", 8: "exit_code", 10: "broken-state"}[n]
		if !strings.Contains(string(data), expected) {
			t.Errorf("call %d missing %q: %s", n, expected, data)
		}
		writeCompleted(w, n, message("checked"))
	}))
	defer server.Close()
	writeConfig(t, home, server.URL)
	cli := func(args ...string) string {
		t.Helper()
		out, diag, err := run(binary, workspace, "", append([]string{"--home", home}, args...)...)
		if err != nil {
			t.Fatalf("%v: %s %s %v", args, out, diag, err)
		}
		return out
	}
	ordinary := cli("sessions", "list", "--all", "--json")
	if strings.Contains(ordinary, timerSession.SessionID) || !strings.Contains(ordinary, created.SessionID) {
		t.Fatal(ordinary)
	}
	recent := cli("sessions", "list", "--all", "--since", "24h", "--json")
	if strings.Contains(recent, created.SessionID) {
		t.Fatal("old source unexpectedly inside recent discovery", recent)
	}
	all := cli("sessions", "list", "--all", "--include-timers", "--json")
	if !strings.Contains(all, timerSession.SessionID) || !strings.Contains(all, `"timer_id":"nap"`) {
		t.Fatal(all)
	}
	cli("sessions", "read", "--all", "--id", timerSession.SessionID)
	prompt, err := os.ReadFile(filepath.Join("..", "..", "examples", "timers", "nap.md"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range commands {
		if i == 3 {
			write("pending.json", pending)
		}
		if i == 4 {
			write("processed.json", "broken-state")
		}
		cli("sessions", "create")
		cli("resume", "--access", "full", "--mode", "plain", "-m", string(prompt))
		data, err := os.ReadFile(filepath.Join(workspace, "notes.md"))
		if err != nil || string(data) != note+"\n" {
			t.Fatalf("notes changed on step %d: %s %v", i, data, err)
		}
		if i < 2 {
			if _, err := os.Stat(filepath.Join(home, "skills", "nap-review")); !os.IsNotExist(err) {
				t.Fatal("one observation created a skill")
			}
		}
		if i >= 3 {
			data, err := os.ReadFile(filepath.Join(workspace, "pending.json"))
			if err != nil || string(data) != pending {
				t.Fatalf("lost pending source: %s %v", data, err)
			}
			data, err = os.ReadFile(filepath.Join(workspace, "processed.json"))
			if err != nil || (i == 4 && string(data) != "broken-state") || (i == 3 && !strings.Contains(string(data), "source-turn")) {
				t.Fatalf("lost state: %s %v", data, err)
			}
		}
	}
	def, err := timer.LoadFile(home, "nap-draft")
	if err != nil || def.Enabled {
		t.Fatalf("draft enabled: %+v %v", def, err)
	}
	if _, err := os.Stat(filepath.Join(home, "workspace", "nap-draft")); !os.IsNotExist(err) {
		t.Fatal("validation created draft workspace")
	}
	if call.Load() != 10 {
		t.Fatalf("unexpected provider calls: %d", call.Load())
	}
}
