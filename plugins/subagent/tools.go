package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ReanSn0w/horizon/internal/daemon"
	"github.com/ReanSn0w/horizon/internal/plugins"
)

type arguments struct {
	Message     string `json:"message"`
	Access      string `json:"access"`
	TaskID      string `json:"task_id"`
	OperationID string `json:"operation_id"`
	Cursor      uint64 `json:"cursor"`
	Offset      int    `json:"offset"`
	WaitMS      int    `json:"wait_ms"`
	Tree        bool   `json:"tree"`
}

func invoke(ctx context.Context, r plugins.Request) (any, error) {
	var selected *plugins.Tool
	for _, t := range description().Tools {
		if t.Name == r.Tool {
			selected = &t
			break
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("unknown subagent tool")
	}
	if !plugins.Allowed(r.Access, selected.Effect) {
		return nil, &daemon.Failure{Code: "access_denied", Message: "tool effect is not allowed in current access mode"}
	}
	if r.SessionID == "" || r.TurnID == "" || r.CallID == "" || r.OperationID == "" {
		return nil, fmt.Errorf("tool requires host session/turn/call/operation context")
	}
	if err := plugins.Validate(selected.Parameters, r.Arguments); err != nil {
		return nil, err
	}
	var a arguments
	if err := plugins.Decode(r.Arguments, &a); err != nil {
		return nil, err
	}
	req := daemon.Request{ID: r.CallID, Method: r.Tool, OperationID: r.OperationID, Caller: daemon.Caller{Kind: "subagent", Workspace: r.Workspace, Session: r.SessionID, Access: r.Access}, TaskID: a.TaskID, Message: a.Message, Access: a.Access, Cursor: a.Cursor, Offset: a.Offset, WaitMS: a.WaitMS, Tree: a.Tree}
	if r.Tool == "spawn" {
		req.Method = "submit"
	}
	if r.Tool == "read" {
		if (a.TaskID == "") == (a.OperationID == "") || a.Offset < 0 {
			return nil, fmt.Errorf("read requires exactly one identifier and a nonnegative offset")
		}
		if a.OperationID != "" {
			lookup := req
			lookup.Method = "lookup"
			lookup.OperationID = a.OperationID
			reply, err := daemon.Call(ctx, r.Home, lookup)
			if err != nil {
				return nil, err
			}
			var task daemon.Task
			if err = json.Unmarshal(reply.Data, &task); err != nil {
				return nil, err
			}
			req.TaskID = task.ID
		}
	}
	if r.Tool == "wait" && (a.WaitMS < 0 || a.WaitMS > 30000) {
		return nil, fmt.Errorf("wait_ms must be 0..30000")
	}
	response, err := daemon.Call(ctx, r.Home, req)
	if err != nil {
		var failure *daemon.Failure
		if errors.As(err, &failure) && failure.Code == "outcome_unknown" {
			return nil, &daemon.Failure{Code: "outcome_unknown", Message: fmt.Sprintf("operation_id=%s; use subagent__read with operation_id and empty task_id before repeating: %s", r.OperationID, failure.Message)}
		}
		return nil, err
	}
	return response.Data, nil
}
