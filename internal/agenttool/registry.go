package agenttool

import (
	"context"
	"encoding/json"
)

const (
	FileCreate = "file_create"
	FileRead   = "file_read"
	FileUpdate = "file_update"
	DirList    = "dir_list"
	DirDelete  = "dir_delete"
	SkillRead  = "skill_read"
	ShellExec  = "shell_exec"
)

type handler func(context.Context, json.RawMessage, environment) outcome

type registeredTool struct {
	definition Definition
	handler    handler
}

func registry() []registeredTool {
	return []registeredTool{
		tool(FileCreate, "Create a new UTF-8 text file and missing parent directories.", objectSchema(
			properties(field("path", "string"), field("content", "string")), "path", "content")),
		tool(FileRead, "Read a range of lines from a UTF-8 text file.", objectSchema(
			properties(field("path", "string"), nullableInteger("start_line"), nullableInteger("line_count")), "path", "start_line", "line_count")),
		tool(FileUpdate, "Replace exactly one literal fragment in an existing UTF-8 text file.", objectSchema(
			properties(field("path", "string"), field("old_text", "string"), field("new_text", "string")), "path", "old_text", "new_text")),
		tool(DirList, "List immediate directory entries with deterministic pagination.", objectSchema(
			properties(field("path", "string"), nullableInteger("offset"), nullableInteger("limit")), "path", "offset", "limit")),
		tool(DirDelete, "Delete a file, symlink, or directory.", objectSchema(
			properties(field("path", "string"), field("recursive", "boolean")), "path", "recursive")),
		tool(SkillRead, "Load one skill's SKILL.md instructions by exact catalog name.", objectSchema(
			properties(field("name", "string")), "name")),
		tool(ShellExec, "Execute one non-interactive /bin/sh command in the workspace.", objectSchema(
			properties(field("command", "string"), nullableInteger("timeout_ms"), nullableInteger("max_output_chars")), "command", "timeout_ms", "max_output_chars")),
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
	case FileCreate:
		return fileCreateHandler
	case FileRead:
		return fileReadHandler
	case FileUpdate:
		return fileUpdateHandler
	case DirList:
		return dirListHandler
	case DirDelete:
		return dirDeleteHandler
	case SkillRead:
		return skillReadHandler
	case ShellExec:
		return shellExecHandler
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
