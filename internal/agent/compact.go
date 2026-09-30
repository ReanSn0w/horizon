package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ReanSn0w/horizon/internal/eventstream"
	"github.com/ReanSn0w/horizon/internal/responses"
	"github.com/ReanSn0w/horizon/internal/session"
)

type CompactClient interface {
	Compact(context.Context, responses.CompactRequest, func() error) (*responses.Response, error)
}

type CompactResult struct {
	Compacted      bool
	SessionID      string
	BoundaryTurnID string
	CompactionID   string
}

func Compact(ctx context.Context, client CompactClient, locked *session.LockedSession, value *session.Session, instructions string, maxDuration time.Duration, publish eventstream.Publish) (CompactResult, error) {
	boundary, profile, input, ok := SuccessfulWindow(value)
	if !ok {
		return CompactResult{SessionID: value.SessionID}, nil
	}
	if maxDuration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, maxDuration)
		defer cancel()
	}
	emit := func(eventType string, data any) {
		if publish != nil {
			publish(eventstream.New(eventType, value.SessionID, nil, data))
		}
	}
	emit("compaction_started", map[string]any{"boundary_turn_id": boundary})
	response, err := client.Compact(ctx, responses.CompactRequest{Model: profile.Model, Instructions: instructions, Input: input}, nil)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			emit("error", map[string]any{"code": "turn_timeout", "message": ctx.Err().Error()})
			return CompactResult{}, &Error{Code: "turn_timeout", Cause: ctx.Err()}
		}
		emit("error", map[string]any{"code": responseErrorCode(err), "message": err.Error()})
		return CompactResult{}, &Error{Code: responseErrorCode(err), Cause: err}
	}
	_, compactID, found, err := PruneAtLatestCompaction(response.Output)
	if err != nil {
		emit("error", map[string]any{"code": "invalid_compaction", "message": err.Error()})
		return CompactResult{}, &Error{Code: "invalid_compaction", Cause: err}
	}
	if !found {
		emit("error", map[string]any{"code": "incompatible_endpoint", "message": "compact endpoint returned no compaction item"})
		return CompactResult{}, &Error{Code: "incompatible_endpoint", Cause: errors.New("compact endpoint returned no compaction item")}
	}
	compact := session.Compaction{
		ID: compactID, BoundaryTurnID: boundary, ModelProfile: profile.Name,
		CreatedAt: time.Now().UTC(), Items: clone(response.Output),
	}
	if err := locked.RecordCompaction(compact); err != nil {
		emit("error", map[string]any{"code": "session_write_failed", "message": err.Error()})
		return CompactResult{}, &Error{Code: "session_write_failed", Cause: fmt.Errorf("save compacted window: %w", err)}
	}
	emit("compaction_completed", map[string]any{"boundary_turn_id": boundary, "compaction_id": compactID})
	return CompactResult{Compacted: true, SessionID: value.SessionID, BoundaryTurnID: boundary, CompactionID: compactID}, nil
}

func SuccessfulWindow(value *session.Session) (string, session.ModelProfile, []json.RawMessage, bool) {
	index, ok := value.LastCompletedTurn()
	if !ok {
		return "", session.ModelProfile{}, nil, false
	}
	turn := value.Turns[index]
	for compactIndex := len(value.Compactions) - 1; compactIndex >= 0; compactIndex-- {
		compact := value.Compactions[compactIndex]
		if compact.BoundaryTurnID == turn.ID {
			return turn.ID, turn.Model, clone(compact.Items), true
		}
	}
	for checkpointIndex := len(value.Checkpoints) - 1; checkpointIndex >= 0; checkpointIndex-- {
		checkpoint := value.Checkpoints[checkpointIndex]
		if checkpoint.TurnID == turn.ID {
			return turn.ID, turn.Model, clone(checkpoint.Items), true
		}
	}
	return "", session.ModelProfile{}, nil, false
}
