package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type activeJob struct {
	ChatID    int64     `json:"chat_id"`
	RequestID string    `json:"request_id"`
	Stage     string    `json:"stage"`
	Since     time.Time `json:"since"`
}
type healthSnapshot struct {
	Version        int         `json:"schema_version"`
	RunID          string      `json:"run_id"`
	PID            int         `json:"pid"`
	Updated        time.Time   `json:"updated_at"`
	PollAt         time.Time   `json:"last_poll_at"`
	OffsetAt       time.Time   `json:"last_offset_at"`
	Offset         int64       `json:"offset"`
	PollError      string      `json:"poll_error,omitempty"`
	PollStage      string      `json:"poll_stage"`
	PollSince      time.Time   `json:"poll_stage_since"`
	SchedulerStage string      `json:"scheduler_stage"`
	SchedulerSince time.Time   `json:"scheduler_stage_since"`
	Pending        int         `json:"pending"`
	Slots          int         `json:"occupied_slots"`
	Parallel       int         `json:"max_parallel_chats"`
	LogFailed      bool        `json:"journal_failed"`
	Active         []activeJob `json:"active"`
}
type diagnosticHealth struct {
	mu    sync.Mutex
	value healthSnapshot
	jobs  map[string]activeJob
}

func newHealth(runID string) *diagnosticHealth {
	return &diagnosticHealth{value: healthSnapshot{Version: 1, RunID: runID, PID: os.Getpid()}, jobs: map[string]activeJob{}}
}
func (h *diagnosticHealth) event(name string, data map[string]any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now().UTC()
	switch name {
	case "polling_failed":
		h.value.PollError, _ = data["error"].(string)
	case "polling_recovered":
		h.value.PollError = ""
	case "offset_saved":
		h.value.Offset, _ = data["offset"].(int64)
		h.value.OffsetAt = now
	}
	id, _ := data["request_id"].(string)
	if id == "" {
		return
	}
	job, ok := h.jobs[id]
	if name == "job_started" {
		job = activeJob{RequestID: id, Since: now, Stage: "workspace"}
		job.ChatID, _ = data["chat_id"].(int64)
		ok = true
	}
	if !ok {
		return
	}
	stage := ""
	switch name {
	case "job_finished":
		delete(h.jobs, id)
		return
	case "job_status":
		stage, _ = data["status"].(string)
	case "reply_decision":
		stage = "reply_decision"
	case "horizon_model_request_started":
		stage = "model"
	case "horizon_tool_started":
		stage = "tool"
	case "horizon_tool_completed", "horizon_model_request_completed":
		stage = "horizon"
	case "delivery_started":
		stage = "sending"
	case "delivery_rate_limited":
		stage = "rate_limit"
	}
	if stage != "" && stage != job.Stage {
		job.Stage = stage
		job.Since = now
	}
	h.jobs[id] = job
}
func (h *diagnosticHealth) phase(poll bool, stage string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if poll {
		h.value.PollStage = stage
		h.value.PollSince = time.Now().UTC()
	} else {
		h.value.SchedulerStage = stage
		h.value.SchedulerSince = time.Now().UTC()
	}
}
func (h *diagnosticHealth) snapshot() healthSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	v := h.value
	v.Updated = time.Now().UTC()
	v.Active = []activeJob{}
	for _, j := range h.jobs {
		v.Active = append(v.Active, j)
	}
	sort.Slice(v.Active, func(i, j int) bool { return v.Active[i].RequestID < v.Active[j].RequestID })
	return v
}
func (g *bridge) healthLoop(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	lastError := ""
	for {
		v := g.health.snapshot()
		if g.journal != nil {
			g.journal.mu.Lock()
			v.LogFailed = g.journal.failed
			g.journal.mu.Unlock()
		}
		data, err := json.Marshal(v)
		if err == nil {
			err = atomicFile(filepath.Join(g.home, "gateway", "health.json"), data, 0600)
		}
		if err != nil && err.Error() != lastError {
			g.emit("error", "health_write_failed", map[string]any{"error": err.Error()})
			lastError = err.Error()
		}
		if err == nil && lastError != "" {
			g.emit("info", "health_write_recovered", nil)
			lastError = ""
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
func readHealth(home string) (healthSnapshot, error) {
	var v healthSnapshot
	f, err := os.Open(filepath.Join(home, "gateway", "health.json"))
	if err != nil {
		return v, err
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 128*1024))
	if err = decoder.Decode(&v); err != nil {
		return v, err
	}
	if v.Version != 1 {
		return v, fmt.Errorf("unsupported health snapshot")
	}
	return v, nil
}
func printHealth(a *app) {
	v, err := readHealth(a.home)
	if err != nil {
		fmt.Fprintln(a.out, "Diagnostics: unavailable (no valid daemon snapshot)")
		return
	}
	state := "fresh"
	if !telegramRunning(a.home) || time.Since(v.Updated) > 3*time.Second {
		state = "stale"
	}
	fmt.Fprintf(a.out, "Diagnostics: %s; run %s; PID %d; updated %s\nLast successful polling: %s\nLast offset advance: %s (offset %d)\nQueue: %d pending; slots %d/%d\nJournal failed: %t\n", state, v.RunID, v.PID, v.Updated.Format(time.RFC3339), healthTime(v.PollAt), healthTime(v.OffsetAt), v.Offset, v.Pending, v.Slots, v.Parallel, v.LogFailed)
	fmt.Fprintf(a.out, "Polling stage: %s (%s)\nScheduler stage: %s (%s)\n", v.PollStage, healthAge(v.PollSince), v.SchedulerStage, healthAge(v.SchedulerSince))
	if v.PollError != "" {
		fmt.Fprintln(a.out, "Polling error:", safeLine(v.PollError))
	}
	for _, j := range v.Active {
		fmt.Fprintf(a.out, "Active: chat %d; request %s; %s (%s)\n", j.ChatID, j.RequestID, j.Stage, healthAge(j.Since))
	}
}
func healthTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Format(time.RFC3339)
}
func healthAge(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return time.Since(t).Round(time.Second).String()
}
