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
	YieldTimeMS    *int   `json:"yield_time_ms"`
}

type shellWaitArgs struct {
	ProcessID string `json:"process_id"`
	WaitMS    int    `json:"wait_ms"`
}

type shellCancelArgs struct {
	ProcessID string `json:"process_id"`
}

func validateArguments(name string, arguments json.RawMessage) *ToolError {
	required := map[string][]string{
		SkillRead:   {"name"},
		ShellExec:   {"command", "timeout_ms", "max_output_chars"},
		ShellWait:   {"process_id", "wait_ms"},
		ShellCancel: {"process_id"},
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
		if value.YieldTimeMS != nil && (*value.YieldTimeMS < 1 || *value.YieldTimeMS > 30000) {
			return invalid("yield_time_ms must be between 1 and 30000 or null")
		}
	case ShellWait:
		var value shellWaitArgs
		if err := decodeStrict(arguments, &value); err != nil {
			return err
		}
		if err := requireString("process_id", value.ProcessID); err != nil {
			return err
		}
		if value.WaitMS < 1 || value.WaitMS > 60000 {
			return invalid("wait_ms must be between 1 and 60000")
		}
	case ShellCancel:
		var value shellCancelArgs
		if err := decodeStrict(arguments, &value); err != nil {
			return err
		}
		return requireString("process_id", value.ProcessID)
	}
	return nil
}

func requireString(name, value string) *ToolError {
	if value == "" {
		return invalid(name + " must not be empty")
	}
	return nil
}
