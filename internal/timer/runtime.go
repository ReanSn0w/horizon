package timer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ReanSn0w/horizon/internal/daemon"
	"github.com/ReanSn0w/horizon/internal/maintenance"
)

const (
	tickInterval  = 500 * time.Millisecond
	slotTolerance = 2 * time.Second
)

type plannedTimer struct {
	definition Definition
	schedule   Schedule
	next       Slot
}

type runtime struct {
	home          string
	caller        daemon.Caller
	settings      Settings
	now           func() time.Time
	call          func(context.Context, string, daemon.Request) (daemon.Response, error)
	store         stateStore
	log           *log.Logger
	timers        map[string]*plannedTimer
	lastTick      time.Time
	lastReload    time.Time
	catalogReady  bool
	catalogErrors map[string]string
}

func Run(ctx context.Context, home, credential, inherited string, settings Settings, out io.Writer) error {
	if credential == "" || accessLevel(inherited) == 0 {
		return errors.New("agent-timer must run under the Horizon daemon supervisor")
	}
	if accessLevel(settings.Access) == 0 || settings.ReloadInterval < time.Second {
		return errors.New("invalid agent-timer settings")
	}
	if inherited == "read" {
		return errors.New("read inherited access cannot submit scheduled tasks")
	}
	if accessLevel(settings.Access) > accessLevel(inherited) {
		return fmt.Errorf("plugins.agent-timer.access=%s exceeds the daemon's inherited %s access", settings.Access, inherited)
	}
	r := &runtime{
		home: home, settings: settings, now: time.Now, call: daemon.Call, store: newStateStore(home),
		log:    log.New(out, "agent-timer: ", 0),
		caller: daemon.Caller{Kind: "adapter", Source: daemon.TimerSource, Workspace: home, Access: inherited, Credential: credential},
	}
	if _, err := r.request(ctx, daemon.Request{Method: "list"}); err != nil {
		return err
	}
	if err := r.store.ensureDirectory(); err != nil {
		return err
	}
	release, err := maintenance.Lock(filepath.Join(r.store.directory(), "scheduler.lock"), true)
	if err != nil {
		return fmt.Errorf("timer scheduler lock: %w", err)
	}
	defer release()
	if _, err := r.store.read(); err != nil {
		return err
	}
	now := r.now().Round(0)
	r.reload(now)
	r.lastTick = now
	tick := time.NewTicker(tickInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			r.tick(ctx)
		}
	}
}

func (r *runtime) reload(now time.Time) {
	r.lastReload = now
	problems := map[string]string{}
	defer func() {
		for _, id := range sortedIDs(problems) {
			if r.catalogErrors[id] != problems[id] {
				r.log.Printf("%s: %s", id, problems[id])
			}
		}
		r.catalogErrors = problems
	}()
	files, err := LoadCatalog(r.home)
	if err != nil {
		r.catalogReady = false
		problems["timer catalog"] = err.Error()
		return
	}
	next := map[string]*plannedTimer{}
	for _, file := range files {
		def := file.Definition
		if file.Err != nil {
			problems[file.ID] = file.Err.Error()
			continue
		}
		if !def.Enabled {
			continue
		}
		current := r.timers[file.ID]
		if r.catalogReady && current != nil && current.definition.Revision == def.Revision &&
			current.definition.Workspace == def.Workspace && !current.next.At.IsZero() {
			next[file.ID] = current
			continue
		}
		schedule, err := Compile(def.Schedule)
		if err == nil {
			var slot Slot
			slot, err = schedule.Next(now)
			if err == nil {
				next[file.ID] = &plannedTimer{definition: def, schedule: schedule, next: slot}
			}
		}
		if err != nil {
			problems[file.ID] = err.Error()
		}
	}
	r.catalogReady = true
	r.timers = next
}

func (r *runtime) advance(planned *plannedTimer, after time.Time) {
	slot, err := planned.schedule.Next(after)
	if err != nil {
		planned.next = Slot{}
		r.log.Printf("%s: %v", planned.definition.ID, err)
		return
	}
	planned.next = slot
}

func (r *runtime) tick(ctx context.Context) {
	now := r.now().Round(0)
	forwardJump := now.Sub(r.lastTick) > slotTolerance
	jumped := now.Before(r.lastTick) || forwardJump
	r.lastTick = now
	if r.lastReload.IsZero() || now.Before(r.lastReload) || now.Sub(r.lastReload) >= r.settings.ReloadInterval {
		r.reload(now)
	}
	records, err := r.store.read()
	if err != nil {
		r.log.Print(err)
		return
	}
	for _, id := range sortedIDs(records) {
		if ctx.Err() != nil {
			return
		}
		if p := records[id].Pending; p != nil && p.TaskState != "unknown" {
			r.reconcile(ctx, id, *p)
		}
	}
	if !r.catalogReady {
		return
	}
	for _, id := range sortedIDs(r.timers) {
		if ctx.Err() != nil {
			return
		}
		planned := r.timers[id]
		if jumped {
			if forwardJump && !planned.next.At.IsZero() && !now.Before(planned.next.At) {
				r.skip(id, now, "clock advanced past a scheduled slot")
			}
			after := now
			if through := records[id].Through; through.After(after) {
				after = through
			}
			r.advance(planned, after)
			continue
		}
		slot := planned.next
		if slot.At.IsZero() || now.Before(slot.At) {
			continue
		}
		r.advance(planned, now)
		observed := r.now().Round(0)
		if observed.Before(slot.At) || observed.Sub(slot.At) > slotTolerance {
			continue
		}
		if slot.Deadline != nil && !observed.Before(*slot.Deadline) {
			r.skip(id, slot.At, "window ended before submission")
			continue
		}
		r.submit(ctx, planned.definition, slot)
	}
}

func sortedIDs[V any](m map[string]V) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func (r *runtime) request(ctx context.Context, request daemon.Request) (json.RawMessage, error) {
	return r.requestAt(ctx, r.home, request)
}

func (r *runtime) requestAt(ctx context.Context, workspace string, request daemon.Request) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	request.ID = "timer-" + request.Method
	request.Caller = r.caller
	request.Caller.Workspace = workspace
	response, err := r.call(ctx, r.home, request)
	return response.Data, err
}

func (r *runtime) skip(id string, at time.Time, reason string) {
	changed := false
	if r.change(id, func(record *Record) bool {
		if !at.After(record.Through) {
			return false
		}
		record.Through = at
		record.LastEvent = "skipped: " + reason
		changed = true
		return true
	}) && changed {
		r.log.Printf("%s: skipped through %s (%s)", id, at.Format(time.RFC3339Nano), reason)
	}
}

func (r *runtime) change(id string, update func(*Record) bool) bool {
	if err := r.store.update(id, update); err != nil {
		r.log.Printf("%s: could not persist timer state: %v", id, err)
		return false
	}
	return true
}

func (r *runtime) submit(ctx context.Context, def Definition, slot Slot) {
	intent := Submission{At: slot.At, Revision: def.Revision, Workspace: def.Workspace}
	begin := false
	if !r.change(def.ID, func(record *Record) bool {
		if !slot.At.After(record.Through) {
			return false
		}
		record.Through = slot.At
		if record.Block != "" || record.Pending != nil {
			record.LastEvent = "skipped while blocked or busy"
			return true
		}
		record.Pending = &intent
		record.LastEvent = "submission recorded"
		begin = true
		return true
	}) || !begin {
		return
	}
	// Persistence may have taken longer than the slot tolerance, including
	// a machine suspend while the state file was being flushed.
	now := r.now().Round(0)
	if now.Before(slot.At) || now.Sub(slot.At) > slotTolerance {
		r.finishRejected(def.ID, intent, "slot_elapsed")
		return
	}
	if slot.Deadline != nil && !now.Before(*slot.Deadline) {
		r.finishRejected(def.ID, intent, "window_ended")
		return
	}
	if err := prepareWorkspace(def); err != nil {
		r.log.Printf("%s: workspace unavailable: %v", def.ID, err)
		r.finishRejected(def.ID, intent, "workspace_unavailable")
		return
	}
	now = r.now().Round(0)
	if now.Before(slot.At) || now.Sub(slot.At) > slotTolerance || slot.Deadline != nil && !now.Before(*slot.Deadline) {
		r.finishRejected(def.ID, intent, "window_elapsed_during_workspace_preparation")
		return
	}
	data, err := r.requestAt(ctx, def.Workspace, daemon.Request{
		Method: "submit", OperationID: intent.OperationID(def.ID), Mode: daemon.FreshTask,
		SourceKey: def.ID, Deadline: slot.Deadline, Access: r.settings.Access, Message: def.Message,
	})
	if err == nil {
		var task daemon.Task
		if json.Unmarshal(data, &task) == nil && validTask(def.ID, task) {
			r.observe(def.ID, intent, task)
			return
		}
	}
	switch code := failureCode(err); code {
	case "queue_full", "timer_busy", "daemon_unavailable", "access_denied", "storage_error", "invalid_request":
		r.finishRejected(def.ID, intent, code)
	default:
		r.reconcile(ctx, def.ID, intent)
	}
}

func failureCode(err error) string {
	var failure *daemon.Failure
	if errors.As(err, &failure) {
		return failure.Code
	}
	return ""
}

func validTask(id string, task daemon.Task) bool {
	return task.ID != "" && task.SourceKey == id && task.Mode == daemon.FreshTask
}

func (r *runtime) reconcile(ctx context.Context, id string, p Submission) {
	var task daemon.Task
	var err error
	if p.TaskID == "" {
		var data json.RawMessage
		data, err = r.request(ctx, daemon.Request{Method: "lookup", OperationID: p.OperationID(id)})
		if err == nil {
			err = json.Unmarshal(data, &task)
		}
	} else {
		var data json.RawMessage
		data, err = r.request(ctx, daemon.Request{Method: "read", TaskID: p.TaskID})
		if err == nil {
			var response struct{ Task daemon.Task }
			err = json.Unmarshal(data, &response)
			task = response.Task
		}
	}
	if err != nil {
		if failureCode(err) == "not_found" {
			r.block(id, p, "could not confirm submission in daemon")
		}
		return
	}
	if !validTask(id, task) || p.TaskID != "" && p.TaskID != task.ID {
		r.block(id, p, "daemon returned an unexpected task")
		return
	}
	if task.State == "queued" && r.stale(id, p) {
		task = r.cancelQueued(ctx, id, p, task)
	}
	r.observe(id, p, task)
}

func (r *runtime) stale(id string, p Submission) bool {
	if !r.catalogReady {
		return false
	}
	current := r.timers[id]
	return current == nil || current.definition.Revision != p.Revision || current.definition.Workspace != p.Workspace
}

func (r *runtime) cancelQueued(ctx context.Context, id string, p Submission, current daemon.Task) daemon.Task {
	data, err := r.request(ctx, daemon.Request{
		Method: "cancel", TaskID: current.ID, OperationID: p.OperationID(id) + ":reload-cancel", QueuedOnly: true,
	})
	if err == nil {
		var cancelled daemon.Task
		if json.Unmarshal(data, &cancelled) == nil && validTask(id, cancelled) && cancelled.ID == current.ID {
			return cancelled
		}
	}
	return current
}

func sameSubmission(record *Record, p Submission) bool {
	return record.Pending != nil && record.Pending.At.Equal(p.At)
}

func (r *runtime) observe(id string, p Submission, task daemon.Task) {
	switch task.State {
	case "queued", "running", "succeeded", "failed", "cancelled", "skipped", "unknown":
	default:
		r.block(id, p, "daemon returned an unsupported task state")
		return
	}
	changed := false
	if !r.change(id, func(record *Record) bool {
		if !sameSubmission(record, p) || record.Pending.TaskID == task.ID && record.Pending.TaskState == task.State {
			return false
		}
		record.LastTaskID = task.ID
		record.LastEvent = task.State
		record.Pending.TaskID = task.ID
		record.Pending.TaskState = task.State
		switch task.State {
		case "queued", "running":
			record.Block = ""
		case "unknown":
			record.Block = "daemon restarted during execution; inspect task before unblocking"
		default:
			record.Block = ""
			record.Pending = nil
		}
		changed = true
		return true
	}) || !changed {
		return
	}
	message := strings.TrimSpace(task.Error)
	r.log.Printf("%s: %s task %s %s", id, p.At.Format(time.RFC3339Nano), task.ID, strings.TrimSpace(task.State+" "+message))
}

func (r *runtime) block(id string, p Submission, reason string) {
	changed := false
	if r.change(id, func(record *Record) bool {
		if !sameSubmission(record, p) || record.Block == reason {
			return false
		}
		record.Block = reason
		changed = true
		return true
	}) && changed {
		r.log.Printf("%s: blocked (%s)", id, reason)
	}
}

func (r *runtime) finishRejected(id string, p Submission, code string) {
	changed := false
	if r.change(id, func(record *Record) bool {
		if !sameSubmission(record, p) {
			return false
		}
		record.Pending = nil
		record.LastEvent = "skipped: " + code
		changed = true
		return true
	}) && changed {
		r.log.Printf("%s: skipped %s (%s)", id, p.At.Format(time.RFC3339Nano), code)
	}
}
