package agenttool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/ReanSn0w/horizon/internal/instructions"
	"github.com/ReanSn0w/horizon/internal/session"
)

type Executor struct {
	workspace    string
	artifactsDir string
	locked       *session.LockedSession
	turnID       string
	now          func() time.Time
	tools        map[string]registeredTool
	skills       *instructions.Catalog
	home         string
}

type environment struct {
	workspace    string
	artifactsDir string
	callID       string
	skills       *instructions.Catalog
	home         string
}

func (e *Executor) SetSkillCatalog(catalog *instructions.Catalog) {
	e.skills = catalog
}

func (e *Executor) SetHome(home string) { e.home = home }

func NewExecutor(workspace, artifactsDir string, locked *session.LockedSession, turnID string) *Executor {
	registered := registry()
	tools := make(map[string]registeredTool, len(registered))
	for _, item := range registered {
		tools[item.definition.Name] = item
	}
	return &Executor{workspace: workspace, artifactsDir: artifactsDir, locked: locked, turnID: turnID, now: time.Now, tools: tools}
}

func Definitions() []Definition {
	registered := registry()
	result := make([]Definition, len(registered))
	for index := range registered {
		result[index] = registered[index].definition
	}
	return result
}

// Execute records the call before invoking a handler and records the structured
// result afterward. A persistence error is returned to the runtime and must not
// be converted into a retryable tool error.
func (e *Executor) Execute(ctx context.Context, callID, name string, arguments json.RawMessage) (json.RawMessage, error) {
	if callID == "" || name == "" {
		return nil, errors.New("tool call ID and name are required")
	}
	if len(arguments) == 0 {
		return nil, errors.New("tool arguments are required")
	}
	startedAt := e.now().UTC()
	if err := e.locked.RecordToolCall(e.turnID, session.ToolCall{CallID: callID, Name: name, Arguments: append(json.RawMessage(nil), arguments...), StartedAt: startedAt}); err != nil {
		return nil, fmt.Errorf("record tool call before execution: %w", err)
	}

	var result outcome
	if err := ctx.Err(); err != nil {
		result = outcome{Error: &ToolError{Code: "cancelled", Message: err.Error()}}
	} else if registered, ok := e.tools[name]; !ok {
		result = outcome{Error: &ToolError{Code: "unknown_tool", Message: fmt.Sprintf("unknown tool %q", name)}}
	} else if validationError := validateArguments(name, arguments); validationError != nil {
		result = outcome{Error: validationError}
	} else {
		result = registered.handler(ctx, arguments, environment{workspace: e.workspace, artifactsDir: e.artifactsDir, callID: callID, skills: e.skills, home: e.home})
	}
	encoded, err := marshalResponse(result)
	if err != nil {
		return nil, err
	}
	completedAt := e.now().UTC()
	artifacts := make([]session.Artifact, len(result.Artifacts))
	for index := range result.Artifacts {
		artifacts[index] = session.Artifact{Kind: result.Artifacts[index].Kind, Path: result.Artifacts[index].Path}
	}
	if err := e.locked.RecordToolResult(e.turnID, callID, encoded, completedAt, artifacts); err != nil {
		return nil, fmt.Errorf("record tool result after execution: %w", err)
	}
	return encoded, nil
}

func decodeStrict(arguments json.RawMessage, target any) *ToolError {
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return invalid(err.Error())
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return invalid("arguments must contain exactly one JSON object")
		}
		return invalid(err.Error())
	}
	return nil
}

func resolvePath(workspace, value string) (string, *ToolError) {
	if value == "" {
		return "", invalid("path must not be empty")
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value), nil
	}
	return filepath.Clean(filepath.Join(workspace, value)), nil
}
