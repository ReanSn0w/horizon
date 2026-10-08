package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ReanSn0w/horizon/internal/session"
)

const (
	minIdleCheckpointBytes = 128 << 10
	idleCompactRetryDelay  = time.Hour
	idleCompactTimeout     = 5 * time.Minute
)

var errIdleChatChanged = errors.New("idle chat changed before compaction")

type idleCompactCandidate struct {
	boundary string
	bytes    int
}

func idleChatEligible(c *chat, now time.Time, after time.Duration) bool {
	if c == nil || !c.Available || c.Workspace == "" || c.Session == "" || after <= 0 {
		return false
	}
	last := c.LastReceivedAt
	if last.IsZero() {
		last = c.LastAt
	}
	if last.IsZero() || now.Before(last) || now.Sub(last) < after {
		return false
	}
	for _, j := range c.Jobs {
		if pending(j.Status) {
			return false
		}
	}
	return true
}

func idleCompactDue(c *chat, now time.Time, after time.Duration) bool {
	return c != nil && idleChatEligible(c, now, after) && (c.LastCompactAttemptAt.IsZero() || now.Sub(c.LastCompactAttemptAt) >= idleCompactRetryDelay)
}

func (g *bridge) inspectIdleSession(c *chat) (idleCompactCandidate, string, error) {
	store := session.NewStore(g.home)
	workspace, err := store.ResolveWorkspace(c.Workspace)
	if err != nil {
		return idleCompactCandidate{}, "workspace_error", err
	}
	locked, err := store.LockSession(workspace, c.Session)
	if err != nil {
		if errors.Is(err, session.ErrBusy) {
			return idleCompactCandidate{}, "session_busy", nil
		}
		return idleCompactCandidate{}, "session_error", err
	}
	defer locked.Close()
	value, err := locked.Load()
	if err != nil {
		return idleCompactCandidate{}, "session_error", err
	}
	index, ok := value.LastCompletedTurn()
	if !ok {
		return idleCompactCandidate{}, "no_completed_turn", nil
	}
	if value.LatestCompletedTurnCompacted() {
		return idleCompactCandidate{}, "already_compacted", nil
	}
	boundary := value.Turns[index].ID
	for i := len(value.Checkpoints) - 1; i >= 0; i-- {
		checkpoint := value.Checkpoints[i]
		if checkpoint.TurnID != boundary {
			continue
		}
		size := 2
		for _, item := range checkpoint.Items {
			size += len(item) + 1
		}
		if size < minIdleCheckpointBytes {
			return idleCompactCandidate{boundary: boundary, bytes: size}, "small_checkpoint", nil
		}
		return idleCompactCandidate{boundary: boundary, bytes: size}, "", nil
	}
	return idleCompactCandidate{boundary: boundary}, "missing_checkpoint", nil
}

func (g *bridge) startIdleCompaction(ctx context.Context, c *chat, cfg settings, now time.Time, done chan<- int64, wg *sync.WaitGroup) (context.CancelFunc, bool) {
	candidate, reason, err := g.inspectIdleSession(c)
	fields := map[string]any{"chat_id": c.ID, "session_id": c.Session, "boundary_turn_id": candidate.boundary, "checkpoint_bytes": candidate.bytes}
	if err != nil {
		fields["reason"] = reason
		fields["error"] = err.Error()
		g.emit("error", "idle_compaction_error", fields)
		return nil, false
	}
	if reason != "" {
		fields["reason"] = reason
		g.emit("info", "idle_compaction_skipped", fields)
		return nil, false
	}
	g.emit("info", "idle_compaction_candidate", fields)
	if err := g.store.update(func(value *state) error {
		current, resolveErr := resolveChat(value, strconv.FormatInt(c.ID, 10))
		if resolveErr != nil || current.Session != c.Session || !idleCompactDue(current, now, cfg.idleCompactAfter()) {
			return errIdleChatChanged
		}
		current.LastCompactAttemptAt = now.UTC()
		return nil
	}); err != nil {
		fields["reason"] = "chat_changed"
		if !errors.Is(err, errIdleChatChanged) {
			fields["reason"] = "state_error"
			fields["error"] = err.Error()
			g.emit("error", "idle_compaction_error", fields)
		} else {
			g.emit("info", "idle_compaction_skipped", fields)
		}
		return nil, false
	}
	runCtx, cancel := context.WithTimeout(ctx, idleCompactTimeout)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		defer func() { done <- c.Origin }()
		value, err := g.store.snapshot()
		if err != nil {
			g.emit("error", "idle_compaction_error", map[string]any{"chat_id": c.ID, "session_id": c.Session, "reason": "state_error", "error": err.Error()})
			return
		}
		current, err := resolveChat(&value, strconv.FormatInt(c.ID, 10))
		if err != nil || current.Session != c.Session || !idleChatEligible(current, time.Now(), cfg.idleCompactAfter()) {
			g.emit("info", "idle_compaction_skipped", map[string]any{"chat_id": c.ID, "session_id": c.Session, "boundary_turn_id": candidate.boundary, "reason": "chat_changed"})
			return
		}
		started := time.Now()
		g.emit("info", "idle_compaction_started", fields)
		output, err := g.run(runCtx, c.Workspace, []string{"--home", g.home, "sessions", "compact", "--id", c.Session, "--if-new-turn", "--mode", "jsonl"}, "")
		resultFields := map[string]any{"chat_id": c.ID, "session_id": c.Session, "boundary_turn_id": candidate.boundary, "duration_ms": time.Since(started).Milliseconds()}
		if err != nil {
			if errors.Is(runCtx.Err(), context.Canceled) {
				resultFields["reason"] = "cancelled"
				g.emit("info", "idle_compaction_skipped", resultFields)
				return
			}
			resultFields["reason"] = "process_error"
			resultFields["error"] = err.Error()
			g.emit("error", "idle_compaction_error", resultFields)
			return
		}
		compacted, err := parseIdleCompactResult(output)
		if err != nil {
			resultFields["reason"] = "invalid_result"
			g.emit("error", "idle_compaction_error", resultFields)
			return
		}
		if !compacted {
			resultFields["reason"] = "already_compacted"
			g.emit("info", "idle_compaction_skipped", resultFields)
			return
		}
		g.emit("info", "idle_compaction_completed", resultFields)
	}()
	return cancel, true
}

func parseIdleCompactResult(output string) (bool, error) {
	decoder := json.NewDecoder(strings.NewReader(output))
	for {
		var event struct {
			Type string `json:"type"`
			Data struct {
				Compacted    *bool  `json:"compacted"`
				CompactionID string `json:"compaction_id"`
			} `json:"data"`
		}
		if err := decoder.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				return false, errors.New("missing compaction completion event")
			}
			return false, err
		}
		if event.Type == "compaction_completed" {
			return event.Data.CompactionID != "", nil
		}
	}
}
