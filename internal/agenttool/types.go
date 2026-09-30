package agenttool

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
)

type Definition struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Strict      bool           `json:"strict"`
	Parameters  map[string]any `json:"parameters"`
}

type Response struct {
	OK    bool       `json:"ok"`
	Data  any        `json:"data,omitempty"`
	Error *ToolError `json:"error,omitempty"`
}

type ToolError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func (e *ToolError) Error() string { return e.Message }

func invalid(message string) *ToolError {
	return &ToolError{Code: "invalid_arguments", Message: message}
}

func errorForPath(code, message, path string) *ToolError {
	return &ToolError{Code: code, Message: message, Details: map[string]any{"path": path}}
}

func filesystemError(err error, path string) *ToolError {
	code := "io_error"
	switch {
	case errors.Is(err, fs.ErrNotExist):
		code = "not_found"
	case errors.Is(err, fs.ErrPermission):
		code = "permission_denied"
	case errors.Is(err, fs.ErrExist):
		code = "already_exists"
	}
	return errorForPath(code, err.Error(), path)
}

func marshalResponse(outcome outcome) (json.RawMessage, error) {
	response := Response{OK: outcome.Error == nil, Data: outcome.Data, Error: outcome.Error}
	data, err := json.Marshal(response)
	if err != nil {
		return nil, fmt.Errorf("marshal tool response: %w", err)
	}
	return data, nil
}

type outcome struct {
	Data      any
	Error     *ToolError
	Artifacts []artifact
}

type artifact struct {
	Kind string
	Path string
}
