package agenttool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ReanSn0w/horizon/internal/memory"
)

const (
	MemoryAdd  = "memory__add"
	MemoryRead = "memory__read"
)

type memoryAddArgs struct {
	Scope string `json:"scope"`
	Text  string `json:"text"`
}

type memoryReadArgs struct {
	Scope string `json:"scope"`
}

func (e *Executor) SetMemory(sessionID string, service *memory.Service) error {
	if service == nil {
		return nil
	}
	scope := map[string]any{"type": "string", "enum": []string{"agent", "user", "workspace"}}
	definitions := []registeredTool{
		{
			definition: Definition{Type: "function", Name: MemoryAdd, Description: memory.AddDescription, Strict: true, Parameters: objectSchema(properties(map[string]any{"scope": scope}, field("text", "string")), "scope", "text")},
			validate: func(arguments json.RawMessage) *ToolError {
				var args memoryAddArgs
				if err := decodeStrict(arguments, &args); err != nil {
					return err
				}
				if err := validateMemoryScope(args.Scope); err != nil {
					return err
				}
				return requireString("text", args.Text)
			},
			handler: func(ctx context.Context, arguments json.RawMessage, env environment) outcome {
				if env.access == "read" {
					return outcome{Error: &ToolError{Code: "access_denied", Message: "memory write denied in read access"}}
				}
				var args memoryAddArgs
				if err := decodeStrict(arguments, &args); err != nil {
					return outcome{Error: err}
				}
				result, err := service.Add(ctx, args.Scope, args.Text, sessionID+":"+e.turnID+":"+env.callID)
				if err != nil {
					return outcome{Error: &ToolError{Code: "memory_error", Message: err.Error()}}
				}
				return outcome{Data: result}
			},
		},
		{
			definition: Definition{Type: "function", Name: MemoryRead, Description: "Read current long and short memory in the selected scope.", Strict: true, Parameters: objectSchema(map[string]any{"scope": scope}, "scope")},
			validate: func(arguments json.RawMessage) *ToolError {
				var args memoryReadArgs
				if err := decodeStrict(arguments, &args); err != nil {
					return err
				}
				return validateMemoryScope(args.Scope)
			},
			handler: func(_ context.Context, arguments json.RawMessage, _ environment) outcome {
				var args memoryReadArgs
				if err := decodeStrict(arguments, &args); err != nil {
					return outcome{Error: err}
				}
				result, err := service.Read(args.Scope)
				if err != nil {
					return outcome{Error: &ToolError{Code: "memory_error", Message: err.Error()}}
				}
				return outcome{Data: result}
			},
		},
	}
	for _, tool := range definitions {
		name := tool.definition.Name
		if _, exists := e.tools[name]; exists {
			return fmt.Errorf("tool name collision %q", name)
		}
		e.tools[name] = tool
		e.order = append(e.order, name)
	}
	return nil
}

func validateMemoryScope(scope string) *ToolError {
	if scope != "agent" && scope != "user" && scope != "workspace" {
		return invalid("scope must be agent, user or workspace")
	}
	return nil
}
