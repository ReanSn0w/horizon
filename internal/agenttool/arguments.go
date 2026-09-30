package agenttool

import (
	"encoding/json"
	"fmt"
)

type fileCreateArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type fileReadArgs struct {
	Path      string `json:"path"`
	StartLine *int   `json:"start_line"`
	LineCount *int   `json:"line_count"`
}

type fileUpdateArgs struct {
	Path    string `json:"path"`
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

type dirListArgs struct {
	Path   string `json:"path"`
	Offset *int   `json:"offset"`
	Limit  *int   `json:"limit"`
}

type dirDeleteArgs struct {
	Path      string `json:"path"`
	Recursive *bool  `json:"recursive"`
}

type skillReadArgs struct {
	Name string `json:"name"`
}

type shellExecArgs struct {
	Command        string `json:"command"`
	TimeoutMS      *int   `json:"timeout_ms"`
	MaxOutputChars *int   `json:"max_output_chars"`
}

func validateArguments(name string, arguments json.RawMessage) *ToolError {
	required := map[string][]string{
		FileCreate: {"path", "content"},
		FileRead:   {"path", "start_line", "line_count"},
		FileUpdate: {"path", "old_text", "new_text"},
		DirList:    {"path", "offset", "limit"},
		DirDelete:  {"path", "recursive"},
		SkillRead:  {"name"},
		ShellExec:  {"command", "timeout_ms", "max_output_chars"},
	}
	properties, known := required[name]
	if !known {
		return &ToolError{Code: "unknown_tool", Message: fmt.Sprintf("unknown tool %q", name)}
	}
	var supplied map[string]json.RawMessage
	if err := json.Unmarshal(arguments, &supplied); err != nil || supplied == nil {
		return invalid("arguments must be a JSON object")
	}
	for _, property := range properties {
		if _, ok := supplied[property]; !ok {
			return invalid(property + " is required")
		}
	}
	switch name {
	case FileCreate:
		var value fileCreateArgs
		if err := decodeStrict(arguments, &value); err != nil {
			return err
		}
		return requireString("path", value.Path)
	case FileRead:
		var value fileReadArgs
		if err := decodeStrict(arguments, &value); err != nil {
			return err
		}
		if err := requireString("path", value.Path); err != nil {
			return err
		}
		if value.StartLine != nil && *value.StartLine < 1 {
			return invalid("start_line must be at least 1 or null")
		}
		if value.LineCount != nil && (*value.LineCount < 1 || *value.LineCount > 2000) {
			return invalid("line_count must be between 1 and 2000 or null")
		}
	case FileUpdate:
		var value fileUpdateArgs
		if err := decodeStrict(arguments, &value); err != nil {
			return err
		}
		if err := requireString("path", value.Path); err != nil {
			return err
		}
		if value.OldText == "" {
			return invalid("old_text must not be empty")
		}
	case DirList:
		var value dirListArgs
		if err := decodeStrict(arguments, &value); err != nil {
			return err
		}
		if err := requireString("path", value.Path); err != nil {
			return err
		}
		if value.Offset != nil && *value.Offset < 0 {
			return invalid("offset must not be negative")
		}
		if value.Limit != nil && (*value.Limit < 1 || *value.Limit > 1000) {
			return invalid("limit must be between 1 and 1000 or null")
		}
	case DirDelete:
		var value dirDeleteArgs
		if err := decodeStrict(arguments, &value); err != nil {
			return err
		}
		if err := requireString("path", value.Path); err != nil {
			return err
		}
		if value.Recursive == nil {
			return invalid("recursive must be true or false")
		}
	case SkillRead:
		var value skillReadArgs
		if err := decodeStrict(arguments, &value); err != nil {
			return err
		}
		return requireString("name", value.Name)
	case ShellExec:
		var value shellExecArgs
		if err := decodeStrict(arguments, &value); err != nil {
			return err
		}
		if err := requireString("command", value.Command); err != nil {
			return err
		}
		if value.TimeoutMS != nil && *value.TimeoutMS <= 0 {
			return invalid("timeout_ms must be positive or null")
		}
		if value.MaxOutputChars != nil && (*value.MaxOutputChars < 1000 || *value.MaxOutputChars > 100000) {
			return invalid("max_output_chars must be between 1000 and 100000 or null")
		}
	}
	return nil
}

func requireString(name, value string) *ToolError {
	if value == "" {
		return invalid(name + " must not be empty")
	}
	return nil
}
