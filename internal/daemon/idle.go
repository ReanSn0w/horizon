package daemon

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"time"

	"github.com/ReanSn0w/horizon/internal/session"
)

// Idle maintenance shares the dialog's queue and session lease.
func (q *Queue) submitIdle(r Request, key, digest string) (*Task, error) {
	if r.Caller.Kind != "adapter" || r.Route == "" || r.TaskID != "" {
		return nil, fail("invalid_request", "idle compaction requires an adapter dialog route")
	}
	var root *Task
	for _, t := range q.state.Tasks {
		if t.Owner != owner(r.Caller) || t.Route != r.Route {
			continue
		}
		if t.State == "running" || t.State == "queued" || q.running[t.ID] != nil || t.State == "unknown" && !t.IdleCompact && !t.InputResolved {
			return nil, fail("dialog_busy", "dialog has unfinished tasks")
		}
		if t.ActiveInput && t.ID == t.Child {
			root = t
		}
	}
	if root == nil || root.Session == "" {
		return nil, fail("not_found", "dialog has no existing session")
	}
	if root.Workspace != r.Caller.Workspace {
		return nil, fail("route_conflict", "dialog workspace does not match")
	}
	if q.queueFull() {
		return nil, fail("queue_full", "pending queue limit reached")
	}
	var result *Task
	err := q.transaction(func() error {
		id := newID()
		q.state.Sequence++
		result = &Task{ID: id, Child: root.Child, Owner: root.Owner, Workspace: root.Workspace,
			Session: root.Session, Access: root.Access, Depth: root.Depth, State: "queued", Route: root.Route,
			Sequence: q.state.Sequence, IdleCompact: true}
		q.state.Tasks[id] = result
		q.event(result, "idle_compact")
		q.state.Operations[key] = Operation{Hash: digest, TaskID: id}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return summary(result), nil
}

func (q *Queue) cancelIdle(r Request, target *Task, key, digest string) (*Task, error) {
	if r.Caller.Kind != "adapter" || !target.IdleCompact {
		return nil, fail("access_denied", "idle cancellation only accepts adapter compaction tasks")
	}
	err := q.transaction(func() error {
		if target.State == "running" || target.State == "queued" {
			q.cancelIdleTask(target)
		}
		q.state.Operations[key] = Operation{Hash: digest, TaskID: target.ID}
		return nil
	})
	if err != nil {
		return nil, err
	}
	q.stopIdleProcess(target)
	return summary(target), nil
}

func (q *Queue) cancelIdleTask(t *Task) {
	t.State = "cancelled"
	q.event(t, "idle_compact")
}

func (q *Queue) stopIdleProcess(t *Task) {
	if cancel := q.running[t.ID]; cancel != nil {
		cancel()
	}
}

func (q *Queue) runningDialogs(r Request) (any, error) {
	c, err := q.caller(r.Caller)
	if err != nil {
		return nil, err
	}
	if c.Kind != "adapter" {
		return nil, fail("access_denied", "running dialogs are adapter-only")
	}
	tasks := []*Task{}
	for id := range q.running {
		if task := q.state.Tasks[id]; task.Owner == owner(c) && task.Route != "" && task.State == "running" {
			tasks = append(tasks, summary(task))
		}
	}
	return map[string]any{"tasks": tasks}, nil
}

const minimumIdleCheckpointBytes = 128 << 10

func compactIdle(ctx context.Context, home string, t Task) Outcome {
	store := session.NewStore(home)
	workspace, err := store.ResolveWorkspace(t.Workspace)
	if err != nil {
		return Outcome{Err: err}
	}
	value, err := store.ReadSnapshot(workspace, t.Session)
	if err != nil {
		return Outcome{Err: err}
	}
	index, ok := value.LastCompletedTurn()
	if !ok || value.LatestCompletedTurnCompacted() {
		return Outcome{}
	}
	for i := len(value.Checkpoints) - 1; i >= 0; i-- {
		checkpoint := value.Checkpoints[i]
		if checkpoint.TurnID != value.Turns[index].ID {
			continue
		}
		size := 2
		for _, item := range checkpoint.Items {
			size += len(item) + 1
		}
		if size < minimumIdleCheckpointBytes {
			return Outcome{}
		}
		ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		binary := filepath.Join(home, "bin", "horizon")
		code, err := Process(ctx, binary, []string{"--home", home, "sessions", "compact", "--id", t.Session, "--if-new-turn", "--mode", "jsonl"},
			childEnv(home, "read"), t.Workspace, "", io.Discard)
		return Outcome{ExitCode: code, Err: err}
	}
	return Outcome{Err: errors.New("completed session has no checkpoint")}
}
