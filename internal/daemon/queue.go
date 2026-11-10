package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ReanSn0w/horizon/internal/session"
)

type Settings struct {
	Parallel int      `yaml:"parallel_per_depth"`
	Depth    int      `yaml:"max_depth"`
	Children int      `yaml:"max_children"`
	Queue    int      `yaml:"max_queue"`
	Adapters []string `yaml:"adapters"`
}

func Defaults() Settings { return Settings{Parallel: 2, Depth: 4, Children: 16, Queue: 128} }
func (s Settings) Validate() error {
	if s.Parallel < 1 || s.Parallel > 16 || s.Depth < 1 || s.Depth > 8 || s.Children < 1 || s.Children > 128 || s.Queue < 1 || s.Queue > 4096 {
		return errors.New("invalid daemon limits")
	}
	return nil
}

type Task struct {
	ID        string `json:"id"`
	Child     string `json:"child_id"`
	Owner     string `json:"owner"`
	Parent    string `json:"parent_child,omitempty"`
	Workspace string `json:"workspace"`
	Session   string `json:"session_id"`
	Turn      string `json:"turn_id,omitempty"`
	Access    string `json:"access"`
	Depth     int    `json:"depth"`
	Message   string `json:"message,omitempty"`
	State     string `json:"state"`
	Result    string `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
	ExitCode  int    `json:"exit_code"`
	Route     string `json:"route,omitempty"`
	Delivered bool   `json:"delivered"`
	Sequence  uint64 `json:"sequence"`
}
type Event struct {
	Cursor uint64 `json:"cursor"`
	TaskID string `json:"task_id"`
	Owner  string `json:"owner"`
	State  string `json:"state"`
	Stage  string `json:"stage,omitempty"`
}
type Operation struct {
	Hash   string `json:"hash"`
	TaskID string `json:"task_id"`
}
type diskState struct {
	Version    int                  `json:"version"`
	Tasks      map[string]*Task     `json:"tasks"`
	Operations map[string]Operation `json:"operations"`
	Events     []Event              `json:"events"`
	Cursor     uint64               `json:"cursor"`
	Sequence   uint64               `json:"sequence"`
}
type Outcome struct {
	Text     string
	Turn     string
	ExitCode int
	Err      error
}
type Runner func(context.Context, Task, func(string) error, func(string, string)) Outcome
type Queue struct {
	mu       sync.Mutex
	home     string
	settings Settings
	state    diskState
	running  map[string]context.CancelFunc
	runner   Runner
	fatal    error
	write    func(string, []byte) error
	adapters map[string]string
}

func OpenQueue(home string, settings Settings, runner Runner) (*Queue, error) {
	if err := settings.Validate(); err != nil {
		return nil, err
	}
	q := &Queue{home: home, settings: settings, runner: runner, running: map[string]context.CancelFunc{}, write: atomicState, adapters: map[string]string{}}
	q.state = diskState{Version: 1, Tasks: map[string]*Task{}, Operations: map[string]Operation{}, Events: []Event{}}
	b, err := os.ReadFile(filepath.Join(home, "daemon", "state.json"))
	if err == nil {
		if json.Unmarshal(b, &q.state) != nil || q.state.Version != 1 || q.state.Tasks == nil || q.state.Operations == nil {
			return nil, errors.New("invalid daemon state")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	changed := false
	for _, t := range q.state.Tasks {
		if t.State == "running" {
			t.State = "unknown"
			t.Error = "daemon restarted during execution"
			q.event(t, "")
			changed = true
		}
	}
	if changed {
		if err = q.save(); err != nil {
			return nil, err
		}
	}
	return q, nil
}
func atomicState(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".state-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func (q *Queue) save() error {
	b, e := json.Marshal(q.state)
	if e != nil {
		return e
	}
	return q.write(filepath.Join(q.home, "daemon", "state.json"), b)
}
func (q *Queue) transaction(fn func() error) error {
	b, _ := json.Marshal(q.state)
	if err := fn(); err != nil {
		q.state = diskState{}
		_ = json.Unmarshal(b, &q.state)
		return err
	}
	if err := q.save(); err != nil {
		q.state = diskState{}
		_ = json.Unmarshal(b, &q.state)
		return fail("storage_error", "cannot persist daemon state")
	}
	return nil
}
func (q *Queue) event(t *Task, stage string) {
	q.state.Cursor++
	q.state.Events = append(q.state.Events, Event{q.state.Cursor, t.ID, t.Owner, t.State, stage})
	if len(q.state.Events) > 1024 {
		q.state.Events = q.state.Events[len(q.state.Events)-1024:]
	}
}
func newID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func level(a string) int {
	switch a {
	case "read":
		return 1
	case "write":
		return 2
	case "full":
		return 3
	}
	return 0
}
func narrower(a, b string) string {
	if level(a) < level(b) {
		return a
	}
	return b
}
func owner(c Caller) string {
	if c.Kind == "adapter" {
		return "adapter:" + c.Source
	}
	return "session:" + c.Workspace + ":" + c.Session
}
func (q *Queue) caller(c Caller) (Caller, error) {
	if level(c.Access) == 0 {
		return c, fail("invalid_request", "invalid access")
	}
	w, e := session.NormalizeWorkspace(c.Workspace)
	if e != nil {
		return c, fail("invalid_request", "invalid workspace")
	}
	c.Workspace = w
	if c.Kind == "adapter" {
		if c.Credential == "" || q.adapters[c.Source] != c.Credential {
			return c, fail("access_denied", "unregistered adapter")
		}
	} else if c.Kind != "subagent" || c.Session == "" {
		return c, fail("invalid_request", "parent session required")
	}
	for _, t := range q.state.Tasks {
		if t.Workspace == c.Workspace && t.Session == c.Session && c.Session != "" {
			c.Access = narrower(c.Access, t.Access)
		}
	}
	return c, nil
}
func summary(t *Task) *Task { v := *t; v.Message = ""; v.Result = ""; return &v }
func (q *Queue) Handle(ctx context.Context, r Request) (any, error) {
	if r.Method == "wait" {
		if r.WaitMS < 0 || r.WaitMS > 30000 {
			return nil, fail("invalid_request", "wait_ms must be 0..30000")
		}
		until := time.NewTimer(time.Duration(r.WaitMS) * time.Millisecond)
		defer until.Stop()
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			q.mu.Lock()
			data, err := q.read(r)
			ready := q.state.Cursor > r.Cursor
			q.mu.Unlock()
			if err != nil || ready || r.WaitMS == 0 {
				return data, err
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-until.C:
				return data, nil
			case <-tick.C:
			}
		}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	switch r.Method {
	case "list", "read", "lookup":
		return q.read(r)
	}
	c, err := q.caller(r.Caller)
	if err != nil {
		return nil, err
	}
	r.Caller = c
	if level(c.Access) < 2 {
		return nil, fail("access_denied", "mutation requires write access")
	}
	if r.OperationID == "" || len(r.OperationID) > 256 {
		return nil, fail("invalid_request", "operation_id required (max 256 bytes)")
	}
	if q.fatal != nil {
		return nil, fail("storage_error", "scheduler stopped after a persistence failure")
	}
	if r.Method != "submit" && r.Method != "send" && r.Method != "cancel" && r.Method != "ack" {
		return nil, fail("unknown_method", "unsupported method")
	}
	key := owner(c) + ":" + r.OperationID
	hashRequest := r
	hashRequest.ID = ""
	hashRequest.Caller.Credential = ""
	b, _ := json.Marshal(hashRequest)
	hash := sha256.Sum256(b)
	digest := hex.EncodeToString(hash[:])
	if op, ok := q.state.Operations[key]; ok {
		if op.Hash != digest {
			return nil, fail("operation_conflict", "operation ID reused with different contents")
		}
		return summary(q.state.Tasks[op.TaskID]), nil
	}
	var target *Task
	if r.Method != "submit" {
		target = q.state.Tasks[r.TaskID]
		if target == nil || target.Owner != owner(c) {
			return nil, fail("not_found", "task not found")
		}
	}
	var result *Task
	var cancels []string
	err = q.transaction(func() error {
		switch r.Method {
		case "submit", "send":
			if strings.TrimSpace(r.Message) == "" || len(r.Message) > 128*1024 {
				return fail("invalid_request", "message must be 1..131072 bytes")
			}
			pending := 0
			for _, t := range q.state.Tasks {
				if t.State == "queued" || t.State == "running" {
					pending++
				}
			}
			if pending >= q.settings.Queue {
				return fail("queue_full", "pending queue limit reached")
			}
			access := r.Access
			if access == "" {
				access = c.Access
			}
			if level(access) == 0 || level(access) > level(c.Access) {
				return fail("access_denied", "child cannot exceed caller access")
			}
			id := newID()
			t := &Task{ID: id, Child: id, Owner: owner(c), Workspace: c.Workspace, Access: access, Depth: 1, Message: r.Message, State: "queued", Route: r.Route}
			if target != nil {
				t.Child = target.Child
				t.Session = target.Session
				t.Parent = target.Parent
				t.Depth = target.Depth
				t.Workspace = target.Workspace
				t.Access = narrower(access, target.Access)
				t.Route = target.Route
			} else {
				children := map[string]bool{}
				for _, old := range q.state.Tasks {
					if old.Owner == t.Owner {
						children[old.Child] = true
					}
					if old.Session == c.Session && old.Workspace == c.Workspace && c.Session != "" {
						t.Depth = old.Depth + 1
						t.Parent = old.Child
					}
				}
				if len(children) >= q.settings.Children {
					return fail("child_limit", "parent child limit reached")
				}
			}
			if t.Depth > q.settings.Depth {
				return fail("depth_limit", "delegation depth limit reached")
			}
			q.state.Sequence++
			t.Sequence = q.state.Sequence
			q.state.Tasks[t.ID] = t
			q.event(t, "")
			result = t
		case "cancel":
			children := map[string]bool{target.Child: true}
			if r.Tree {
				for changed := true; changed; {
					changed = false
					for _, t := range q.state.Tasks {
						if children[t.Parent] && !children[t.Child] {
							children[t.Child] = true
							changed = true
						}
					}
				}
			}
			for _, t := range q.state.Tasks {
				if children[t.Child] && (t.State == "queued" || t.State == "running") {
					t.State = "cancelled"
					q.event(t, "")
					cancels = append(cancels, t.ID)
				}
			}
			result = target
		case "ack":
			if c.Kind != "adapter" {
				return fail("access_denied", "delivery acknowledgment is adapter-only")
			}
			if target.State != "succeeded" {
				return fail("invalid_state", "only successful results may be acknowledged")
			}
			target.Delivered = true
			result = target
		}
		q.state.Operations[key] = Operation{digest, result.ID}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, id := range cancels {
		if cancel := q.running[id]; cancel != nil {
			cancel()
		}
	}
	return summary(result), nil
}
func (q *Queue) read(r Request) (any, error) {
	c, err := q.caller(r.Caller)
	if err != nil {
		return nil, err
	}
	o := owner(c)
	if r.Method == "lookup" {
		op, ok := q.state.Operations[o+":"+r.OperationID]
		if !ok {
			return nil, fail("not_found", "operation not found")
		}
		return summary(q.state.Tasks[op.TaskID]), nil
	}
	if r.Method == "read" {
		t := q.state.Tasks[r.TaskID]
		if t == nil || t.Owner != o {
			return nil, fail("not_found", "task not found")
		}
		if r.Offset < 0 || r.Offset > len(t.Result) || r.Offset < len(t.Result) && !utf8.RuneStart(t.Result[r.Offset]) {
			return nil, fail("invalid_request", "invalid result offset")
		}
		end := min(r.Offset+8192, len(t.Result))
		for end < len(t.Result) && !utf8.RuneStart(t.Result[end]) {
			end--
		}
		next := 0
		if end < len(t.Result) {
			next = end
		}
		return map[string]any{"task": summary(t), "text": t.Result[r.Offset:end], "next_offset": next, "truncated": next != 0}, nil
	}
	if r.Method == "wait" {
		if len(q.state.Events) > 0 && r.Cursor+1 < q.state.Events[0].Cursor {
			return nil, fail("cursor_expired", "event cursor expired; list tasks again")
		}
		events := []Event{}
		cursor := r.Cursor
		for _, e := range q.state.Events {
			if e.Cursor <= r.Cursor {
				continue
			}
			cursor = e.Cursor
			if e.Owner == o {
				events = append(events, e)
			}
			if len(events) >= 32 {
				break
			}
		}
		return map[string]any{"events": events, "cursor": cursor}, nil
	}
	tasks := []*Task{}
	for _, t := range q.state.Tasks {
		if t.Owner == o && t.Sequence > r.Cursor {
			tasks = append(tasks, summary(t))
		}
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Sequence < tasks[j].Sequence })
	more := len(tasks) > 32
	if more {
		tasks = tasks[:32]
	}
	next := uint64(0)
	if more {
		next = tasks[len(tasks)-1].Sequence
	}
	return map[string]any{"tasks": tasks, "next_cursor": next, "event_cursor": q.state.Cursor}, nil
}
func (q *Queue) Run(ctx context.Context) error {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	var wg sync.WaitGroup
	defer func() {
		q.mu.Lock()
		for _, c := range q.running {
			c()
		}
		q.mu.Unlock()
		wg.Wait()
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		q.mu.Lock()
		if q.fatal != nil {
			e := q.fatal
			q.mu.Unlock()
			return e
		}
		counts := map[int]int{}
		busy := map[string]bool{}
		for id := range q.running {
			t := q.state.Tasks[id]
			counts[t.Depth]++
			busy[t.Child] = true
		}
		tasks := []*Task{}
		for _, t := range q.state.Tasks {
			tasks = append(tasks, t)
		}
		sort.Slice(tasks, func(i, j int) bool { return tasks[i].Sequence < tasks[j].Sequence })
		blocked := map[string]bool{}
		for _, t := range tasks {
			if t.State == "unknown" {
				blocked[t.Child] = true
			}
			if t.State != "queued" {
				continue
			}
			if busy[t.Child] || blocked[t.Child] || counts[t.Depth] >= q.settings.Parallel {
				continue
			}
			err := q.transaction(func() error { t.State = "running"; q.event(t, ""); return nil })
			if err != nil {
				q.fatal = err
				break
			}
			jobCtx, cancel := context.WithCancel(ctx)
			q.running[t.ID] = cancel
			busy[t.Child] = true
			counts[t.Depth]++
			copyTask := *t
			wg.Add(1)
			go func() { defer wg.Done(); defer cancel(); q.execute(jobCtx, copyTask) }()
		}
		q.mu.Unlock()
	}
}
func (q *Queue) execute(ctx context.Context, t Task) {
	setSession := func(id string) error {
		q.mu.Lock()
		defer q.mu.Unlock()
		return q.transaction(func() error {
			for _, task := range q.state.Tasks {
				if task.Child == t.Child {
					task.Session = id
				}
			}
			return nil
		})
	}
	stage := func(turn, name string) {
		q.mu.Lock()
		defer q.mu.Unlock()
		if q.fatal != nil {
			return
		}
		if err := q.transaction(func() error { current := q.state.Tasks[t.ID]; current.Turn = turn; q.event(current, name); return nil }); err != nil {
			q.fatal = err
			if cancel := q.running[t.ID]; cancel != nil {
				cancel()
			}
		}
	}
	out := q.runner(ctx, t, setSession, stage)
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.running, t.ID)
	if err := q.transaction(func() error {
		current := q.state.Tasks[t.ID]
		if current.State == "cancelled" {
			return nil
		}
		current.Result = out.Text
		current.Turn = out.Turn
		current.ExitCode = out.ExitCode
		current.State = "succeeded"
		if out.Err != nil {
			current.State = "failed"
			current.Error = "Horizon execution failed"
		}
		if ctx.Err() != nil {
			current.State = "cancelled"
		}
		q.event(current, "")
		return nil
	}); err != nil {
		q.fatal = err
	}
}
