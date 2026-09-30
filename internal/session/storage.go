package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type LockedSession struct {
	store     *Store
	workspace Workspace
	id        string
	lock      *fileLock
}

func NewID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate session ID: %w", err)
	}
	return hex.EncodeToString(data), nil
}

func validID(id string) bool {
	return isWorkspaceID(id)
}

func (s *Store) SessionPath(workspace Workspace, id string) (string, error) {
	if !validID(id) {
		return "", fmt.Errorf("invalid session ID %q", id)
	}
	return filepath.Join(s.WorkspaceDir(workspace.ID), id+".json"), nil
}

func (s *Store) LockSession(workspace Workspace, id string) (*LockedSession, error) {
	if !validID(id) {
		return nil, fmt.Errorf("invalid session ID %q", id)
	}
	lock, err := acquireFileLock(filepath.Join(s.WorkspaceDir(workspace.ID), id+".lock"), true)
	if err != nil {
		if errors.Is(err, ErrBusy) {
			return nil, fmt.Errorf("session %s is busy: %w", id, ErrBusy)
		}
		return nil, fmt.Errorf("lock session %s: %w", id, err)
	}
	return &LockedSession{store: s, workspace: workspace, id: id, lock: lock}, nil
}

func (locked *LockedSession) Close() error {
	if locked.lock == nil {
		return nil
	}
	err := locked.lock.Close()
	locked.lock = nil
	return err
}

func (locked *LockedSession) Load() (*Session, error) {
	if locked.lock == nil {
		return nil, errors.New("session lock is not held")
	}
	path, err := locked.store.SessionPath(locked.workspace, locked.id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
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
	if value.SessionID != locked.id {
		return nil, fmt.Errorf("session file ID %q does not match filename ID %q", value.SessionID, locked.id)
	}
	if value.Workspace != locked.workspace.Dir {
		return nil, fmt.Errorf("session %s belongs to workspace %q, current workspace is %q", value.SessionID, value.Workspace, locked.workspace.Dir)
	}
	return &value, nil
}

func (locked *LockedSession) Save(value *Session) error {
	if locked.lock == nil {
		return errors.New("session lock is not held")
	}
	if value.SessionID != locked.id || value.Workspace != locked.workspace.Dir {
		return errors.New("session identity does not match held lock")
	}
	if err := value.Validate(); err != nil {
		return err
	}
	path, err := locked.store.SessionPath(locked.workspace, locked.id)
	if err != nil {
		return err
	}
	if err := locked.store.writeJSON(path, value); err != nil {
		return fmt.Errorf("save session %s: %w", locked.id, err)
	}
	return nil
}

func (locked *LockedSession) StartTurn(turn Turn) error {
	value, err := locked.Load()
	if err != nil {
		return err
	}
	if turn.Status != StatusActive || turn.CompletedAt != nil {
		return errors.New("new turn must be active and incomplete")
	}
	for _, existing := range value.Turns {
		if existing.Status == StatusActive {
			return fmt.Errorf("session already contains active turn %q", existing.ID)
		}
		if existing.ID == turn.ID {
			return fmt.Errorf("duplicate turn ID %q", turn.ID)
		}
	}
	value.Turns = append(value.Turns, turn)
	return locked.Save(value)
}

// RecoverInterruptedTurn converts the active record left by a terminated
// process into a failed turn with unknown outcomes for pending tool calls.
func (locked *LockedSession) RecoverInterruptedTurn(completedAt time.Time) (bool, error) {
	value, err := locked.Load()
	if err != nil {
		return false, err
	}
	found := false
	for turnIndex := range value.Turns {
		turn := &value.Turns[turnIndex]
		if turn.Status != StatusActive {
			continue
		}
		if found {
			return false, errors.New("session contains multiple active turns")
		}
		found = true
		for callIndex := range turn.ToolCalls {
			if turn.ToolCalls[callIndex].ResultState == ToolResultPending {
				turn.ToolCalls[callIndex].ResultState = ToolResultUnknown
			}
		}
		turn.Status = StatusFailed
		turn.CompletedAt = &completedAt
		turn.Error = &TurnError{Code: "process_interrupted", Message: "previous Horizon process stopped before recording the turn outcome"}
	}
	if !found {
		return false, nil
	}
	return true, locked.Save(value)
}

func (locked *LockedSession) RecordToolCall(turnID string, call ToolCall) error {
	value, turn, err := locked.activeTurn(turnID)
	if err != nil {
		return err
	}
	if call.CallID == "" || call.Name == "" || len(call.Arguments) == 0 || call.StartedAt.IsZero() {
		return errors.New("tool call ID, name, arguments, and started_at are required")
	}
	call.ResultState = ToolResultPending
	call.Result = nil
	call.CompletedAt = nil
	for _, existing := range turn.ToolCalls {
		if existing.CallID == call.CallID {
			return fmt.Errorf("duplicate tool call ID %q", call.CallID)
		}
	}
	turn.ToolCalls = append(turn.ToolCalls, call)
	return locked.Save(value)
}

func (locked *LockedSession) RecordToolResult(turnID, callID string, result json.RawMessage, completedAt time.Time, artifacts []Artifact) error {
	value, turn, err := locked.activeTurn(turnID)
	if err != nil {
		return err
	}
	for index := range turn.ToolCalls {
		call := &turn.ToolCalls[index]
		if call.CallID != callID {
			continue
		}
		if call.ResultState != ToolResultPending {
			return fmt.Errorf("tool call %q is in state %s", callID, call.ResultState)
		}
		if len(result) == 0 || completedAt.IsZero() {
			return errors.New("tool result and completion time are required")
		}
		call.ResultState = ToolResultKnown
		call.Result = append(json.RawMessage(nil), result...)
		call.CompletedAt = &completedAt
		call.Artifacts = append([]Artifact(nil), artifacts...)
		return locked.Save(value)
	}
	return fmt.Errorf("tool call %q not found", callID)
}

func (locked *LockedSession) AppendAPIItems(turnID string, items []json.RawMessage) error {
	value, turn, err := locked.activeTurn(turnID)
	if err != nil {
		return err
	}
	turn.APIItems = append(turn.APIItems, cloneRawMessages(items)...)
	return locked.Save(value)
}

func (locked *LockedSession) AppendMessage(turnID, role, text string) error {
	value, turn, err := locked.activeTurn(turnID)
	if err != nil {
		return err
	}
	if role == "" || text == "" {
		return errors.New("message role and text are required")
	}
	turn.Messages = append(turn.Messages, Message{Role: role, Text: text})
	return locked.Save(value)
}

func (locked *LockedSession) RecordCompaction(compact Compaction) error {
	value, err := locked.Load()
	if err != nil {
		return err
	}
	if compact.ID == "" || compact.BoundaryTurnID == "" || compact.ModelProfile == "" || compact.CreatedAt.IsZero() || len(compact.Items) == 0 {
		return errors.New("complete compaction metadata and output items are required")
	}
	foundBoundary := false
	for _, turn := range value.Turns {
		if turn.ID == compact.BoundaryTurnID && turn.Status == StatusCompleted {
			foundBoundary = true
			break
		}
	}
	if !foundBoundary {
		return fmt.Errorf("compaction boundary %q is not a completed turn", compact.BoundaryTurnID)
	}
	for _, existing := range value.Compactions {
		if existing.ID == compact.ID {
			return fmt.Errorf("duplicate compaction ID %q", compact.ID)
		}
	}
	compact.Items = cloneRawMessages(compact.Items)
	value.Compactions = append(value.Compactions, compact)
	return locked.Save(value)
}

func (locked *LockedSession) CompleteTurn(turnID string, status Status, turnError *TurnError, contextItems []json.RawMessage, completedAt time.Time) error {
	return locked.CompleteTurnWithCompaction(turnID, status, turnError, contextItems, nil, completedAt)
}

func (locked *LockedSession) CompleteTurnWithCompaction(turnID string, status Status, turnError *TurnError, contextItems []json.RawMessage, compact *Compaction, completedAt time.Time) error {
	if status == StatusActive {
		return errors.New("completed turn status must not be active")
	}
	value, turn, err := locked.activeTurn(turnID)
	if err != nil {
		return err
	}
	for index := range turn.ToolCalls {
		call := &turn.ToolCalls[index]
		if call.ResultState == ToolResultPending {
			if status == StatusCompleted {
				return fmt.Errorf("completed turn has pending tool call %q", call.CallID)
			}
			call.ResultState = ToolResultUnknown
		}
	}
	turn.Status = status
	turn.Error = turnError
	turn.CompletedAt = &completedAt
	if status == StatusCompleted {
		value.Checkpoints = append(value.Checkpoints, ContextCheckpoint{
			TurnID: turnID, CreatedAt: completedAt, Items: cloneRawMessages(contextItems),
		})
		if compact != nil {
			copy := *compact
			copy.BoundaryTurnID = turnID
			copy.CreatedAt = completedAt
			copy.Items = cloneRawMessages(compact.Items)
			value.Compactions = append(value.Compactions, copy)
		}
	} else if compact != nil {
		return errors.New("compaction can only be committed with a completed turn")
	}
	return locked.Save(value)
}

func (locked *LockedSession) activeTurn(turnID string) (*Session, *Turn, error) {
	value, err := locked.Load()
	if err != nil {
		return nil, nil, err
	}
	for index := range value.Turns {
		turn := &value.Turns[index]
		if turn.ID == turnID {
			if turn.Status != StatusActive {
				return nil, nil, fmt.Errorf("turn %q is not active", turnID)
			}
			return value, turn, nil
		}
	}
	return nil, nil, fmt.Errorf("turn %q not found", turnID)
}

func cloneRawMessages(items []json.RawMessage) []json.RawMessage {
	result := make([]json.RawMessage, len(items))
	for index := range items {
		result[index] = append(json.RawMessage(nil), items[index]...)
	}
	return result
}

func (s *Store) listSessionIDs(workspace Workspace) ([]string, error) {
	entries, err := os.ReadDir(s.WorkspaceDir(workspace.ID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".json" || strings.HasPrefix(name, ".") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if !validID(id) {
			return nil, fmt.Errorf("invalid session filename %q", name)
		}
		ids = append(ids, id)
	}
	return ids, nil
}
