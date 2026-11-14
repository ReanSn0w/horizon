package timer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ReanSn0w/horizon/internal/daemon"
)

type timerDaemon struct {
	tasks     map[string]daemon.Task
	requests  []daemon.Request
	submitErr string
	loseReply bool
	startRace bool
}

func (d *timerDaemon) call(_ context.Context, _ string, r daemon.Request) (daemon.Response, error) {
	d.requests = append(d.requests, r)
	failure := func(code string) (daemon.Response, error) {
		return daemon.Response{}, &daemon.Failure{Code: code}
	}
	response := func(value any) (daemon.Response, error) {
		data, err := json.Marshal(value)
		return daemon.Response{Data: data}, err
	}
	switch r.Method {
	case "submit":
		if d.submitErr != "" {
			return failure(d.submitErr)
		}
		task := daemon.Task{ID: fmt.Sprintf("task-%d", len(d.tasks)+1), State: "queued", Mode: r.Mode, SourceKey: r.SourceKey}
		d.tasks[r.OperationID] = task
		if d.loseReply {
			return failure("outcome_unknown")
		}
		return response(task)
	case "lookup":
		task, ok := d.tasks[r.OperationID]
		if ok {
			return response(task)
		}
	case "read", "cancel":
		for op, task := range d.tasks {
			if task.ID != r.TaskID {
				continue
			}
			if r.Method == "read" {
				return response(map[string]any{"task": task})
			}
			if task.State == "queued" && r.QueuedOnly {
				task.State = "cancelled"
				if d.startRace {
					task.State = "running"
				}
				d.tasks[op] = task
				return response(task)
			}
			return failure("invalid_request")
		}
	}
	return failure("not_found")
}

func (d *timerDaemon) submissions() []daemon.Request {
	var submits []daemon.Request
	for _, r := range d.requests {
		if r.Method == "submit" {
			submits = append(submits, r)
		}
	}
	return submits
}

func (d *timerDaemon) stateOfLastSubmit(state string) {
	requests := d.submissions()
	key := requests[len(requests)-1].OperationID
	task := d.tasks[key]
	task.State = state
	d.tasks[key] = task
}

func testRuntime(t *testing.T) (*runtime, *timerDaemon, *time.Time, string) {
	t.Helper()
	home, workspace := t.TempDir(), t.TempDir()
	now := instant(t, "2026-11-11T12:00:00Z")
	stub := &timerDaemon{tasks: map[string]daemon.Task{}}
	putFile(t, home, "reports", markdown(workspace, minutely, "Original task."))
	r := &runtime{
		home: home, now: func() time.Time { return now }, call: stub.call, store: newStateStore(home),
		settings: Settings{Access: "write", ReloadInterval: time.Second}, log: log.New(io.Discard, "", 0),
	}
	r.reload(now)
	r.lastTick = now
	return r, stub, &now, workspace
}

func tickAt(r *runtime, clock *time.Time, when time.Time) {
	*clock = when
	r.tick(context.Background())
}

func reachSlot(r *runtime, clock *time.Time, when time.Time) {
	tickAt(r, clock, when.Add(-tickInterval))
	tickAt(r, clock, when)
}

func timerRecord(t *testing.T, r *runtime, id string) Record {
	t.Helper()
	records, err := r.store.read()
	if err != nil {
		t.Fatal(err)
	}
	return records[id]
}

func TestSchedulerClockJumpsRestartAndBusySlots(t *testing.T) {
	r, stub, now, _ := testRuntime(t)
	base := *now
	tickAt(r, now, base)
	tickAt(r, now, base.Add(time.Minute)) // Wake exactly on a scheduled minute.
	if len(stub.submissions()) != 0 {
		t.Fatal("startup or wake caught up an old slot")
	}
	reachSlot(r, now, base.Add(2*time.Minute))
	if len(stub.submissions()) != 1 {
		t.Fatal("missed future slot")
	}
	reachSlot(r, now, base.Add(3*time.Minute))
	if len(stub.submissions()) != 1 {
		t.Fatal("accepted another task while timer was busy")
	}
	stub.stateOfLastSubmit("succeeded")
	tickAt(r, now, base.Add(2*time.Minute-time.Second))
	tickAt(r, now, base.Add(2*time.Minute))
	if len(stub.submissions()) != 1 {
		t.Fatal("ran an already considered slot after clock reversal")
	}

	// A restarted scheduler starts strictly after the observed time, retaining
	// the original operation record and reconciled task.
	tickAt(r, now, base.Add(7*time.Minute))
	r.timers = nil
	r.catalogReady = false
	r.reload(*now)
	r.lastTick = *now
	tickAt(r, now, *now)
	reachSlot(r, now, base.Add(8*time.Minute))
	submits := stub.submissions()
	if len(submits) != 2 || submits[0].OperationID == submits[1].OperationID ||
		!timerRecord(t, r, "reports").Through.Equal(base.Add(8*time.Minute)) {
		t.Fatalf("submits = %+v", submits)
	}
}

func TestSchedulerReloadCancelsOnlyQueuedRevision(t *testing.T) {
	for _, dispatched := range []bool{false, true} {
		t.Run(fmt.Sprintf("dispatch_wins=%t", dispatched), func(t *testing.T) {
			r, stub, now, workspace := testRuntime(t)
			base := *now
			reachSlot(r, now, base.Add(time.Minute))
			stub.startRace = dispatched
			putFile(t, r.home, "reports", markdown(workspace, minutely, "Updated task."))
			putFile(t, r.home, "other", markdown(workspace, minutely, "Independent task."))
			putFile(t, r.home, "broken", "bad frontmatter")
			tickAt(r, now, base.Add(time.Minute+time.Second))
			record := timerRecord(t, r, "reports")
			expected := "cancelled"
			if dispatched {
				expected = "running"
			}
			if record.LastEvent != expected || len(stub.submissions()) != 1 || r.timers["broken"] != nil ||
				r.timers["other"] == nil || !r.timers["reports"].next.At.Equal(base.Add(2*time.Minute)) {
				t.Fatalf("reload record=%+v catalog=%+v submits=%+v", record, r.timers, stub.submissions())
			}
			cancel := stub.requests[len(stub.requests)-1]
			if cancel.Method != "cancel" || !cancel.QueuedOnly || stub.submissions()[0].Message != "Original task." {
				t.Fatalf("incorrect cancellation/snapshot: %+v", cancel)
			}
			stub.stateOfLastSubmit("succeeded")
			reachSlot(r, now, base.Add(2*time.Minute))
			submits := stub.submissions()
			if len(submits) != 3 || submits[2].Message != "Updated task." || submits[1].SourceKey != "other" {
				t.Fatalf("submits=%+v", submits)
			}
		})
	}
}

func TestSchedulerReloadDeletionDisableAndUnchanged(t *testing.T) {
	for _, action := range []string{"delete", "disable", "invalidate", "unchanged"} {
		t.Run(action, func(t *testing.T) {
			r, stub, now, _ := testRuntime(t)
			reachSlot(r, now, now.Add(time.Minute))
			next := r.timers["reports"].next.At
			switch action {
			case "delete":
				if err := os.Remove(filepath.Join(r.home, "timers", "reports.md")); err != nil {
					t.Fatal(err)
				}
			case "disable":
				if changed, err := SetEnabled(r.home, "reports", false); err != nil || !changed {
					t.Fatalf("disable: %t %v", changed, err)
				}
			case "invalidate":
				putFile(t, r.home, "reports", "broken")
			}
			tickAt(r, now, now.Add(time.Second))
			if action == "unchanged" {
				if !r.timers["reports"].next.At.Equal(next) || len(stub.requests) != 2 {
					t.Fatal("unchanged timer lost its state")
				}
				return
			}
			if r.timers["reports"] != nil || timerRecord(t, r, "reports").LastEvent != "cancelled" {
				t.Fatal("obsolete timer still scheduled or queued")
			}
		})
	}
}

func TestSchedulerRecoversLostReplyAndBlocksUnknown(t *testing.T) {
	r, stub, now, workspace := testRuntime(t)
	base := *now
	stub.loseReply = true
	reachSlot(r, now, base.Add(time.Minute))
	if len(stub.requests) != 2 || stub.requests[0].Method != "submit" || stub.requests[1].Method != "lookup" ||
		stub.requests[0].OperationID != stub.requests[1].OperationID {
		t.Fatalf("request sequence: %+v", stub.requests)
	}
	if record := timerRecord(t, r, "reports"); record.Pending == nil || record.Pending.TaskID == "" || record.Block != "" {
		t.Fatalf("did not reconnect: %+v", record)
	}
	stub.stateOfLastSubmit("unknown")
	tickAt(r, now, now.Add(tickInterval))
	if record := timerRecord(t, r, "reports"); record.Block == "" || record.Pending.TaskState != "unknown" {
		t.Fatalf("did not block unknown outcome: %+v", record)
	}
	putFile(t, r.home, "reports", markdown(workspace, minutely, "Updated after crash."))
	reachSlot(r, now, base.Add(2*time.Minute))
	if len(stub.submissions()) != 1 {
		t.Fatal("reload bypassed an unknown outcome")
	}
	if err := Unblock(r.home, "reports"); err != nil {
		t.Fatal(err)
	}
	reachSlot(r, now, base.Add(3*time.Minute))
	submits := stub.submissions()
	if len(submits) != 2 || submits[1].OperationID == submits[0].OperationID || submits[1].Message != "Updated after crash." {
		t.Fatalf("unblock did not limit execution to future slots: %+v", submits)
	}
}

func TestSchedulerStorageFailureQueueFullAndAmbiguousIntent(t *testing.T) {
	t.Run("clock advances while saving intent", func(t *testing.T) {
		r, stub, now, _ := testRuntime(t)
		r.store.save = func(path string, data []byte) error {
			*now = now.Add(slotTolerance + time.Second)
			return saveState(path, data)
		}
		reachSlot(r, now, now.Add(time.Minute))
		if len(stub.submissions()) != 0 || timerRecord(t, r, "reports").LastEvent != "skipped: slot_elapsed" {
			t.Fatal("submitted an old slot after persistence blocked")
		}
	})
	t.Run("cannot save intent", func(t *testing.T) {
		r, stub, now, _ := testRuntime(t)
		r.store.save = func(string, []byte) error { return errors.New("disk full") }
		reachSlot(r, now, now.Add(time.Minute))
		if len(stub.submissions()) != 0 {
			t.Fatal("submitted without persisting intent")
		}
	})
	t.Run("cannot save acknowledgement", func(t *testing.T) {
		r, stub, now, _ := testRuntime(t)
		writes := 0
		r.store.save = func(path string, data []byte) error {
			writes++
			if writes == 2 {
				return errors.New("disk full")
			}
			return saveState(path, data)
		}
		reachSlot(r, now, now.Add(time.Minute))
		if p := timerRecord(t, r, "reports").Pending; p == nil || p.TaskID != "" {
			t.Fatalf("intent = %+v", p)
		}
		r.store = newStateStore(r.home) // Same durable state after restart.
		r.reload(*now)
		tickAt(r, now, now.Add(tickInterval))
		if len(stub.submissions()) != 1 || timerRecord(t, r, "reports").Pending.TaskID == "" {
			t.Fatal("lost local acknowledgment was not recovered via lookup")
		}
	})
	for _, failure := range []string{"queue_full", "outcome_unknown"} {
		t.Run(failure, func(t *testing.T) {
			r, stub, now, _ := testRuntime(t)
			stub.submitErr = failure
			reachSlot(r, now, now.Add(time.Minute))
			tickAt(r, now, now.Add(tickInterval))
			record := timerRecord(t, r, "reports")
			if len(stub.submissions()) != 1 {
				t.Fatal("blindly retried submission")
			}
			if failure == "queue_full" {
				if record.LastEvent != "skipped: queue_full" || record.Pending != nil || record.Block != "" {
					t.Fatalf("queue_full = %+v", record)
				}
			} else if record.Block == "" || record.Pending == nil {
				t.Fatalf("uncertain submission = %+v", record)
			}
		})
	}
}

func TestTimerStateVersionAndPrivateModes(t *testing.T) {
	r, _, now, _ := testRuntime(t)
	reachSlot(r, now, now.Add(time.Minute))
	for name, mode := range map[string]os.FileMode{"timer": 0700, "timer/state.json": 0600, "timer/state.lock": 0600} {
		info, err := os.Stat(filepath.Join(r.home, name))
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("%s: %v, %v", name, info, err)
		}
	}
	got, err := Status(r.home)
	if err != nil || !reflect.DeepEqual(got["reports"], timerRecord(t, r, "reports")) {
		t.Fatalf("status = %+v %v", got, err)
	}
}

func TestSchedulerDoesNotRevisitSlotsSkippedDuringSleep(t *testing.T) {
	r, stub, now, _ := testRuntime(t)
	base := *now
	tickAt(r, now, base.Add(3*time.Minute))
	if record := timerRecord(t, r, "reports"); !record.Through.Equal(base.Add(3 * time.Minute)) {
		t.Fatalf("sleep did not advance the durable high-water mark: %+v", record)
	}
	reachSlot(r, now, base.Add(2*time.Minute))
	if len(stub.submissions()) != 0 {
		t.Fatal("clock reversal caught up a slot already skipped during sleep")
	}
	reachSlot(r, now, base.Add(4*time.Minute))
	if len(stub.submissions()) != 1 {
		t.Fatal("timer did not resume at the next future slot")
	}
}

func TestSchedulerCreatesDefaultWorkspaceOnlyForLiveSlot(t *testing.T) {
	r, stub, now, _ := testRuntime(t)
	body := "---\nname: Nap\ndescription: Job\nschedule:\n" + minutely + "\n---\nTask.\n"
	putFile(t, r.home, "nap", body)
	r.reload(*now)
	def := r.timers["nap"].definition
	if _, err := os.Stat(def.Workspace); !os.IsNotExist(err) {
		t.Fatal("reload created workspace")
	}
	stale := Slot{At: now.Add(-time.Minute)}
	r.submit(context.Background(), def, stale)
	if _, err := os.Stat(def.Workspace); !os.IsNotExist(err) {
		t.Fatal("stale slot created workspace")
	}
	live := Slot{At: *now}
	r.submit(context.Background(), def, live)
	info, err := os.Stat(def.Workspace)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal(info, err)
	}
	found := false
	for _, req := range stub.submissions() {
		if req.SourceKey == "nap" {
			found = true
			if req.Caller.Workspace != def.Workspace {
				t.Fatal(req)
			}
		}
	}
	if !found {
		t.Fatal("no nap submission")
	}
}

func TestWorkspacePreparationFailureDoesNotSubmit(t *testing.T) {
	r, stub, now, _ := testRuntime(t)
	putFile(t, r.home, "nap", "---\nname: Nap\ndescription: Job\nschedule:\n"+minutely+"\n---\nTask.\n")
	def, err := LoadFile(r.home, "nap")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.home, "workspace"), []byte("blocking file"), 0600); err != nil {
		t.Fatal(err)
	}
	r.submit(context.Background(), def, Slot{At: *now})
	if len(stub.submissions()) != 0 {
		t.Fatal("submitted despite invalid workspace")
	}
	record := timerRecord(t, r, "nap")
	if record.Pending != nil {
		t.Fatal("known failed preparation left uncertain intent")
	}
}
