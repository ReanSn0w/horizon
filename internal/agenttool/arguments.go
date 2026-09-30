package agenttool

import (
	"encoding/json"
	"fmt"
)

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
		SkillRead: {"name"},
		ShellExec: {"command", "timeout_ms", "max_output_chars"},
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
