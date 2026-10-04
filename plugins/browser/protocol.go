package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/ReanSn0w/horizon/internal/plugins"
	"github.com/ReanSn0w/horizon/internal/session"
)

func protocol(_ context.Context, op string, stdin io.Reader, stdout, stderr io.Writer) int {
	var request plugins.Request
	data, err := io.ReadAll(io.LimitReader(stdin, plugins.OutputLimit+1))
	if err == nil && len(data) > plugins.OutputLimit {
		err = fmt.Errorf("request too large")
	}
	if err == nil {
		err = plugins.Decode(data, &request)
	}
	var result any
	if err == nil {
		var home, workspace string
		home, err = session.NormalizeWorkspace(request.Home)
		if err == nil {
			workspace, err = session.NormalizeWorkspace(request.Workspace)
		}
		if err == nil && (home != request.Home || workspace != request.Workspace || session.WorkspaceID(workspace) != request.WorkspaceID || strings.TrimSpace(request.OperationID) == "" || len(request.OperationID) > 512) {
			err = fmt.Errorf("invalid host execution context")
		}
		if err == nil {
			_, err = configuration(home)
		}
		if err == nil {
			switch op {
			case "describe":
				result = plugins.Description{Tools: []plugins.Tool{}, FinalizeEffect: "unrestricted"}
			case "context":
				result = []plugins.Block{}
			default:
				err = fmt.Errorf("unsupported browser operation")
			}
		}
	}
	reply := plugins.Reply{}
	if err != nil {
		reply.Error = &plugins.Failure{Code: "browser_error", Message: err.Error()}
	} else {
		reply.OK = true
		reply.Data, err = json.Marshal(result)
		if err != nil {
			reply.OK = false
			reply.Error = &plugins.Failure{Code: "browser_error", Message: "cannot encode result"}
		}
	}
	if err := json.NewEncoder(stdout).Encode(reply); err != nil {
		fmt.Fprintln(stderr, "browser: cannot write protocol response")
		return 1
	}
	return 0
}

func browserCLI(_ context.Context, command string, args []string, stale bool, _ io.Writer, stderr io.Writer) int {
	if command == "list" && (len(args) != 0 || stale) || command == "close" && (stale && len(args) != 0 || !stale && len(args) != 1) {
		fmt.Fprintln(stderr, "browser: invalid arguments")
		return 2
	}
	fmt.Fprintln(stderr, "browser: command is not yet implemented")
	return 1
}
