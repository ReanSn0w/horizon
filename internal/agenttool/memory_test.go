package agenttool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/memory"
	"github.com/ReanSn0w/horizon/internal/session"
)

func TestBuiltInMemoryAccessAndJournal(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	store := session.NewStore(home)
	ws, err := store.ResolveWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(ws)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := store.LockSession(ws, created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	turn := strings.Repeat("b", 32)
	if err := locked.StartTurn(session.Turn{ID: turn, Status: session.StatusActive, StartedAt: time.Now(), Model: session.ModelProfile{Name: "test", Model: "test", CompactThreshold: 1000}}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		DefaultModel: "coding",
		Models:       map[string]config.Model{"coding": {Provider: "default", Model: "test", CompactThreshold: 1000}},
		Providers:    map[string]config.Provider{"default": {URL: "https://example.test", Key: "unused"}},
		Memory: config.Memory{
			Enabled: true, Threshold: 5, Interval: 24 * time.Hour, Model: "coding",
			NoteLimit: 4096, SummaryLimit: 16384, ContextLimit: 16000,
			APITimeout: 45 * time.Second, RetryInterval: 5 * time.Minute,
			MaxPendingNotes: 256, MaxPendingBytes: 262144,
		},
	}
	service, err := memory.New(home, ws.Dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	executor := NewExecutor(ws.Dir, store.ArtifactsDir(ws, created.SessionID), locked, turn)
	executor.SetCommandReview("write", nil)
	if err := executor.SetMemory(created.SessionID, service); err != nil {
		t.Fatal(err)
	}
	if len(executor.Definitions()) != len(Definitions())+2 {
		t.Fatal("memory tools missing from registry")
	}
	if _, ok := executor.tools[MemoryAdd]; !ok {
		t.Fatalf("memory add key missing: %+v", executor.Definitions())
	}
	added, err := executor.Execute(context.Background(), "add", MemoryAdd, json.RawMessage(`{"scope":"workspace","text":"Stable workspace fact"}`))
	if err != nil || !strings.Contains(string(added), `"saved":true`) {
		t.Fatalf("add = %s, %v", added, err)
	}
	read, err := executor.Execute(context.Background(), "read", MemoryRead, json.RawMessage(`{"scope":"workspace"}`))
	if err != nil || !strings.Contains(string(read), "Stable workspace fact") {
		t.Fatalf("read = %s, %v", read, err)
	}
	executor.SetCommandReview("read", nil)
	denied, err := executor.Execute(context.Background(), "denied", MemoryAdd, json.RawMessage(`{"scope":"user","text":"Must not be saved"}`))
	if err != nil || !strings.Contains(string(denied), "access_denied") {
		t.Fatalf("denied = %s, %v", denied, err)
	}
	user, err := service.Read("user")
	if err != nil || len(user) != 0 {
		t.Fatalf("read access wrote memory: %v %v", user, err)
	}
	value, err := locked.Load()
	if err != nil || len(value.Turns[0].ToolCalls) != 3 {
		t.Fatalf("journal = %+v, %v", value.Turns, err)
	}
	for _, call := range value.Turns[0].ToolCalls {
		if call.ResultState != session.ToolResultKnown {
			t.Fatalf("call = %+v", call)
		}
	}
}
