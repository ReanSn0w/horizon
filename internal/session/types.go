package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const FormatVersion = 1

type Status string

const (
	StatusActive    Status = "active"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

type Session struct {
	FormatVersion  int                 `json:"format_version"`
	SessionID      string              `json:"session_id"`
	Workspace      string              `json:"workspace"`
	Title          string              `json:"title"`
	CreatedAt      time.Time           `json:"created_at"`
	LastAccessedAt time.Time           `json:"last_accessed_at"`
	Fork           *ForkOrigin         `json:"fork,omitempty"`
	Turns          []Turn              `json:"turns"`
	Checkpoints    []ContextCheckpoint `json:"context_checkpoints,omitempty"`
	Compactions    []Compaction        `json:"compactions,omitempty"`
}

type ForkOrigin struct {
	SessionID string    `json:"session_id"`
	TurnID    string    `json:"turn_id,omitempty"`
	ForkedAt  time.Time `json:"forked_at"`
}

type ModelProfile struct {
	Name             string `json:"name"`
	Model            string `json:"model"`
	Reasoning        string `json:"reasoning,omitempty"`
	CompactThreshold int    `json:"compact_threshold"`
}

type Turn struct {
	ID          string            `json:"id"`
	Status      Status            `json:"status"`
	StartedAt   time.Time         `json:"started_at"`
	CompletedAt *time.Time        `json:"completed_at,omitempty"`
	Model       ModelProfile      `json:"model"`
	Messages    []Message         `json:"messages,omitempty"`
	APIItems    []json.RawMessage `json:"api_items,omitempty"`
	ToolCalls   []ToolCall        `json:"tool_calls,omitempty"`
	Error       *TurnError        `json:"error,omitempty"`
}

type Message struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type TurnError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ToolResultState string

const (
	ToolResultPending ToolResultState = "pending"
	ToolResultKnown   ToolResultState = "known"
	ToolResultUnknown ToolResultState = "unknown"
)

type ToolCall struct {
	CallID      string          `json:"call_id"`
	Name        string          `json:"name"`
	Arguments   json.RawMessage `json:"arguments"`
	ResultState ToolResultState `json:"result_state"`
	Result      json.RawMessage `json:"result,omitempty"`
	StartedAt   time.Time       `json:"started_at"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
	Artifacts   []Artifact      `json:"artifacts,omitempty"`
}

type Artifact struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

// ContextCheckpoint is the complete reusable API input after a successful turn.
type ContextCheckpoint struct {
	TurnID    string            `json:"turn_id"`
	CreatedAt time.Time         `json:"created_at"`
	Items     []json.RawMessage `json:"items"`
}

// Compaction is a complete compacted API window tied to a successful boundary.
type Compaction struct {
	ID             string            `json:"id"`
	BoundaryTurnID string            `json:"boundary_turn_id"`
	ModelProfile   string            `json:"model_profile"`
	CreatedAt      time.Time         `json:"created_at"`
	Items          []json.RawMessage `json:"items"`
}

func (s *Session) Validate() error {
	if s.FormatVersion != FormatVersion {
		return fmt.Errorf("unsupported session format version %d", s.FormatVersion)
	}
	if s.SessionID == "" || s.Workspace == "" {
		return errors.New("session_id and workspace are required")
	}
	if s.CreatedAt.IsZero() || s.LastAccessedAt.IsZero() {
		return errors.New("created_at and last_accessed_at are required")
	}
	turnIDs := make(map[string]Status, len(s.Turns))
	for index := range s.Turns {
		turn := &s.Turns[index]
		if err := turn.validate(); err != nil {
			return fmt.Errorf("turn %d: %w", index, err)
		}
		if _, exists := turnIDs[turn.ID]; exists {
			return fmt.Errorf("duplicate turn ID %q", turn.ID)
		}
		turnIDs[turn.ID] = turn.Status
	}
	for _, checkpoint := range s.Checkpoints {
		if turnIDs[checkpoint.TurnID] != StatusCompleted {
			return fmt.Errorf("context checkpoint references non-completed turn %q", checkpoint.TurnID)
		}
	}
	for _, compact := range s.Compactions {
		if compact.ID == "" || compact.ModelProfile == "" {
			return errors.New("compaction ID and model_profile are required")
		}
		if turnIDs[compact.BoundaryTurnID] != StatusCompleted {
			return fmt.Errorf("compaction references non-completed turn %q", compact.BoundaryTurnID)
		}
	}
	return nil
}

func (t *Turn) validate() error {
	if t.ID == "" || t.StartedAt.IsZero() {
		return errors.New("id and started_at are required")
	}
	if t.Model.Name == "" || t.Model.Model == "" || t.Model.CompactThreshold <= 0 {
		return errors.New("model name, model ID, and positive compact_threshold are required")
	}
	switch t.Status {
	case StatusActive:
		if t.CompletedAt != nil {
			return errors.New("active turn must not have completed_at")
		}
	case StatusCompleted, StatusFailed, StatusCancelled:
		if t.CompletedAt == nil {
			return fmt.Errorf("%s turn requires completed_at", t.Status)
		}
	default:
		return fmt.Errorf("unknown status %q", t.Status)
	}
	for index, call := range t.ToolCalls {
		switch call.ResultState {
		case ToolResultPending, ToolResultUnknown:
			if len(call.Result) != 0 {
				return fmt.Errorf("tool call %d has a result in state %s", index, call.ResultState)
			}
		case ToolResultKnown:
			if len(call.Result) == 0 || call.CompletedAt == nil {
				return fmt.Errorf("tool call %d known result is incomplete", index)
			}
		default:
			return fmt.Errorf("tool call %d has unknown result state %q", index, call.ResultState)
		}
	}
	return nil
}

func (s *Session) LastCompletedTurn() (int, bool) {
	for index := len(s.Turns) - 1; index >= 0; index-- {
		if s.Turns[index].Status == StatusCompleted {
			return index, true
		}
	}
	return 0, false
}

func (s *Session) LatestCompletedTurnCompacted() bool {
	index, ok := s.LastCompletedTurn()
	if !ok {
		return false
	}
	for _, compact := range s.Compactions {
		if compact.BoundaryTurnID == s.Turns[index].ID {
			return true
		}
	}
	return false
}
