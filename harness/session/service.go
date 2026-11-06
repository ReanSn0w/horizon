package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrNotFound = errors.New("session not found")

type WrongWorkspaceError struct {
	SessionID string
	Expected  string
	Actual    string
}

func (e *WrongWorkspaceError) Error() string {
	return fmt.Sprintf("session %s belongs to workspace %q; run: cd -- %s", e.SessionID, e.Expected, ShellQuote(e.Expected))
}

func IsWrongWorkspace(err error) bool {
	var target *WrongWorkspaceError
	return errors.As(err, &target)
}

func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

func IsBusy(err error) bool { return errors.Is(err, ErrBusy) }

func ShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func InitialTitle(message string) string {
	for _, line := range strings.Split(message, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		runes := []rune(line)
		if len(runes) > 80 {
			runes = runes[:80]
		}
		return string(runes)
	}
	return "Новый диалог"
}

func (s *Store) Create(workspace Workspace) (*Session, error) {
	var created *Session
	err := s.withWorkspaceLock(workspace, func() error {
		locked, value, err := s.createLocked(workspace, "Новый диалог")
		if err != nil {
			return err
		}
		defer locked.Close()
		created = value
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, err := s.TouchWorkspace(workspace); err != nil {
		return nil, err
	}
	return created, nil
}

func (s *Store) AcquireForResume(workspace Workspace, requestedID, message string) (*LockedSession, *Session, error) {
	if requestedID != "" {
		if !validID(requestedID) {
			return nil, nil, fmt.Errorf("invalid session ID %q", requestedID)
		}
		found, err := s.findSessionWorkspace(requestedID)
		if err != nil && !IsNotFound(err) {
			return nil, nil, err
		}
		if err == nil && found.Dir != workspace.Dir {
			return nil, nil, &WrongWorkspaceError{SessionID: requestedID, Expected: found.Dir, Actual: workspace.Dir}
		}
	}

	var locked *LockedSession
	var value *Session
	err := s.withWorkspaceLock(workspace, func() error {
		id := requestedID
		if id == "" {
			var err error
			id, err = s.latestSessionID(workspace)
			if err != nil {
				return err
			}
		}
		if id == "" {
			var err error
			locked, value, err = s.createLocked(workspace, InitialTitle(message))
			return err
		}
		var err error
		locked, err = s.LockSession(workspace, id)
		if err != nil {
			return err
		}
		value, err = locked.Load()
		if err != nil {
			locked.Close()
			locked = nil
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("session %s: %w", id, ErrNotFound)
			}
			return err
		}
		if _, err := locked.RecoverInterruptedTurn(s.now().UTC()); err != nil {
			locked.Close()
			locked = nil
			return err
		}
		value, err = locked.Load()
		if err != nil {
			locked.Close()
			locked = nil
			return err
		}
		next, err := s.nextAccessTime(workspace)
		if err != nil {
			locked.Close()
			locked = nil
			return err
		}
		value.LastAccessedAt = next
		if value.Title == "Новый диалог" && len(value.Turns) == 0 {
			value.Title = InitialTitle(message)
		}
		if err := locked.Save(value); err != nil {
			locked.Close()
			locked = nil
			return err
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if _, err := s.TouchWorkspace(workspace); err != nil {
		locked.Close()
		return nil, nil, err
	}
	return locked, value, nil
}

// AcquireForCompact selects an existing session without changing its access
// time or creating state. The returned exclusive lock is held by the caller.
func (s *Store) AcquireForCompact(workspace Workspace, requestedID string) (*LockedSession, *Session, error) {
	if requestedID != "" {
		if !validID(requestedID) {
			return nil, nil, fmt.Errorf("invalid session ID %q", requestedID)
		}
		found, err := s.findSessionWorkspace(requestedID)
		if err != nil {
			return nil, nil, err
		}
		if found.Dir != workspace.Dir {
			return nil, nil, &WrongWorkspaceError{SessionID: requestedID, Expected: found.Dir, Actual: workspace.Dir}
		}
	}
	id := requestedID
	if id == "" {
		var err error
		id, err = s.latestSessionID(workspace)
		if err != nil {
			return nil, nil, err
		}
		if id == "" {
			return nil, nil, ErrNotFound
		}
	}
	locked, err := s.LockSession(workspace, id)
	if err != nil {
		return nil, nil, err
	}
	value, err := locked.Load()
	if err != nil {
		locked.Close()
		return nil, nil, err
	}
	return locked, value, nil
}

func (s *Store) createLocked(workspace Workspace, title string) (*LockedSession, *Session, error) {
	id, err := NewID()
	if err != nil {
		return nil, nil, err
	}
	locked, err := s.LockSession(workspace, id)
	if err != nil {
		return nil, nil, err
	}
	now, err := s.nextAccessTime(workspace)
	if err != nil {
		locked.Close()
		return nil, nil, err
	}
	value := &Session{FormatVersion: FormatVersion, SessionID: id, Workspace: workspace.Dir, Title: title, CreatedAt: now, LastAccessedAt: now, Turns: []Turn{}}
	if err := locked.Save(value); err != nil {
		locked.Close()
		return nil, nil, err
	}
	return locked, value, nil
}

func (s *Store) latestSessionID(workspace Workspace) (string, error) {
	ids, err := s.listSessionIDs(workspace)
	if err != nil {
		return "", err
	}
	var latest *Session
	for _, id := range ids {
		value, err := s.ReadSnapshot(workspace, id)
		if err != nil {
			return "", err
		}
		if latest == nil || value.LastAccessedAt.After(latest.LastAccessedAt) || (value.LastAccessedAt.Equal(latest.LastAccessedAt) && value.SessionID > latest.SessionID) {
			latest = value
		}
	}
	if latest == nil {
		return "", nil
	}
	return latest.SessionID, nil
}

func (s *Store) nextAccessTime(workspace Workspace) (time.Time, error) {
	ids, err := s.listSessionIDs(workspace)
	if err != nil {
		return time.Time{}, err
	}
	next := s.now().UTC()
	for _, id := range ids {
		value, err := s.ReadSnapshot(workspace, id)
		if err != nil {
			return time.Time{}, err
		}
		if !next.After(value.LastAccessedAt) {
			next = value.LastAccessedAt.Add(time.Nanosecond)
		}
	}
	return next, nil
}

func (s *Store) ReadSnapshot(workspace Workspace, id string) (*Session, error) {
	path, err := s.SessionPath(workspace, id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("session %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	var value Session
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("parse session %q: %w", path, err)
	}
	if err := value.Validate(); err != nil {
		return nil, fmt.Errorf("validate session %q: %w", path, err)
	}
	if value.SessionID != id || value.Workspace != workspace.Dir {
		return nil, fmt.Errorf("session identity in %q does not match its location", path)
	}
	return &value, nil
}

func (s *Store) ReadForWorkspace(workspace Workspace, id string) (*Session, error) {
	value, err := s.ReadSnapshot(workspace, id)
	if err == nil {
		return value, nil
	}
	if !IsNotFound(err) {
		return nil, err
	}
	found, findErr := s.findSessionWorkspace(id)
	if findErr == nil && found.Dir != workspace.Dir {
		return nil, &WrongWorkspaceError{SessionID: id, Expected: found.Dir, Actual: workspace.Dir}
	}
	return nil, err
}

func (s *Store) findSessionWorkspace(id string) (Workspace, error) {
	if !validID(id) {
		return Workspace{}, fmt.Errorf("invalid session ID %q", id)
	}
	registry, err := s.loadOrRecoverRegistry()
	if err != nil {
		return Workspace{}, err
	}
	var found *Workspace
	for _, workspace := range registry {
		path, pathErr := s.SessionPath(workspace, id)
		if pathErr != nil {
			return Workspace{}, pathErr
		}
		if _, statErr := os.Stat(path); statErr == nil {
			if found != nil {
				return Workspace{}, fmt.Errorf("session ID %s exists in multiple workspaces", id)
			}
			copy := workspace
			found = &copy
		} else if !errors.Is(statErr, fs.ErrNotExist) {
			return Workspace{}, statErr
		}
	}
	if found == nil {
		return Workspace{}, fmt.Errorf("session %s: %w", id, ErrNotFound)
	}
	return *found, nil
}

type ListItem struct {
	Session *Session
	Active  bool
}

func (s *Store) List(workspace Workspace, activeOnly bool) ([]ListItem, error) {
	ids, err := s.listSessionIDs(workspace)
	if err != nil {
		return nil, err
	}
	items := make([]ListItem, 0, len(ids))
	for _, id := range ids {
		value, err := s.ReadSnapshot(workspace, id)
		if err != nil {
			return nil, err
		}
		locked, lockErr := s.LockSession(workspace, id)
		active := errors.Is(lockErr, ErrBusy)
		if lockErr == nil {
			_ = locked.Close()
		} else if !active {
			return nil, lockErr
		}
		if activeOnly && !active {
			continue
		}
		items = append(items, ListItem{Session: value, Active: active})
	}
	sort.Slice(items, func(i, j int) bool {
		left, right := items[i].Session, items[j].Session
		if left.LastAccessedAt.Equal(right.LastAccessedAt) {
			return left.SessionID > right.SessionID
		}
		return left.LastAccessedAt.After(right.LastAccessedAt)
	})
	return items, nil
}

func WriteHistory(writer io.Writer, value *Session, turns int) error {
	if turns <= 0 {
		return errors.New("turn count must be positive")
	}
	buffered := bufio.NewWriter(writer)
	fmt.Fprintf(buffered, "Session %s — %s\n", value.SessionID, value.Title)
	start := len(value.Turns) - turns
	if start < 0 {
		start = 0
	}
	for _, turn := range value.Turns[start:] {
		fmt.Fprintf(buffered, "\nTurn %s [%s]\n", turn.ID, turn.Status)
		for _, message := range turn.Messages {
			fmt.Fprintf(buffered, "%s: %s\n", message.Role, message.Text)
		}
		for _, call := range turn.ToolCalls {
			fmt.Fprintf(buffered, "tool %s (%s) [%s]\n", call.Name, call.CallID, call.ResultState)
			fmt.Fprintf(buffered, "  arguments: %s\n", truncate(string(call.Arguments), 16000))
			switch call.ResultState {
			case ToolResultKnown:
				fmt.Fprintf(buffered, "  result: %s\n", truncate(string(call.Result), 16000))
			case ToolResultUnknown:
				fmt.Fprintln(buffered, "  result: unknown; inspect actual state before repeating")
			}
		}
		if turn.Error != nil {
			fmt.Fprintf(buffered, "error %s: %s\n", turn.Error.Code, turn.Error.Message)
		}
	}
	return buffered.Flush()
}

func truncate(value string, max int) string {
	if utf8.RuneCountInString(value) <= max {
		return value
	}
	runes := []rune(value)
	return string(runes[:max/2]) + "… [truncated] …" + string(runes[len(runes)-max/2:])
}
