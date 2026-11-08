package agenttool

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ReanSn0w/horizon/internal/instructions"
)

type skillReadData struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content"`
	BaseDir     string `json:"base_dir"`
}

func skillReadHandler(_ context.Context, arguments json.RawMessage, env environment) outcome {
	var args skillReadArgs
	if err := decodeStrict(arguments, &args); err != nil {
		return outcome{Error: err}
	}
	if env.skills == nil {
		return outcome{Error: &ToolError{Code: "invalid_skill", Message: "skill catalog is unavailable"}}
	}
	skill, err := env.skills.Read(args.Name)
	if err != nil {
		code := "invalid_skill"
		if errors.Is(err, instructions.ErrSkillNotFound) {
			code = "skill_not_found"
		}
		return outcome{Error: &ToolError{Code: code, Message: err.Error()}}
	}
	return outcome{Data: skillReadData{Name: skill.Name, Description: skill.Description, Content: skill.Content, BaseDir: skill.BaseDir}}
}
