package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ReanSn0w/horizon/internal/turninput"
)

// firstInbound reconciles any messages written by an earlier process before
// opening a new turn. A recovery containing only duplicate IDs needs no model.
func (r *Runtime) firstInbound(ctx context.Context, turnID string) (*turninput.Message, error) {
	for {
		message, err := r.Inbound.Next(ctx, turnID, true)
		if err != nil {
			return nil, &Error{Code: "input_channel_failed", Cause: err}
		}
		if message == nil {
			return nil, nil
		}
		includedTurn, err := r.Session.FindInbound(*message)
		if err != nil {
			return nil, &Error{Code: "session_input_conflict", Cause: err}
		}
		if includedTurn == "" {
			return message, nil
		}
		if err := r.Inbound.Acknowledge(ctx, message.ID, includedTurn); err != nil {
			return nil, &Error{Code: "input_channel_failed", Cause: err}
		}
	}
}

// drainInbound runs between requests and after complete tool-call batches.
// New input never cancels a provider stream or breaks a tool-call/result pair.
func (r *Runtime) drainInbound(ctx context.Context, turnID string, input []json.RawMessage, closing bool, limit error) ([]json.RawMessage, int, error) {
	added := 0
	for {
		for range turninput.MaxPending {
			if err := ctx.Err(); err != nil {
				return nil, 0, err
			}
			// New messages require another provider response before this turn
			// can close. Keep admission open while handling that response.
			message, err := r.Inbound.Next(ctx, turnID, closing && added == 0)
			if err != nil {
				return nil, 0, &Error{Code: "input_channel_failed", Cause: err}
			}
			if message == nil {
				return input, added, nil
			}
			if limit != nil {
				return nil, 0, limit
			}
			var recorded bool
			input, recorded, err = r.recordInbound(ctx, turnID, input, *message)
			if err != nil {
				return nil, 0, err
			}
			if recorded {
				added++
			}
		}
		if !closing || added != 0 {
			break
		}
		// Redelivered IDs do not replace the final close handshake.
	}
	return input, added, nil
}

func (r *Runtime) recordInbound(ctx context.Context, turnID string, input []json.RawMessage, message turninput.Message) ([]json.RawMessage, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	fields := map[string]any{"input_id": message.ID, "sequence": message.Sequence}
	r.publish("input_received", turnID, fields)
	includedTurn, added, err := r.Locked.RecordInbound(turnID, message)
	if err != nil {
		return nil, false, &Error{Code: "session_write_failed", Cause: fmt.Errorf("save active input: %w", err)}
	}
	if added {
		input = append(input, inboundUserMessage(message.Author, message.Text))
		r.publish("input_included", turnID, fields)
	}
	if err := r.Inbound.Acknowledge(ctx, message.ID, includedTurn); err != nil {
		return nil, false, &Error{Code: "input_channel_failed", Cause: err}
	}
	return input, added, nil
}
