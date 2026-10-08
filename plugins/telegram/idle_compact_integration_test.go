package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/session"
	"gopkg.in/yaml.v3"
)

func TestIdleCompactionWithBuiltHorizon(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "horizon")
	build := exec.Command("go", "build", "-o", binary, "../..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Horizon: %v: %s", err, output)
	}
	home := readyHome(t)
	var compactCalls, responseCalls atomic.Int32
	var sawCompacted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/responses/compact" {
			compactCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"id":"compact-response","output":[{"type":"compaction","id":"compact-opaque","encrypted_content":"opaque"}]}`)
			return
		}
		var body struct {
			Input []json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode resume: %v", err)
		}
		for _, item := range body.Input {
			if strings.Contains(string(item), "compact-opaque") {
				sawCompacted.Store(true)
			}
		}
		n := responseCalls.Add(1)
		message, _ := json.Marshal(map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "reply"}}})
		response, _ := json.Marshal(map[string]any{"id": fmt.Sprintf("response-%d", n), "status": "completed", "output": []json.RawMessage{message}})
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", response)
	}))
	defer server.Close()
	if _, err := config.UpdateDocument(home, func(doc *yaml.Node) (bool, error) {
		putNode(doc.Content[0], "provider", encodedNode(map[string]string{"url": server.URL, "key": "unused"}))
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadSettings(home, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.Workspace, 0700); err != nil {
		t.Fatal(err)
	}
	base, err := filepath.EvalSymlinks(cfg.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(base, "telegram_99_1")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".horizon-gateway.json"), []byte(`{"Bot":99,"Chat":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	id := idleSession(t, home, workspace, minIdleCheckpointBytes)
	s := newStore(home, 99)
	last := time.Now().Add(-13 * time.Hour)
	if err := s.update(func(v *state) error {
		v.Chats["1"] = &chat{ID: 1, Origin: 1, Type: "private", Available: true, Workspace: workspace, Session: id, LastReceivedAt: last}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	g := &bridge{home: home, store: s, run: processRunner(binary, home)}
	c := &chat{ID: 1, Origin: 1, Type: "private", Available: true, Workspace: workspace, Session: id, LastReceivedAt: last}
	done := make(chan int64, 1)
	var wg sync.WaitGroup
	stop, started := g.startIdleCompaction(context.Background(), c, cfg, time.Now(), done, &wg)
	if !started {
		t.Fatal("idle compaction did not start")
	}
	defer stop()
	<-done
	wg.Wait()
	if compactCalls.Load() != 1 {
		t.Fatalf("compact API calls = %d", compactCalls.Load())
	}
	store := session.NewStore(home)
	resolved, err := store.ResolveWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := store.LockSession(resolved, id)
	if err != nil {
		t.Fatal(err)
	}
	value, err := locked.Load()
	_ = locked.Close()
	if err != nil || !value.LatestCompletedTurnCompacted() {
		t.Fatalf("compact result was not saved: %v", err)
	}
	if _, reason, err := g.inspectIdleSession(c); err != nil || reason != "already_compacted" {
		t.Fatalf("repeat inspection reason=%q err=%v", reason, err)
	}
	input := record{ID: 1, Author: 1, Seq: 1, Text: "hello"}
	j := &job{ID: "next", ChatID: 1, Status: "evaluating", Input: input}
	if err := s.update(func(v *state) error {
		v.Chats["1"].LastReceivedAt = time.Now().UTC()
		v.Chats["1"].History = append(v.Chats["1"].History, input)
		v.Chats["1"].Jobs = append(v.Chats["1"].Jobs, j)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	g.cfg = cfg
	deliveredText := ""
	g.deliver = func(_ context.Context, c *chat, current *job) error {
		deliveredText = current.Response
		return g.setJob(c.ID, current.ID, func(_ *chat, saved *job) error { saved.Status = "sent"; return nil })
	}
	if err := g.process(context.Background(), c, j, cfg); err != nil {
		t.Fatal(err)
	}
	updated, err := s.snapshot()
	if err != nil || updated.Chats["1"].Jobs[0].Status != "sent" || deliveredText != "reply" || !sawCompacted.Load() || responseCalls.Load() != 1 || compactCalls.Load() != 1 {
		t.Fatalf("reply after compact: job=%+v delivered=%q err=%v response calls=%d compact calls=%d compact seen=%v", updated.Chats["1"].Jobs[0], deliveredText, err, responseCalls.Load(), compactCalls.Load(), sawCompacted.Load())
	}
}
