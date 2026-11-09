package e2e

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ReanSn0w/horizon/internal/session"
)

func TestCompactIfNewTurnSkipsAlreadyCompactedBoundary(t *testing.T) {
	binary := buildBinary(t)
	workspace, home := t.TempDir(), t.TempDir()
	var compactCalls, responseCalls atomic.Int32
	var failNextCompact atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/responses/compact" {
			id := compactCalls.Add(1)
			if failNextCompact.Swap(false) {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":{"message":"test refusal"}}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":"compact-%d","output":[{"type":"compaction","id":"c-%d","encrypted_content":"opaque"}]}`, id, id)
			return
		}
		writeCompleted(w, int(responseCalls.Add(1)), message("answer"))
	}))
	defer server.Close()
	writeConfig(t, home, server.URL)
	if _, _, err := run(binary, workspace, "", "--home", home, "resume", "-m", "first"); err != nil {
		t.Fatal(err)
	}
	store := session.NewStore(home)
	resolved, err := store.ResolveWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.List(resolved, false)
	if err != nil || len(items) != 1 {
		t.Fatalf("sessions=%d err=%v", len(items), err)
	}
	id := items[0].Session.SessionID
	firstAccess := items[0].Session.LastAccessedAt
	compact := func() string {
		t.Helper()
		out, _, err := run(binary, workspace, "", "--home", home, "sessions", "compact", "--id", id, "--if-new-turn", "--mode", "jsonl")
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	failNextCompact.Store(true)
	if _, _, err := run(binary, workspace, "", "--home", home, "sessions", "compact", "--id", id, "--if-new-turn"); err == nil || compactCalls.Load() != 1 {
		t.Fatal("API refusal was treated as a completed compaction")
	}
	if out := compact(); !strings.Contains(out, `"compaction_id":"c-2"`) || compactCalls.Load() != 2 {
		t.Fatalf("first compaction=%q calls=%d", out, compactCalls.Load())
	}
	if out := compact(); !strings.Contains(out, `"compacted":false`) || compactCalls.Load() != 2 {
		t.Fatalf("duplicate compaction=%q calls=%d", out, compactCalls.Load())
	}
	items, err = store.List(resolved, false)
	if err != nil || !items[0].Session.LastAccessedAt.Equal(firstAccess) {
		t.Fatal("background compaction changed last_accessed_at", err)
	}
	locked, err := store.LockSession(resolved, id)
	if err != nil {
		t.Fatal(err)
	}
	_, _, busyErr := run(binary, workspace, "", "--home", home, "sessions", "compact", "--id", id, "--if-new-turn")
	_ = locked.Close()
	if busyErr == nil || compactCalls.Load() != 2 {
		t.Fatal("compaction entered a locked session")
	}
	if _, _, err := run(binary, workspace, "", "--home", home, "resume", "--session", id, "-m", "second"); err != nil {
		t.Fatal(err)
	}
	if out := compact(); !strings.Contains(out, `"compaction_id":"c-3"`) || compactCalls.Load() != 3 {
		t.Fatalf("new turn compaction=%q calls=%d", out, compactCalls.Load())
	}
	if out, _, err := run(binary, workspace, "", "--home", home, "sessions", "compact", "--id", id); err != nil || !strings.Contains(out, "сжата") || compactCalls.Load() != 4 {
		t.Fatalf("manual compaction=%q calls=%d err=%v", out, compactCalls.Load(), err)
	}
}
