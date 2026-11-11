package daemon

import (
	"sort"
	"strings"

	"github.com/ReanSn0w/horizon/internal/turninput"
)

type storedInput struct {
	Receipt InputReceipt `json:"receipt"`
	TaskID  string       `json:"task_id"`
	Author  string       `json:"author,omitempty"`
	Text    string       `json:"text,omitempty"`
}

func (q *Queue) queueFull() bool {
	pending := 0
	for _, t := range q.state.Tasks {
		if t.State == "queued" || t.State == "running" {
			pending++
		}
	}
	return pending >= q.settings.Queue
}

func (q *Queue) operationResult(op Operation) *Task {
	if op.InputID == "" {
		return summary(q.state.Tasks[op.TaskID])
	}
	in := q.state.Inputs[op.InputID]
	result := summary(q.state.Tasks[in.TaskID])
	receipt := in.Receipt
	result.Input = &receipt
	return result
}

func (q *Queue) acceptActiveInput(r Request, key, digest string) (*Task, error) {
	if r.Route == "" || r.Caller.Session != "" || r.TaskID != "" || strings.TrimSpace(r.Author) == "" || len(r.Author) > 512 {
		return nil, fail("invalid_request", "active input requires route and author; the daemon assigns the session")
	}
	if strings.TrimSpace(r.Message) == "" || len(r.Message) > MaxInputBytes {
		return nil, fail("invalid_request", "message must be 1..131072 bytes")
	}
	access := r.Access
	if access == "" {
		access = r.Caller.Access
	}
	if level(access) == 0 || level(access) > level(r.Caller.Access) {
		return nil, fail("access_denied", "dialog cannot exceed caller access")
	}
	source := owner(r.Caller)
	var root, queued, running *Task
	var finished []*Task
	pending := 0
	for _, task := range q.state.Tasks {
		if !task.ActiveInput || task.Owner != source || task.Route != r.Route {
			continue
		}
		if task.Workspace != r.Caller.Workspace || task.Channel != r.Channel {
			return nil, fail("route_conflict", "dialog route has a different workspace or channel binding")
		}
		if task.Child == task.ID {
			root = task
		}
		remaining := q.countPendingInputs(task)
		pending += remaining
		if remaining > 0 && task.Access != access {
			return nil, fail("input_access_conflict", "pending dialog input has different access")
		}
		switch task.State {
		case "queued":
			if queued == nil || queued.Sequence > task.Sequence {
				queued = task
			}
		case "running":
			if !task.InputClosed && task.Access == access {
				running = task
			}
		case "failed", "cancelled":
			if q.running[task.ID] == nil {
				finished = append(finished, task)
			}
		case "unknown":
			if task.InputResolved {
				finished = append(finished, task)
			}
		}
	}
	if pending >= MaxPendingInputs {
		return nil, fail("input_full", "dialog pending input limit reached")
	}
	target := queued
	if target != nil && target.Access != access {
		return nil, fail("input_access_conflict", "queued dialog has different access")
	}
	if target == nil {
		target = running
	}
	if target == nil && q.queueFull() {
		return nil, fail("queue_full", "pending queue limit reached")
	}
	var operation Operation
	err := q.transaction(func() error {
		if target == nil {
			target = q.newActiveTask(source, r.Caller.Workspace, access, r.Route, r.Channel, root)
		}
		for _, task := range finished {
			q.moveStoredInputs(task, target)
		}
		q.state.InputSequence++
		receipt := InputReceipt{ID: newID(), Sequence: q.state.InputSequence, Source: r.Caller.Source, Route: r.Route, Status: "stored"}
		q.state.Inputs[receipt.ID] = &storedInput{Receipt: receipt, TaskID: target.ID, Author: r.Author, Text: r.Message}
		target.InputIDs = append(target.InputIDs, receipt.ID)
		q.event(target, "input_stored")
		operation = Operation{Hash: digest, TaskID: target.ID, InputID: receipt.ID}
		q.state.Operations[key] = operation
		return nil
	})
	if err != nil {
		return nil, err
	}
	return q.operationResult(operation), nil
}

func (q *Queue) newActiveTask(owner, workspace, access, route string, channel bool, root *Task) *Task {
	id := newID()
	q.state.Sequence++
	task := &Task{ID: id, Child: id, Owner: owner, Workspace: workspace, Access: access, Route: route, Depth: 1,
		State: "queued", Sequence: q.state.Sequence, ActiveInput: true, Channel: channel}
	if root != nil {
		task.Child = root.Child
		task.Session = root.Session
	}
	q.state.Tasks[id] = task
	q.event(task, "")
	return task
}

func (q *Queue) countPendingInputs(task *Task) int {
	count := 0
	for _, id := range task.InputIDs {
		if q.state.Inputs[id].Receipt.Status == "stored" {
			count++
		}
	}
	return count
}

func (q *Queue) moveStoredInputs(from, to *Task) {
	if from.Access != to.Access {
		return
	}
	remaining := from.InputIDs[:0]
	for _, id := range from.InputIDs {
		in := q.state.Inputs[id]
		if in.Receipt.Status != "stored" {
			remaining = append(remaining, id)
			continue
		}
		in.TaskID = to.ID
		to.InputIDs = append(to.InputIDs, id)
	}
	from.InputIDs = remaining
	sort.Slice(to.InputIDs, func(i, j int) bool {
		return q.state.Inputs[to.InputIDs[i]].Receipt.Sequence < q.state.Inputs[to.InputIDs[j]].Receipt.Sequence
	})
}

func (q *Queue) moveInputToQueuedSuccessor(task *Task) {
	if !task.ActiveInput || task.State == "unknown" || q.countPendingInputs(task) == 0 {
		return
	}
	if successor := q.queuedInputSuccessor(task); successor != nil {
		q.moveStoredInputs(task, successor)
	}
}

func (q *Queue) queuedInputSuccessor(task *Task) *Task {
	var successor *Task
	for _, candidate := range q.state.Tasks {
		if candidate.Child == task.Child && candidate.ActiveInput && candidate.State == "queued" && candidate.Access == task.Access &&
			(successor == nil || candidate.Sequence < successor.Sequence) {
			successor = candidate
		}
	}
	return successor
}

func (q *Queue) resolveActiveInput(task *Task, key, digest string) (*Task, error) {
	if !task.ActiveInput || task.Executing || (task.State != "unknown" && task.State != "failed" && task.State != "cancelled") {
		return nil, fail("invalid_state", "expected a stopped active-input task")
	}
	err := q.transaction(func() error {
		if !task.InputResolved && q.countPendingInputs(task) != 0 {
			successor := q.queuedInputSuccessor(task)
			if successor == nil {
				if q.queueFull() {
					return fail("queue_full", "pending queue limit reached")
				}
				successor = q.newActiveTask(task.Owner, task.Workspace, task.Access, task.Route, task.Channel, q.state.Tasks[task.Child])
			}
			q.moveStoredInputs(task, successor)
		}
		task.InputResolved = true
		q.event(task, "input_resolved")
		q.state.Operations[key] = Operation{Hash: digest, TaskID: task.ID}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return summary(task), nil
}

func (q *Queue) handleTurnInput(r Request) (any, error) {
	t := q.state.Tasks[r.TaskID]
	if t == nil || !t.ActiveInput || r.Caller.Kind != "turn_input" || r.Caller.Credential == "" ||
		q.inputCredentials[t.ID] != r.Caller.Credential || t.Session == "" || t.Session != r.Caller.Session ||
		t.Workspace != r.Caller.Workspace || t.Route != r.Route {
		return nil, fail("access_denied", "input channel binding does not match")
	}
	if q.fatal != nil {
		return nil, fail("storage_error", "scheduler stopped after a persistence failure")
	}
	if r.TurnID == "" || len(r.TurnID) > 256 {
		return nil, fail("invalid_request", "input channel requires a turn ID")
	}
	if r.Method == "input_ack" {
		in := q.state.Inputs[r.InputID]
		if in == nil || in.TaskID != t.ID {
			return nil, fail("not_found", "input is not assigned to this task")
		}
		if in.Receipt.Status == "included" {
			if in.Receipt.Turn != r.TurnID {
				return nil, fail("operation_conflict", "input is already included in a different turn")
			}
			return nil, nil
		}
		return nil, q.transaction(func() error {
			in.Receipt.Status = "included"
			in.Receipt.Session = t.Session
			in.Receipt.Turn = r.TurnID
			in.Author = ""
			in.Text = ""
			q.event(t, "input_included")
			return nil
		})
	}
	if t.State != "running" {
		return nil, fail("invalid_state", "input task is not running")
	}
	for _, id := range t.InputIDs {
		in := q.state.Inputs[id]
		if in.Receipt.Status == "stored" {
			return &turninput.Message{ID: id, Sequence: in.Receipt.Sequence, Source: in.Receipt.Source,
				Route: in.Receipt.Route, Author: in.Author, Text: in.Text}, nil
		}
	}
	if r.Method != "input_close" || t.InputClosed {
		return nil, nil
	}
	return nil, q.transaction(func() error {
		t.InputClosed = true
		return nil
	})
}

func (s diskState) validInputs() bool {
	seen := make(map[string]bool, len(s.Inputs))
	routes := make(map[[2]string]string)
	sequences := make(map[uint64]bool, len(s.Inputs))
	for _, task := range s.Tasks {
		if task.ActiveInput {
			if !strings.HasPrefix(task.Owner, "adapter:") || task.Route == "" || task.Depth != 1 {
				return false
			}
			key := [2]string{task.Owner, task.Route}
			if child := routes[key]; child != "" && child != task.Child {
				return false
			}
			routes[key] = task.Child
			root := s.Tasks[task.Child]
			if !root.ActiveInput || root.Owner != task.Owner || root.Workspace != task.Workspace || root.Route != task.Route ||
				root.Channel != task.Channel || root.Session != task.Session {
				return false
			}
		}
		var prior uint64
		for _, id := range task.InputIDs {
			in := s.Inputs[id]
			if !task.ActiveInput || in == nil || seen[id] || in.TaskID != task.ID || in.Receipt.ID != id ||
				in.Receipt.Sequence <= prior || in.Receipt.Sequence > s.InputSequence || sequences[in.Receipt.Sequence] ||
				in.Receipt.Route != task.Route || "adapter:"+in.Receipt.Source != task.Owner {
				return false
			}
			switch in.Receipt.Status {
			case "stored":
				if in.Receipt.Turn != "" || in.Receipt.Session != "" || in.Author == "" || in.Text == "" || len(in.Text) > MaxInputBytes {
					return false
				}
			case "included":
				if in.Receipt.Turn == "" || in.Receipt.Session == "" || in.Receipt.Session != task.Session {
					return false
				}
			default:
				return false
			}
			seen[id] = true
			sequences[in.Receipt.Sequence] = true
			prior = in.Receipt.Sequence
		}
	}
	return len(seen) == len(s.Inputs)
}
