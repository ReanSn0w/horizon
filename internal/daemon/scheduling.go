package daemon

import "strings"

func (t *Task) dialogKey() string {
	if t.Depth != 1 || !strings.HasPrefix(t.Owner, "adapter:") {
		return ""
	}
	if t.Route != "" {
		return t.Owner + "\x00route\x00" + t.Route
	}
	return t.Owner + "\x00workspace\x00" + t.Workspace
}

type runnerSlots struct {
	depth    map[int]int
	children map[string]bool
	sessions map[[2]string]bool
	dialogs  map[string]bool
}

func (q *Queue) occupiedSlots() runnerSlots {
	slots := runnerSlots{depth: map[int]int{}, children: map[string]bool{}, sessions: map[[2]string]bool{}, dialogs: map[string]bool{}}
	for id := range q.running {
		slots.reserve(q.state.Tasks[id])
	}
	return slots
}

func (s runnerSlots) reserve(t *Task) {
	s.children[t.Child] = true
	if t.Session != "" {
		s.sessions[[2]string{t.Workspace, t.Session}] = true
	}
	if key := t.dialogKey(); key != "" {
		s.dialogs[key] = true
	} else {
		s.depth[t.Depth]++
	}
}

func (s runnerSlots) available(settings Settings, t *Task) bool {
	if s.children[t.Child] || s.sessions[[2]string{t.Workspace, t.Session}] {
		return false
	}
	if key := t.dialogKey(); key != "" {
		return !s.dialogs[key] && len(s.dialogs) < settings.ActiveDialogs
	}
	return s.depth[t.Depth] < settings.Parallel
}
