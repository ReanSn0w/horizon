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

func protocol(ctx context.Context, op string, stdin io.Reader, stdout, stderr io.Writer) int {
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
		var settings settings
		if err == nil {
			settings, err = configuration(home)
		}
		if err == nil {
			switch op {
			case "describe":
				result = browserTools()
			case "context":
				result = []plugins.Block{}
			case "finalize":
				if request.Access != "full" {
					err = fmt.Errorf("browser finalization requires full access")
				} else {
					err = newBrowserLifecycle(home, settings).finish(ctx, request)
					result = map[string]bool{"stopped": err == nil}
				}
			case "tool":
				result, err = newBrowserLifecycle(home, settings).tool(ctx, request)
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
