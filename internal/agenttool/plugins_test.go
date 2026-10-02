package agenttool

import (
	"context"
	"encoding/json"
	"github.com/ReanSn0w/horizon/internal/plugins"
	"github.com/ReanSn0w/horizon/internal/session"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExtensionAccessAndJournal(t *testing.T) {
	for _, access := range []string{"read", "write", "full"} {
		t.Run(access, func(t *testing.T) {
			store := session.NewStore(t.TempDir())
			ws, err := store.ResolveWorkspace(t.TempDir())
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
			turn := strings.Repeat("a", 32)
			if err := locked.StartTurn(session.Turn{ID: turn, Status: session.StatusActive, StartedAt: time.Now(), Model: session.ModelProfile{Name: "test", Model: "test", CompactThreshold: 1000}}); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(ws.Dir, "launched")
			path := filepath.Join(ws.Dir, "plugin")
			if err := os.WriteFile(path, []byte("#!/bin/sh\ncat >/dev/null\ntouch '"+marker+"'\necho diagnostic >&2\necho '{\"ok\":true,\"data\":{\"saved\":true}}'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			executor := NewExecutor(ws.Dir, store.ArtifactsDir(ws, created.SessionID), locked, turn)
			executor.SetCommandReview(access, nil)
			extension := plugins.Extension{Name: "example", Path: path, Request: plugins.Request{Home: store.Home, Workspace: ws.Dir, WorkspaceID: ws.ID}, Description: plugins.Description{Tools: []plugins.Tool{{Name: "add", Description: "test", Effect: "write_home", Parameters: objectSchema(properties(field("text", "string")), "text")}}}}
			if err := executor.SetExtensions(created.SessionID, []plugins.Extension{extension}); err != nil {
				t.Fatal(err)
			}
			if len(executor.Definitions()) != 3 {
				t.Fatal("registry definitions")
			}
			result, err := executor.Execute(context.Background(), "call", "example__add", json.RawMessage(`{"text":"hello"}`))
			if err != nil {
				t.Fatal(err)
			}
			_, statErr := os.Stat(marker)
			if access == "read" {
				if statErr == nil || !strings.Contains(string(result), "access_denied") {
					t.Fatalf("denial: %s %v", result, statErr)
				}
			} else {
				if statErr != nil || !strings.Contains(string(result), `"saved":true`) {
					t.Fatalf("write: %s %v", result, statErr)
				}
			}
			value, err := locked.Load()
			if err != nil {
				t.Fatal(err)
			}
			call := value.Turns[0].ToolCalls[0]
			if call.ResultState != session.ToolResultKnown {
				t.Fatal(call)
			}
			if access != "read" && (len(call.Artifacts) != 1) {
				t.Fatal("missing diagnostics")
			}
			removed, err := executor.Execute(context.Background(), "old", "removed__add", json.RawMessage(`{}`))
			if err != nil || !strings.Contains(string(removed), "unknown_tool") {
				t.Fatal(string(removed), err)
			}
		})
	}
}
