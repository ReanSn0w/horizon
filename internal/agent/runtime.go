package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ReanSn0w/horizon/internal/agenttool"
	"github.com/ReanSn0w/horizon/internal/decision"
	"github.com/ReanSn0w/horizon/internal/eventstream"
	"github.com/ReanSn0w/horizon/internal/instructions"
	"github.com/ReanSn0w/horizon/internal/plugins"
	"github.com/ReanSn0w/horizon/internal/responses"
	"github.com/ReanSn0w/horizon/internal/session"
)

const Introduction = "You are an autonomous coding agent operating through Horizon. Work carefully in the current workspace, use tools when needed, and continue until the user's task is complete."

type ResponseClient interface {
	Stream(context.Context, responses.Request, func() error, func(responses.Event)) (*responses.Response, error)
}

type Runtime struct {
	Client       ResponseClient
	Locked       *session.LockedSession
	Store        *session.Store
	Workspace    session.Workspace
	Session      *session.Session
	ProfileName  string
	Access       string
	Reviewer     decision.Reviewer
	Profile      session.ModelProfile
	Instructions *instructions.Snapshot
	Extensions   []plugins.Extension
	MaxRequests  int
	MaxDuration  time.Duration
	Publish      eventstream.Publish
	Now          func() time.Time
}

type Result struct {
	SessionID string
	TurnID    string
	Text      string
}

type Error struct {
	Code   string
	TurnID string
	Cause  error
}

func (e *Error) Error() string {
	if e.Cause == nil {
		return e.Code
	}
	return e.Cause.Error()
}

func (e *Error) Unwrap() error { return e.Cause }

func (r *Runtime) Run(ctx context.Context, message string) (Result, error) {
	if r.Client == nil || r.Locked == nil || r.Store == nil || r.Session == nil || r.Instructions == nil {
		return Result{}, errors.New("agent runtime is incomplete")
	}
	now := r.Now
	if now == nil {
		now = time.Now
	}
	parentContext := ctx
	if r.MaxDuration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.MaxDuration)
		defer cancel()
	}
	turnID, err := session.NewID()
	if err != nil {
		return Result{}, err
	}
	started := now().UTC()
	turn := session.Turn{
		ID: turnID, Status: session.StatusActive, StartedAt: started, Model: r.Profile,
		Messages: []session.Message{{Role: "user", Text: message}},
	}
	if err := r.Locked.StartTurn(turn); err != nil {
		return Result{}, fmt.Errorf("start turn: %w", err)
	}
	defer r.finalizeTurn(turnID)
	skills := make([]map[string]string, 0)
	for _, skill := range r.Instructions.Skills.Summaries() {
		skills = append(skills, map[string]string{"id": skill.ID, "name": skill.Name})
	}
	fingerprint := sha256.Sum256([]byte(r.Instructions.Prompt))
	r.publish("turn_started", turnID, map[string]any{"model_profile": r.ProfileName, "model": r.Profile.Model, "home": r.Store.Home, "skills": skills, "instructions_sha256": hex.EncodeToString(fingerprint[:])})

	input, err := BuildInput(r.Session)
	if err != nil {
		return Result{}, r.fail(turnID, "context_reconstruction_failed", err, now)
	}
	input = append(input, responses.UserMessage(message))
	executor := agenttool.NewExecutor(r.Workspace.Dir, r.Store.ArtifactsDir(r.Workspace, r.Session.SessionID), r.Locked, turnID)
	executor.SetSkillCatalog(r.Instructions.Skills)
	executor.SetHome(r.Store.Home)
	executor.SetCommandReview(r.Access, r.Reviewer)
	if err := executor.SetExtensions(r.Session.SessionID, r.Extensions); err != nil {
		return Result{}, r.fail(turnID, "tool_registry_failed", err, now)
	}
	var automaticCompact *session.Compaction
	requests := 0
	var requestStarted time.Time
	beforeAttempt := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if r.MaxRequests > 0 && requests >= r.MaxRequests {
			return errRequestLimit
		}
		requests++
		requestStarted = now().UTC()
		r.publish("model_request_started", turnID, map[string]any{"attempt": requests})
		return nil
	}

	for {
		request := responses.Request{
			Model: r.Profile.Model, Instructions: r.Instructions.Prompt, Input: clone(input),
			Tools: toolDefinitions(executor.Definitions()), ParallelToolCalls: false, Store: false,
			Include:           []string{"reasoning.encrypted_content"},
			ContextManagement: []responses.ContextPolicy{{Type: "compaction", CompactThreshold: r.Profile.CompactThreshold}},
		}
		if r.Profile.Reasoning != "" {
			request.Reasoning = &responses.Reasoning{Effort: r.Profile.Reasoning}
		}
		response, err := r.Client.Stream(ctx, request, beforeAttempt, func(event responses.Event) {
			if event.Type == "response.output_text.delta" && event.Delta != "" {
				r.publish("progress", turnID, map[string]any{"text": event.Delta})
			}
		})
		if !requestStarted.IsZero() {
			fields := map[string]any{"attempt": requests, "duration_ms": now().UTC().Sub(requestStarted).Milliseconds(), "ok": err == nil}
			if err != nil {
				fields["code"] = responseErrorCode(err)
			}
			r.publish("model_request_completed", turnID, fields)
			requestStarted = time.Time{}
		}
		if err != nil {
			return Result{}, r.stop(turnID, err, ctx, parentContext, now)
		}
		if err := r.Locked.AppendAPIItems(turnID, response.Output); err != nil {
			return Result{}, r.fail(turnID, "session_write_failed", err, now)
		}
		input = append(input, clone(response.Output)...)
		_, newCompactID, newCompact, compactErr := PruneAtLatestCompaction(response.Output)
		if compactErr != nil {
			return Result{}, r.fail(turnID, "invalid_compaction", compactErr, now)
		}
		if window, compactID, found, pruneErr := PruneAtLatestCompaction(input); pruneErr != nil {
			return Result{}, r.fail(turnID, "invalid_compaction", pruneErr, now)
		} else if found && newCompact {
			input = window
			automaticCompact = &session.Compaction{ID: compactID, ModelProfile: r.ProfileName, Items: clone(window)}
			r.publish("compaction_completed", turnID, map[string]any{"compaction_id": newCompactID, "automatic": true})
		}
		calls, err := responses.FunctionCalls(response.Output)
		if err != nil {
			return Result{}, r.fail(turnID, "invalid_response", err, now)
		}
		if len(calls) != 0 {
			for _, call := range calls {
				toolStarted := now().UTC()
				r.publish("tool_started", turnID, map[string]any{"call_id": call.CallID, "name": call.Name, "arguments": json.RawMessage(call.Arguments)})
				result, executeErr := executor.Execute(ctx, call.CallID, call.Name, call.Arguments)
				if executeErr != nil {
					return Result{}, r.fail(turnID, "tool_execution_failed", executeErr, now)
				}
				output, outputErr := responses.FunctionOutput(call.CallID, result)
				if outputErr != nil {
					return Result{}, r.fail(turnID, "invalid_tool_result", outputErr, now)
				}
				input = append(input, output)
				r.publish("tool_completed", turnID, map[string]any{"call_id": call.CallID, "name": call.Name, "result": json.RawMessage(result), "duration_ms": now().UTC().Sub(toolStarted).Milliseconds()})
				if ctx.Err() != nil {
					return Result{}, r.stop(turnID, ctx.Err(), ctx, parentContext, now)
				}
			}
			continue
		}
		if ctx.Err() != nil {
			return Result{}, r.stop(turnID, ctx.Err(), ctx, parentContext, now)
		}

		text, err := responses.OutputText(response.Output)
		if err != nil {
			return Result{}, r.fail(turnID, "invalid_response", err, now)
		}
		if text != "" {
			if err := r.Locked.AppendMessage(turnID, "assistant", text); err != nil {
				return Result{}, r.fail(turnID, "session_write_failed", err, now)
			}
		}
		completed := now().UTC()
		if err := r.Locked.CompleteTurnWithCompaction(turnID, session.StatusCompleted, nil, input, automaticCompact, completed); err != nil {
			r.publish("turn_failed", turnID, map[string]any{"code": "session_write_failed", "message": err.Error()})
			return Result{}, &Error{Code: "session_write_failed", TurnID: turnID, Cause: err}
		}
		completionData := map[string]any{"text": text}
		if len(response.Usage) != 0 && string(response.Usage) != "null" {
			completionData["usage"] = json.RawMessage(response.Usage)
		}
		r.publish("turn_completed", turnID, completionData)
		return Result{SessionID: r.Session.SessionID, TurnID: turnID, Text: text}, nil
	}
}

func (r *Runtime) finalizeTurn(turnID string) {
	if len(r.Extensions) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, extension := range r.Extensions {
		if extension.Description.FinalizeEffect == "" || !plugins.Allowed(r.Access, extension.Description.FinalizeEffect) {
			continue
		}
		request := extension.Request
		request.SessionID = r.Session.SessionID
		request.TurnID = turnID
		request.Access = r.Access
		request.OperationID = r.Session.SessionID + ":" + turnID + ":finalize"
		_, _, err := plugins.Call(ctx, extension.Path, "finalize", request, 10*time.Second)
		if err != nil {
			r.publish("plugin_finalize_failed", turnID, map[string]any{"plugin": extension.Name, "message": err.Error()})
		}
	}
}

func PruneAtLatestCompaction(items []json.RawMessage) ([]json.RawMessage, string, bool, error) {
	latest := -1
	compactID := ""
	for index, item := range items {
		var header struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if err := json.Unmarshal(item, &header); err != nil {
			return nil, "", false, fmt.Errorf("decode context item %d: %w", index, err)
		}
		if header.Type == "compaction" {
			if header.ID == "" {
				return nil, "", false, fmt.Errorf("compaction item %d has no ID", index)
			}
			latest, compactID = index, header.ID
		}
	}
	if latest < 0 {
		return items, "", false, nil
	}
	return clone(items[latest:]), compactID, true, nil
}

var errRequestLimit = errors.New("model request limit exceeded; another request is required to finish the turn")

func (r *Runtime) stop(turnID string, cause error, turnContext, parentContext context.Context, now func() time.Time) error {
	status := session.StatusFailed
	code := responseErrorCode(cause)
	if errors.Is(cause, errRequestLimit) {
		code = "request_limit_exceeded"
	} else if parentContext.Err() != nil {
		code = "turn_cancelled"
		status = session.StatusCancelled
	} else if errors.Is(turnContext.Err(), context.DeadlineExceeded) {
		code = "turn_timeout"
	}
	turnError := &session.TurnError{Code: code, Message: cause.Error()}
	if saveErr := r.Locked.CompleteTurn(turnID, status, turnError, nil, now().UTC()); saveErr != nil {
		r.publish("turn_failed", turnID, map[string]any{"code": "session_write_failed", "message": saveErr.Error()})
		return &Error{Code: "session_write_failed", TurnID: turnID, Cause: fmt.Errorf("%v; additionally failed to save turn: %w", cause, saveErr)}
	}
	eventType := "turn_failed"
	if status == session.StatusCancelled {
		eventType = "turn_cancelled"
	}
	r.publish(eventType, turnID, map[string]any{"code": code, "message": cause.Error()})
	return &Error{Code: code, TurnID: turnID, Cause: cause}
}

func (r *Runtime) fail(turnID, code string, cause error, now func() time.Time) error {
	turnError := &session.TurnError{Code: code, Message: cause.Error()}
	if saveErr := r.Locked.CompleteTurn(turnID, session.StatusFailed, turnError, nil, now().UTC()); saveErr != nil {
		r.publish("turn_failed", turnID, map[string]any{"code": "session_write_failed", "message": saveErr.Error()})
		return &Error{Code: "session_write_failed", TurnID: turnID, Cause: fmt.Errorf("%v; additionally failed to save turn: %w", cause, saveErr)}
	}
	r.publish("turn_failed", turnID, map[string]any{"code": code, "message": cause.Error()})
	return &Error{Code: code, TurnID: turnID, Cause: cause}
}

func (r *Runtime) publish(eventType, turnID string, data any) {
	if r.Publish == nil {
		return
	}
	r.Publish(eventstream.New(eventType, r.Session.SessionID, &turnID, data))
}

func BuildInput(value *session.Session) ([]json.RawMessage, error) {
	boundaryIndex := -1
	var input []json.RawMessage
	if index, ok := value.LastCompletedTurn(); ok {
		boundaryIndex = index
		boundaryID := value.Turns[index].ID
		for compactIndex := len(value.Compactions) - 1; compactIndex >= 0; compactIndex-- {
			if value.Compactions[compactIndex].BoundaryTurnID == boundaryID {
				input = clone(value.Compactions[compactIndex].Items)
				break
			}
		}
		if len(input) == 0 {
			for checkpointIndex := len(value.Checkpoints) - 1; checkpointIndex >= 0; checkpointIndex-- {
				if value.Checkpoints[checkpointIndex].TurnID == boundaryID {
					input = clone(value.Checkpoints[checkpointIndex].Items)
					break
				}
			}
		}
	}

	for index := boundaryIndex + 1; index < len(value.Turns); index++ {
		turn := value.Turns[index]
		for _, message := range turn.Messages {
			if message.Role == "user" {
				input = append(input, responses.UserMessage(message.Text))
			}
		}
		calls := make(map[string]session.ToolCall, len(turn.ToolCalls))
		hasUnknown := false
		for _, call := range turn.ToolCalls {
			calls[call.CallID] = call
			hasUnknown = hasUnknown || call.ResultState == session.ToolResultUnknown || call.ResultState == session.ToolResultPending
		}
		for _, item := range turn.APIItems {
			if hasUnknown {
				continue
			}
			var header struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
			}
			if err := json.Unmarshal(item, &header); err != nil {
				return nil, fmt.Errorf("turn %s contains invalid API item: %w", turn.ID, err)
			}
			if header.Type == "compaction" {
				continue
			}
			if header.Type != "function_call" {
				input = append(input, append(json.RawMessage(nil), item...))
				continue
			}
			call, ok := calls[header.CallID]
			if !ok || call.ResultState != session.ToolResultKnown {
				continue
			}
			input = append(input, append(json.RawMessage(nil), item...))
			output, err := responses.FunctionOutput(call.CallID, call.Result)
			if err != nil {
				return nil, err
			}
			input = append(input, output)
		}
		input = append(input, responses.DeveloperMessage(failedTurnSummary(turn)))
	}
	return input, nil
}

func failedTurnSummary(turn session.Turn) string {
	message := fmt.Sprintf("Horizon turn %s ended with status %s and did not complete successfully.", turn.ID, turn.Status)
	if turn.Error != nil {
		message += fmt.Sprintf(" Reason: %s: %s.", turn.Error.Code, turn.Error.Message)
	}
	completedProcesses := make(map[string]bool)
	for _, call := range turn.ToolCalls {
		if call.ResultState != session.ToolResultKnown {
			continue
		}
		var result struct {
			Data struct {
				ProcessID string `json:"process_id"`
				Status    string `json:"status"`
			} `json:"data"`
		}
		if json.Unmarshal(call.Result, &result) == nil && result.Data.ProcessID != "" && result.Data.Status == "completed" {
			completedProcesses[result.Data.ProcessID] = true
		}
	}
	for _, call := range turn.ToolCalls {
		if call.ResultState == session.ToolResultUnknown || call.ResultState == session.ToolResultPending {
			message += fmt.Sprintf(" Tool %s with arguments %s has an unknown outcome; inspect the actual workspace state before deciding whether to repeat it.", call.Name, call.Arguments)
		} else if call.ResultState == session.ToolResultKnown {
			message += fmt.Sprintf(" Tool %s completed with recorded result %s.", call.Name, call.Result)
			var result struct {
				Data struct {
					ProcessID string `json:"process_id"`
					Status    string `json:"status"`
				} `json:"data"`
			}
			if json.Unmarshal(call.Result, &result) == nil && result.Data.Status == "running" && !completedProcesses[result.Data.ProcessID] {
				message += fmt.Sprintf(" Process %s was still running; its final outcome is unknown. Inspect external state before repeating the command.", result.Data.ProcessID)
			}
		}
	}
	return message
}

func toolDefinitions(registered ...[]agenttool.Definition) []responses.Tool {
	definitions := agenttool.Definitions()
	if len(registered) > 0 {
		definitions = registered[0]
	}
	result := make([]responses.Tool, len(definitions))
	for index, definition := range definitions {
		result[index] = responses.Tool(definition)
	}
	return result
}

func responseErrorCode(err error) string {
	var apiError *responses.Error
	if errors.As(err, &apiError) {
		return apiError.Code
	}
	return "api_error"
}

func clone(items []json.RawMessage) []json.RawMessage {
	result := make([]json.RawMessage, len(items))
	for index := range items {
		result[index] = append(json.RawMessage(nil), items[index]...)
	}
	return result
}
