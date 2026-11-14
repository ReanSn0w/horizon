package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ReanSn0w/horizon/internal/buildinfo"
	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/daemon"
	"github.com/ReanSn0w/horizon/internal/maintenance"
	"github.com/ReanSn0w/horizon/internal/plugins"
	"github.com/ReanSn0w/horizon/internal/session"
	flags "github.com/umputun/go-flags"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
func run(ctx context.Context, args []string, in io.Reader, out, diag io.Writer) int {
	if len(args) == 0 {
		args = []string{"--help"}
	}
	if !strings.HasPrefix(args[0], "horizon-plugin-") {
		parser := flags.NewParser(&struct{}{}, flags.HelpFlag|flags.PassDoubleDash)
		parser.Name = "horizon subagent"
		parser.LongDescription = "Install and start the agent supervisor, then enable extensions.subagent.enabled. Children share the parent's workspace."
		parser.AddCommand("status", "Check supervisor connectivity", "Check the local supervisor without a model request.", &struct{}{})
		remaining, err := parser.ParseArgs(args)
		if err != nil {
			var flagErr *flags.Error
			if errors.As(err, &flagErr) && flagErr.Type == flags.ErrHelp {
				fmt.Fprint(out, flagErr.Message)
				return 0
			}
			fmt.Fprintln(diag, err)
			return 2
		}
		if len(remaining) > 0 {
			fmt.Fprintln(diag, "unexpected arguments")
			return 2
		}
		args = []string{parser.Active.Name}
	}
	if len(args) != 1 {
		fmt.Fprintln(diag, "expected one protocol command")
		return 2
	}
	if args[0] == "horizon-plugin-metadata" {
		json.NewEncoder(out).Encode(plugins.Metadata{ProtocolVersion: 1, AgentProtocolVersion: 1, Version: buildinfo.Version, Description: "Delegate Horizon tasks through the agent supervisor"})
		return 0
	}
	home, err := config.ResolveHome("")
	if err != nil {
		fmt.Fprintln(diag, "subagent: invalid home")
		return 2
	}
	release, err := maintenance.Acquire(home, false)
	if err != nil {
		fmt.Fprintln(diag, err)
		return 1
	}
	defer release()
	if args[0] == "status" {
		if err := daemon.Hello(ctx, home); err != nil {
			fmt.Fprintln(diag, err)
			return 1
		}
		fmt.Fprintln(out, "agent supervisor is available")
		return 0
	}
	if strings.HasPrefix(args[0], "horizon-plugin-agent-") {
		return protocol(ctx, strings.TrimPrefix(args[0], "horizon-plugin-agent-"), home, in, out)
	}
	fmt.Fprintln(diag, "subagent: unknown command")
	return 2
}
func protocol(ctx context.Context, op, home string, in io.Reader, out io.Writer) int {
	var r plugins.Request
	data, err := io.ReadAll(io.LimitReader(in, plugins.OutputLimit+1))
	if err == nil && len(data) > plugins.OutputLimit {
		err = errors.New("request exceeds limit")
	}
	if err == nil {
		err = plugins.Decode(data, &r)
	}
	var value any
	if err == nil {
		resolved, e := session.NormalizeWorkspace(home)
		if e != nil || resolved != r.Home {
			err = errors.New("host home mismatch")
		}
		workspace, e := session.NormalizeWorkspace(r.Workspace)
		if e != nil || workspace != r.Workspace || session.WorkspaceID(workspace) != r.WorkspaceID || r.OperationID == "" || len(r.OperationID) > 512 {
			err = errors.New("invalid host context")
		}
	}
	if err == nil {
		switch op {
		case "describe":
			err = daemon.Hello(ctx, r.Home)
			value = description()
		case "context":
			err = daemon.Hello(ctx, r.Home)
			value = []plugins.Block{}
		case "tool":
			value, err = invoke(ctx, r)
		default:
			err = errors.New("unsupported subagent operation")
		}
	}
	response := plugins.Reply{OK: err == nil}
	if err != nil {
		code := "subagent_error"
		var f *daemon.Failure
		if errors.As(err, &f) {
			code = f.Code
		}
		response.Error = &plugins.Failure{Code: code, Message: err.Error()}
	} else {
		response.Data, err = json.Marshal(value)
		if err != nil {
			response.OK = false
			response.Error = &plugins.Failure{Code: "subagent_error", Message: "cannot encode response"}
		}
	}
	if json.NewEncoder(out).Encode(response) != nil {
		return 1
	}
	return 0
}
func description() plugins.Description {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	tool := func(name, desc, effect string, fields map[string]any, required ...string) plugins.Tool {
		return plugins.Tool{Name: name, Description: desc, Effect: effect, Parameters: map[string]any{"type": "object", "properties": fields, "required": required, "additionalProperties": false}}
	}
	return plugins.Description{Instructions: "Delegate explicit tasks to separate Horizon sessions in the same shared workspace. No history fork or file isolation. Only manage your own children. Messages queue for the next turn. Use bounded wait and read final results; parent completion leaves children running. After outcome_unknown, read by the reported operation_id before repeating a mutation. List and event cursors are separate; list again after cursor_expired.", Tools: []plugins.Tool{
		tool("spawn", "Start a child; return accepted task ID immediately.", "write_home", map[string]any{"message": str(), "access": map[string]any{"type": "string", "enum": []any{"read", "write", "full"}}}, "message", "access"),
		tool("list", "List your children; cursor 0 starts a task page.", "read", map[string]any{"cursor": integer()}, "cursor"),
		tool("read", "Read a task/result page. Set exactly one of task_id and operation_id; use empty string for the other. Offset 0 starts result.", "read", map[string]any{"task_id": str(), "operation_id": str(), "offset": integer()}, "task_id", "operation_id", "offset"),
		tool("send", "Queue a new turn in an existing child session.", "write_home", map[string]any{"task_id": str(), "message": str()}, "task_id", "message"),
		tool("wait", "Wait up to 30000 ms for events after event cursor; does not cancel tasks.", "read", map[string]any{"cursor": integer(), "wait_ms": integer()}, "cursor", "wait_ms"),
		tool("cancel", "Cancel a child; tree=true also cancels descendants.", "write_home", map[string]any{"task_id": str(), "tree": map[string]any{"type": "boolean"}}, "task_id", "tree"),
	}}
}
