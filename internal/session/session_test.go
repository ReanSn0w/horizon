package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWorkspaceIdentity(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(root, "workspace-link")
	if err := os.Symlink(workspace, symlink); err != nil {
		t.Fatal(err)
	}
	direct, err := NormalizeWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	linked, err := NormalizeWorkspace(symlink)
	if err != nil {
		t.Fatal(err)
	}
	if direct != linked || WorkspaceID(direct) != WorkspaceID(linked) {
		t.Fatalf("symlink identity differs: %q vs %q", direct, linked)
	}
	if len(WorkspaceID(direct)) != 32 {
		t.Fatalf("workspace hash length = %d", len(WorkspaceID(direct)))
	}

	subdir := filepath.Join(workspace, "subdir")
	if err := os.Mkdir(subdir, 0o700); err != nil {
		t.Fatal(err)
	}
	normalizedSubdir, err := NormalizeWorkspace(subdir)
	if err != nil {
		t.Fatal(err)
	}
	if WorkspaceID(direct) == WorkspaceID(normalizedSubdir) {
		t.Fatal("real subdirectory shares workspace identity")
	}

	moved := filepath.Join(root, "moved")
	if err := os.Rename(workspace, moved); err != nil {
		t.Fatal(err)
	}
	normalizedMoved, err := NormalizeWorkspace(moved)
	if err != nil {
		t.Fatal(err)
	}
	if WorkspaceID(direct) == WorkspaceID(normalizedMoved) {
		t.Fatal("moved workspace retained path identity")
	}
}

func TestWorkspaceCollisionAndRegistryRecovery(t *testing.T) {
	home := t.TempDir()
	firstDir := t.TempDir()
	secondDir := t.TempDir()
	store := NewStore(home)
	store.hash = func(string) string { return strings.Repeat("a", 32) }
	first, err := store.ResolveWorkspace(firstDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TouchWorkspace(first); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveWorkspace(secondDir); err == nil || !strings.Contains(err.Error(), "collision") {
		t.Fatalf("collision error = %v", err)
	}

	store = NewStore(home)
	workspace, err := store.ResolveWorkspace(firstDir)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(workspace)
	if err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(store.DialogsDir(), "workspaces.json")
	if err := os.WriteFile(registryPath, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := store.ResolveWorkspace(firstDir)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ID != workspace.ID {
		t.Fatalf("recovered workspace ID = %q", resolved.ID)
	}
	if _, err := store.ReadSnapshot(workspace, created.SessionID); err != nil {
		t.Fatalf("session lost after registry recovery: %v", err)
	}
}

func TestSessionLockAcrossProcessAndCrash(t *testing.T) {
	store, workspace := testStore(t)
	created, err := store.Create(workspace)
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Create(workspace)
	if err != nil {
		t.Fatal(err)
	}

	command := exec.Command(os.Args[0], "-test.run=^TestSessionLockHelper$")
	command.Env = append(os.Environ(),
		"HORIZON_LOCK_HELPER=1",
		"HORIZON_TEST_HOME="+store.Home,
		"HORIZON_TEST_WORKSPACE_ID="+workspace.ID,
		"HORIZON_TEST_WORKSPACE_DIR="+workspace.Dir,
		"HORIZON_TEST_SESSION_ID="+created.SessionID,
	)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "locked" {
		_ = command.Process.Kill()
		t.Fatalf("lock helper readiness = %q, %v", line, err)
	}

	if _, err := store.LockSession(workspace, created.SessionID); !IsBusy(err) {
		_ = command.Process.Kill()
		t.Fatalf("second lock error = %v", err)
	}
	independent, err := store.LockSession(workspace, other.SessionID)
	if err != nil {
		_ = command.Process.Kill()
		t.Fatalf("independent session lock: %v", err)
	}
	_ = independent.Close()
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()

	afterCrash, err := store.LockSession(workspace, created.SessionID)
	if err != nil {
		t.Fatalf("lock was not released after process death: %v", err)
	}
	_ = afterCrash.Close()
}

func TestSessionLockHelper(t *testing.T) {
	if os.Getenv("HORIZON_LOCK_HELPER") != "1" {
		t.Skip("helper process")
	}
	store := NewStore(os.Getenv("HORIZON_TEST_HOME"))
	workspace := Workspace{ID: os.Getenv("HORIZON_TEST_WORKSPACE_ID"), Dir: os.Getenv("HORIZON_TEST_WORKSPACE_DIR")}
	locked, err := store.LockSession(workspace, os.Getenv("HORIZON_TEST_SESSION_ID"))
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	fmt.Println("locked")
	select {}
}

func TestConcurrentCreatesAndRegistryUpdates(t *testing.T) {
	home := t.TempDir()
	store := NewStore(home)
	first, err := store.ResolveWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.ResolveWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var existing []string
	for index := 0; index < 4; index++ {
		created, err := store.Create(first)
		if err != nil {
			t.Fatal(err)
		}
		existing = append(existing, created.SessionID)
	}
	const count = 12
	var wait sync.WaitGroup
	errorsChannel := make(chan error, count+len(existing))
	for index := 0; index < count; index++ {
		workspace := first
		if index%2 == 1 {
			workspace = second
		}
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := store.Create(workspace)
			errorsChannel <- err
		}()
	}
	for _, id := range existing {
		id := id
		wait.Add(1)
		go func() {
			defer wait.Done()
			locked, _, err := store.AcquireForResume(first, id, "concurrent resume")
			if err == nil {
				err = locked.Close()
			}
			if err == nil {
				err = store.Delete(first, id)
			}
			errorsChannel <- err
		}()
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
	firstItems, err := store.List(first, false)
	if err != nil {
		t.Fatal(err)
	}
	secondItems, err := store.List(second, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstItems)+len(secondItems) != count {
		t.Fatalf("created %d sessions, want %d", len(firstItems)+len(secondItems), count)
	}
	registry, err := store.readRegistryUnlocked()
	if err != nil {
		t.Fatal(err)
	}
	if len(registry) != 2 {
		t.Fatalf("registry entries = %d, want 2", len(registry))
	}
}

func TestAtomicWriteFailureAndUnknownToolOutcome(t *testing.T) {
	store, workspace := testStore(t)
	created, err := store.Create(workspace)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := store.LockSession(workspace, created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	originalWrite := store.writeJSON
	store.writeJSON = func(path string, value any) error {
		if strings.HasSuffix(path, ".json") && !strings.HasSuffix(path, "workspaces.json") {
			if err := os.WriteFile(filepath.Join(filepath.Dir(path), ".stale-session.tmp"), []byte("partial"), 0o600); err != nil {
				return err
			}
			return errors.New("injected write failure")
		}
		return originalWrite(path, value)
	}
	changed := *created
	changed.Title = "must not persist"
	if err := locked.Save(&changed); err == nil {
		t.Fatal("injected session write succeeded")
	}
	store.writeJSON = originalWrite
	if err := locked.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.ReadSnapshot(workspace, created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Title != "Новый диалог" {
		t.Fatalf("failed write changed snapshot title to %q", snapshot.Title)
	}

	locked, _, err = store.AcquireForResume(workspace, created.SessionID, "task")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	turnID := strings.Repeat("b", 32)
	if err := locked.StartTurn(Turn{ID: turnID, Status: StatusActive, StartedAt: started, Model: testModel(), Messages: []Message{{Role: "user", Text: "task"}}}); err != nil {
		t.Fatal(err)
	}
	if err := locked.RecordToolCall(turnID, ToolCall{CallID: "call-1", Name: "shell_exec", Arguments: json.RawMessage(`{"command":"touch marker"}`), StartedAt: started}); err != nil {
		t.Fatal(err)
	}
	_ = locked.Close()

	locked, recovered, err := store.AcquireForResume(workspace, created.SessionID, "continue")
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	if recovered.Turns[0].Status != StatusFailed || recovered.Turns[0].ToolCalls[0].ResultState != ToolResultUnknown {
		t.Fatalf("interrupted turn = %+v", recovered.Turns[0])
	}
}

func TestManagedStartResultWriteFailureRecoversAsUnknown(t *testing.T) {
	store, workspace := testStore(t)
	created, err := store.Create(workspace)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := store.LockSession(workspace, created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	turnID := strings.Repeat("f", 32)
	if err := locked.StartTurn(Turn{ID: turnID, Status: StatusActive, StartedAt: now, Model: testModel()}); err != nil {
		t.Fatal(err)
	}
	if err := locked.RecordToolCall(turnID, ToolCall{CallID: "start", Name: "shell_exec", Arguments: json.RawMessage(`{"command":"sleep 5","timeout_ms":null,"max_output_chars":null,"yield_time_ms":1}`), StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	originalWrite := store.writeJSON
	store.writeJSON = func(string, any) error { return errors.New("injected result write failure") }
	err = locked.RecordToolResult(turnID, "start", json.RawMessage(`{"ok":true,"data":{"status":"running","process_id":"proc_test"}}`), now, nil)
	store.writeJSON = originalWrite
	if err == nil {
		t.Fatal("result write unexpectedly succeeded")
	}
	if err := locked.Close(); err != nil {
		t.Fatal(err)
	}
	locked, recovered, err := store.AcquireForResume(workspace, created.SessionID, "continue")
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	call := recovered.Turns[len(recovered.Turns)-1].ToolCalls[0]
	if call.ResultState != ToolResultUnknown || len(call.Result) != 0 {
		t.Fatalf("interrupted start call = %+v", call)
	}
}

func TestFailedCompactionWriteKeepsPreviousWindow(t *testing.T) {
	store, workspace := testStore(t)
	created, err := store.Create(workspace)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := store.LockSession(workspace, created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	now := time.Now().UTC()
	turnID := strings.Repeat("c", 32)
	if err := locked.StartTurn(Turn{ID: turnID, Status: StatusActive, StartedAt: now, Model: testModel()}); err != nil {
		t.Fatal(err)
	}
	original := []json.RawMessage{json.RawMessage(`{"role":"user","content":"before"}`)}
	if err := locked.CompleteTurn(turnID, StatusCompleted, nil, original, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	originalWrite := store.writeJSON
	store.writeJSON = func(string, any) error { return errors.New("injected compact write failure") }
	err = locked.RecordCompaction(Compaction{
		ID: "compact-failed", BoundaryTurnID: turnID, ModelProfile: "coding", CreatedAt: now.Add(2 * time.Second),
		Items: []json.RawMessage{json.RawMessage(`{"type":"compaction","id":"compact-failed","encrypted_content":"opaque"}`)},
	})
	store.writeJSON = originalWrite
	if err == nil {
		t.Fatal("compaction write unexpectedly succeeded")
	}
	after, err := locked.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Compactions) != 0 || len(after.Checkpoints) != 1 || !bytes.Contains(after.Checkpoints[0].Items[0], []byte("before")) {
		t.Fatalf("failed compaction changed continuation state: %+v", after)
	}
}

func TestForkBoundaryArtifactsAndDelete(t *testing.T) {
	store, workspace := testStore(t)
	source, err := store.Create(workspace)
	if err != nil {
		t.Fatal(err)
	}
	artifactDir := store.ArtifactsDir(workspace, source.SessionID)
	if err := os.MkdirAll(artifactDir, 0o700); err != nil {
		t.Fatal(err)
	}
	includedArtifact := filepath.Join(artifactDir, "call-b.stdout")
	excludedArtifact := filepath.Join(artifactDir, "call-d.stdout")
	if err := os.WriteFile(includedArtifact, []byte("included"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(excludedArtifact, []byte("excluded"), 0o600); err != nil {
		t.Fatal(err)
	}

	locked, err := store.LockSession(workspace, source.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC()
	completed := func(id string, offset time.Duration) Turn {
		at := base.Add(offset)
		return Turn{ID: id, Status: StatusCompleted, StartedAt: at, CompletedAt: &at, Model: testModel()}
	}
	nonSuccessful := func(id string, status Status, offset time.Duration, artifact string) Turn {
		at := base.Add(offset)
		return Turn{ID: id, Status: status, StartedAt: at, CompletedAt: &at, Model: testModel(), ToolCalls: []ToolCall{{CallID: "call-" + strings.ToLower(id), Name: "shell_exec", Arguments: json.RawMessage(`{}`), ResultState: ToolResultKnown, Result: json.RawMessage(`{"ok":true}`), StartedAt: at, CompletedAt: &at, Artifacts: []Artifact{{Kind: "stdout", Path: artifact}}}}}
	}
	a, b, c, d := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32), strings.Repeat("d", 32)
	value, err := locked.Load()
	if err != nil {
		t.Fatal(err)
	}
	value.Turns = []Turn{completed(a, 0), nonSuccessful(b, StatusCancelled, time.Second, includedArtifact), completed(c, 2*time.Second), nonSuccessful(d, StatusFailed, 3*time.Second, excludedArtifact)}
	value.Checkpoints = []ContextCheckpoint{{TurnID: a, CreatedAt: base, Items: []json.RawMessage{json.RawMessage(`{"a":1}`)}}, {TurnID: c, CreatedAt: base.Add(2 * time.Second), Items: []json.RawMessage{json.RawMessage(`{"c":1}`)}}}
	value.Compactions = []Compaction{{ID: "compact-c", BoundaryTurnID: c, ModelProfile: "coding", CreatedAt: base.Add(2 * time.Second), Items: []json.RawMessage{json.RawMessage(`{"opaque":"value"}`)}}}
	if err := locked.Save(value); err != nil {
		t.Fatal(err)
	}
	_ = locked.Close()

	forked, err := store.Fork(workspace, source.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(forked.Turns) != 3 || forked.Turns[0].ID != a || forked.Turns[1].ID != b || forked.Turns[2].ID != c {
		t.Fatalf("forked turn boundary = %+v", forked.Turns)
	}
	if len(forked.Compactions) != 1 || forked.Fork.TurnID != c {
		t.Fatalf("forked context metadata = %+v, origin=%+v", forked.Compactions, forked.Fork)
	}
	copiedPath := forked.Turns[1].ToolCalls[0].Artifacts[0].Path
	if copiedPath == includedArtifact || !strings.HasPrefix(copiedPath, store.ArtifactsDir(workspace, forked.SessionID)) {
		t.Fatalf("fork artifact path = %q", copiedPath)
	}
	if err := store.Delete(workspace, source.SessionID); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(copiedPath)
	if err != nil || string(data) != "included" {
		t.Fatalf("independent fork artifact = %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(store.WorkspaceDir(workspace.ID), source.SessionID+".lock")); err != nil {
		t.Fatalf("permanent source lock was removed: %v", err)
	}
}

func TestRawAPIFieldsSurviveReadWriteAndUnknownVersionIsUntouched(t *testing.T) {
	store, workspace := testStore(t)
	created, err := store.Create(workspace)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := store.LockSession(workspace, created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	created.Turns = []Turn{{ID: strings.Repeat("e", 32), Status: StatusCompleted, StartedAt: at, CompletedAt: &at, Model: testModel(), APIItems: []json.RawMessage{json.RawMessage(`{"type":"future","unknown":{"nested":true}}`)}}}
	created.Checkpoints = []ContextCheckpoint{{TurnID: created.Turns[0].ID, CreatedAt: at, Items: cloneRawMessages(created.Turns[0].APIItems)}}
	if err := locked.Save(created); err != nil {
		t.Fatal(err)
	}
	loaded, err := locked.Load()
	if err != nil {
		t.Fatal(err)
	}
	loaded.Title = "rewritten"
	if err := locked.Save(loaded); err != nil {
		t.Fatal(err)
	}
	_ = locked.Close()
	data, err := os.ReadFile(mustSessionPath(t, store, workspace, created.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"unknown"`) || !strings.Contains(string(data), `"nested": true`) {
		t.Fatalf("unknown API fields were lost: %s", data)
	}

	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	document["format_version"] = float64(99)
	bad, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := mustSessionPath(t, store, workspace, created.SessionID)
	if err := os.WriteFile(path, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	before := append([]byte(nil), bad...)
	if _, err := store.ReadSnapshot(workspace, created.SessionID); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unknown version error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("unknown-version file was rewritten")
	}
}

func TestLatestCompletedTurnCompacted(t *testing.T) {
	s := &Session{Turns: []Turn{{ID: "first", Status: StatusCompleted}}}
	if s.LatestCompletedTurnCompacted() {
		t.Fatal("uncompacted turn was reported as compacted")
	}
	s.Compactions = []Compaction{{BoundaryTurnID: "first"}}
	if !s.LatestCompletedTurnCompacted() {
		t.Fatal("latest completed turn was not recognized")
	}
	s.Turns = append(s.Turns, Turn{ID: "failed", Status: StatusFailed})
	if !s.LatestCompletedTurnCompacted() {
		t.Fatal("failed turn changed the compacted boundary")
	}
	s.Turns = append(s.Turns, Turn{ID: "second", Status: StatusCompleted})
	if s.LatestCompletedTurnCompacted() {
		t.Fatal("new completed turn was treated as already compacted")
	}
}

func testStore(t *testing.T) (*Store, Workspace) {
	t.Helper()
	store := NewStore(t.TempDir())
	workspace, err := store.ResolveWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store, workspace
}

func testModel() ModelProfile {
	return ModelProfile{Name: "coding", Model: "model-code", CompactThreshold: 1000}
}

func mustSessionPath(t *testing.T, store *Store, workspace Workspace, id string) string {
	t.Helper()
	path, err := store.SessionPath(workspace, id)
	if err != nil {
		t.Fatal(err)
	}
	return path
}
