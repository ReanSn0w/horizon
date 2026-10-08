package agenttool

import (
	"context"
	"encoding/json"
)

const (
	SkillRead   = "skill_read"
	ShellExec   = "shell_exec"
	ShellWait   = "shell_wait"
	ShellCancel = "shell_cancel"
)

type handler func(context.Context, json.RawMessage, environment) outcome

type registeredTool struct {
	definition Definition
	handler    handler
	validate   func(json.RawMessage) *ToolError
}

func registry() []registeredTool {
	return []registeredTool{
		tool(SkillRead, "Load one skill's SKILL.md instructions by exact catalog name.", objectSchema(
			properties(field("name", "string")), "name")),
		tool(ShellExec, "Execute one non-interactive /bin/sh command in the workspace.", objectSchema(
			properties(field("command", "string"), nullableInteger("timeout_ms"), nullableInteger("max_output_chars"), nullableInteger("yield_time_ms")), "command", "timeout_ms", "max_output_chars", "yield_time_ms")),
		tool(ShellWait, "Wait for a managed shell process to finish or until the wait interval expires, returning new output.", objectSchema(
			properties(field("process_id", "string"), field("wait_ms", "integer")), "process_id", "wait_ms")),
		tool(ShellCancel, "Cancel a managed shell process in this turn.", objectSchema(
			properties(field("process_id", "string")), "process_id")),
	}
}

func tool(name, description string, parameters map[string]any) registeredTool {
	return registeredTool{
		definition: Definition{Type: "function", Name: name, Description: description, Strict: true, Parameters: parameters},
		handler:    handlerFor(name),
	}
}

func handlerFor(name string) handler {
	switch name {
	case SkillRead:
		return skillReadHandler
	case ShellExec:
		return shellExecHandler
	case ShellWait:
		return shellWaitHandler
	case ShellCancel:
		return shellCancelHandler
	default:
		return func(_ context.Context, _ json.RawMessage, _ environment) outcome {
			return outcome{Error: &ToolError{Code: "not_implemented", Message: name + " is not implemented"}}
		}
	}
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}

func properties(fields ...map[string]any) map[string]any {
	result := make(map[string]any, len(fields))
	for _, item := range fields {
		for name, schema := range item {
			result[name] = schema
		}
	}
	return result
}

func field(name, kind string) map[string]any {
	return map[string]any{name: map[string]any{"type": kind}}
}

func nullableInteger(name string) map[string]any {
	return map[string]any{name: map[string]any{"type": []string{"integer", "null"}}}
}
