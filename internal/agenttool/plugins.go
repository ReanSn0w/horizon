package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ReanSn0w/horizon/internal/plugins"
)

// SetExtensions adds to the same registry used for definitions and dispatch.
func (e *Executor) SetExtensions(sessionID string, extensions []plugins.Extension) error {
	for _, extension := range extensions {
		for _, tool := range extension.Description.Tools {
			name := plugins.ToolName(extension.Name, tool.Name)
			if _, exists := e.tools[name]; exists {
				return fmt.Errorf("tool name collision %q", name)
			}
			e.tools[name] = registeredTool{
				definition: Definition{Type: "function", Name: name, Description: tool.Description, Strict: true, Parameters: tool.Parameters},
				validate: func(args json.RawMessage) *ToolError {
					if err := plugins.Validate(tool.Parameters, args); err != nil {
						return invalid(err.Error())
					}
					return nil
				},
				handler: func(ctx context.Context, args json.RawMessage, env environment) outcome {
					if !plugins.Allowed(env.access, tool.Effect) {
						return outcome{Error: &ToolError{Code: "access_denied", Message: "plugin operation denied before launch in access " + env.access}}
					}
					request := extension.Request
					request.Access = env.access
					request.SessionID = sessionID
					request.TurnID = e.turnID
					request.CallID = env.callID
					request.OperationID = request.SessionID + ":" + request.TurnID + ":" + request.CallID
					request.Tool = tool.Name
					request.Arguments = append(json.RawMessage(nil), args...)
					reply, diagnostic, err := plugins.Call(ctx, extension.Path, "tool", request, plugins.OperationTimeout)
					result := outcome{}
					if err != nil {
						if failure, ok := err.(*plugins.Failure); ok {
							result.Error = &ToolError{Code: failure.Code, Message: failure.Message}
						} else {
							result.Error = &ToolError{Code: "plugin_outcome_unknown", Message: err.Error()}
						}
					} else {
						result.Data = reply.Data
					}
					if diagnostic != "" {
						if err := os.MkdirAll(env.artifactsDir, 0700); err != nil {
							return outcome{Fatal: fmt.Errorf("save plugin diagnostics after execution: %w", err)}
						}
						path := filepath.Join(env.artifactsDir, artifactPrefix(env.callID)+".plugin.stderr")
						f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
						if err == nil {
							_, err = f.WriteString(diagnostic)
							if err == nil {
								err = f.Sync()
							}
							closeErr := f.Close()
							if err == nil {
								err = closeErr
							}
						}
						if err != nil {
							return outcome{Fatal: fmt.Errorf("save plugin diagnostics after execution: %w", err)}
						}
						result.Artifacts = []artifact{{Kind: "plugin_stderr", Path: path}}
					}
					return result
				},
			}
			e.order = append(e.order, name)
		}
	}
	return nil
}
