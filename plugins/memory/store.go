package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/ReanSn0w/horizon/internal/plugins"
	"github.com/ReanSn0w/horizon/internal/session"
)

const ledgerLimit = 1024
const ledgerRetention = 90 * 24 * time.Hour
const maxStateBytes = 4 << 20

var errBusy = errors.New("memory lock is busy")

type note struct {
	ID   string    `json:"id"`
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}
type operation struct {
	ID     string    `json:"id"`
	Digest string    `json:"digest"`
	At     time.Time `json:"at"`
}
type state struct {
	FormatVersion     int         `json:"format_version"`
	Scope             string      `json:"scope"`
	Workspace         string      `json:"workspace,omitempty"`
	Revision          uint64      `json:"revision"`
	Generation        string      `json:"generation"`
	Summary           string      `json:"summary"`
	Pending           []note      `json:"pending"`
	Operations        []operation `json:"operations"`
	LastCompactedAt   *time.Time  `json:"last_compacted_at,omitempty"`
	AttemptIncomplete bool        `json:"attempt_incomplete,omitempty"`
	LastAttemptAt     *time.Time  `json:"last_attempt_at,omitempty"`
}
type memoryStore struct {
	home, workspace string
	settings        settings
	now             func() time.Time
	save            func(string, state) error
}

func newStore(home, workspace string, s settings) *memoryStore {
	return &memoryStore{home: home, workspace: workspace, settings: s, now: time.Now, save: saveState}
}
func (m *memoryStore) path(scope string) (string, error) {
	switch scope {
	case "agent", "user":
		return filepath.Join(m.home, "memory", scope+".json"), nil
	case "workspace":
		if m.workspace == "" {
			return "", fmt.Errorf("workspace is required")
		}
		return filepath.Join(m.home, "memory", "workspaces", session.WorkspaceID(m.workspace)+".json"), nil
	}
	return "", fmt.Errorf("scope must be agent, user or workspace")
}
func (m *memoryStore) read(scope string) (state, error) {
	path, err := m.path(scope)
	if err != nil {
		return state{}, err
	}
	empty := state{FormatVersion: 1, Scope: scope, Pending: []note{}, Operations: []operation{}}
	if scope == "workspace" {
		empty.Workspace = m.workspace
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return empty, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxStateBytes+1))
	if err != nil {
		return empty, err
	}
	if len(data) > maxStateBytes {
		return empty, fmt.Errorf("memory state exceeds size limit")
	}
	var s state
	if err := plugins.Decode(data, &s); err != nil {
		return empty, fmt.Errorf("invalid memory state: %w", err)
	}
	if s.FormatVersion != 1 || s.Scope != scope || s.Workspace != empty.Workspace || s.Generation == "" {
		return empty, fmt.Errorf("incompatible memory state for %s", scope)
	}
	ids := map[string]bool{}
	for _, n := range s.Pending {
		if n.ID == "" || n.At.IsZero() || strings.TrimSpace(n.Text) == "" || ids[n.ID] {
			return empty, fmt.Errorf("invalid pending memory note")
		}
		ids[n.ID] = true
	}
	if len(s.Operations) > ledgerLimit {
		return empty, fmt.Errorf("invalid memory operation ledger")
	}
	ids = map[string]bool{}
	for _, o := range s.Operations {
		if o.ID == "" || len(o.Digest) != 64 || o.At.IsZero() || ids[o.ID] {
			return empty, fmt.Errorf("invalid memory operation ledger")
		}
		ids[o.ID] = true
	}
	return s, nil
}

// Reading uses atomic rename and creates no lock files. Mutations use the
// permanent scope lock; compaction lock is always acquired before state lock.
func (m *memoryStore) update(ctx context.Context, scope string, edit func(*state) error) error {
	path, err := m.path(scope)
	if err != nil {
		return err
	}
	lock, err := lockFile(ctx, path+".lock", false)
	if err != nil {
		return err
	}
	defer lock.Close()
	s, err := m.read(scope)
	if err != nil {
		return err
	}
	if s.Generation == "" {
		s.Generation, err = session.NewID()
		if err != nil {
			return err
		}
	}
	if err := edit(&s); err != nil {
		return err
	}
	s.Revision++
	return m.save(path, s)
}
func (m *memoryStore) add(ctx context.Context, scope, text, id string) (note, bool, error) {
	text = strings.TrimSpace(text)
	if text == "" || !utf8.ValidString(text) || len(text) > m.settings.NoteLimit {
		return note{}, false, fmt.Errorf("note must be nonempty UTF-8 and fit note_limit")
	}
	if strings.TrimSpace(id) == "" || len(id) > 512 {
		return note{}, false, fmt.Errorf("operation ID required (at most 512 bytes)")
	}
	hash := sha256.Sum256([]byte(text))
	digest := hex.EncodeToString(hash[:])
	saved := note{ID: id, At: m.now().UTC(), Text: text}
	duplicate := false
	err := m.update(ctx, scope, func(s *state) error {
		for _, n := range s.Pending {
			if n.ID == id {
				if n.Text != text {
					return fmt.Errorf("operation ID conflicts with a different note")
				}
				saved = n
				duplicate = true
				return nil
			}
		}
		for _, o := range s.Operations {
			if o.ID == id {
				if o.Digest != digest {
					return fmt.Errorf("operation ID conflicts with a different note")
				}
				duplicate = true
				saved.At = o.At
				return nil
			}
		}
		bytes := len(text)
		for _, n := range s.Pending {
			bytes += len(n.Text)
		}
		if len(s.Pending) >= m.settings.MaxPendingNotes || bytes > m.settings.MaxPendingBytes {
			return fmt.Errorf("pending memory limit reached; compact before adding more notes")
		}
		s.Pending = append(s.Pending, saved)
		retained := make([]operation, 0, len(s.Operations)+1)
		for _, o := range s.Operations {
			if saved.At.Sub(o.At) <= ledgerRetention {
				retained = append(retained, o)
			}
		}
		retained = append(retained, operation{ID: id, Digest: digest, At: saved.At})
		if len(retained) > ledgerLimit {
			retained = retained[len(retained)-ledgerLimit:]
		}
		s.Operations = retained
		return nil
	})
	return saved, duplicate, err
}
func (m *memoryStore) clear(ctx context.Context, scope string) error {
	return m.update(ctx, scope, func(s *state) error {
		generation, err := session.NewID()
		if err != nil {
			return err
		}
		s.Generation = generation
		s.Summary = ""
		s.Pending = []note{}
		s.LastCompactedAt = nil
		s.LastAttemptAt = nil
		s.AttemptIncomplete = false
		return nil
	})
}
func saveState(path string, s state) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".memory-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

type scopeLock struct{ *os.File }

func (l *scopeLock) Close() error {
	_ = syscall.Flock(int(l.Fd()), syscall.LOCK_UN)
	return l.File.Close()
}
func lockFile(ctx context.Context, path string, try bool) (*scopeLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &scopeLock{f}, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			f.Close()
			return nil, err
		}
		if try {
			f.Close()
			return nil, errBusy
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			f.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
