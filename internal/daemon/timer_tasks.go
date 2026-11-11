package daemon

import (
	"errors"
	"regexp"
	"time"
)

const (
	TimerSource = "timer"
	FreshTask   = "fresh"
)

var errTimerWindowElapsed = errors.New("timer window ended before execution")

var timerSourceKey = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func (r Request) validateTimerFields() error {
	timer := r.Caller.Kind == "adapter" && r.Caller.Source == TimerSource
	if r.Mode != "" || r.SourceKey != "" || r.Deadline != nil || timer && r.Method == "submit" {
		if !timer || r.Method != "submit" || r.Mode != FreshTask ||
			!timerSourceKey.MatchString(r.SourceKey) || r.Caller.Session != "" ||
			r.Route != "" || r.Delivery != "" || r.Channel || r.Author != "" ||
			r.TaskID != "" || r.InputID != "" || r.TurnID != "" || r.Tree ||
			r.Deadline != nil && r.Deadline.IsZero() {
			return fail("invalid_request", "fresh timer submission requires timer source, source_key and workspace")
		}
	}
	if r.QueuedOnly && (!timer || r.Method != "cancel" || r.Tree) {
		return fail("invalid_request", "queued_only requires a timer cancellation of one task")
	}
	return nil
}

func (t *Task) isTimer() bool {
	return t.Mode == FreshTask && t.Owner == "adapter:"+TimerSource
}

func (t *Task) validTimerFields() bool {
	if t.Mode == "" {
		return t.SourceKey == "" && t.Deadline == nil
	}
	return t.isTimer() && t.Child == t.ID && t.Parent == "" && t.Depth == 1 &&
		timerSourceKey.MatchString(t.SourceKey) && t.Route == "" && !t.Channel && !t.ActiveInput && !t.IdleCompact &&
		(t.Deadline == nil || !t.Deadline.IsZero())
}

func (q *Queue) initTimerTask(t *Task, r Request) error {
	for _, existing := range q.state.Tasks {
		if existing.isTimer() && existing.SourceKey == r.SourceKey &&
			(existing.State == "queued" || existing.State == "running" || q.running[existing.ID] != nil) {
			return fail("timer_busy", "timer already has a queued or active task")
		}
	}
	t.Mode = FreshTask
	t.SourceKey = r.SourceKey
	if r.Deadline != nil {
		deadline := r.Deadline.UTC()
		t.Deadline = &deadline
	}
	return nil
}

func (t *Task) timerExpired(now time.Time) bool {
	return t.isTimer() && t.Deadline != nil && !now.Before(*t.Deadline)
}

// Called with q.mu held. A skipped task never reaches the Horizon runner.
func (q *Queue) skipExpiredTimer(t *Task, now time.Time) bool {
	if !t.timerExpired(now) {
		return false
	}
	if err := q.transaction(func() error {
		q.markTimerSkipped(t)
		return nil
	}); err != nil {
		q.fatal = err
	}
	return true
}

func (q *Queue) markTimerSkipped(t *Task) {
	t.State = "skipped"
	t.Error = errTimerWindowElapsed.Error()
	q.event(t, "")
}
