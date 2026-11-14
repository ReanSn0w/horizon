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
	"github.com/ReanSn0w/horizon/internal/plugins"
	"github.com/ReanSn0w/horizon/internal/session"
	flags "github.com/umputun/go-flags"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
func run(ctx context.Context, args []string, out, diag io.Writer) int {
	if len(args) == 1 {
		switch args[0] {
		case "horizon-plugin-metadata":
			json.NewEncoder(out).Encode(plugins.Metadata{ProtocolVersion: 1, ConfigProtocolVersion: 1, Version: buildinfo.Version, Description: "Local Horizon process supervisor"})
			return 0
		case "horizon-plugin-config-template":
			settings := daemon.Defaults()
			json.NewEncoder(out).Encode(map[string]any{"config_protocol_version": 1, "section": map[string]any{
				"parallel_per_depth": settings.Parallel, "max_active_adapter_dialogs": settings.ActiveDialogs,
				"max_depth": settings.Depth, "max_children": settings.Children, "max_queue": settings.Queue, "adapters": []string{},
			}})
			return 0
		}
	}
	var resolve struct {
		Args struct {
			ID string `positional-arg-name:"TASK_ID" required:"yes"`
		} `positional-args:"yes"`
	}
	var installOptions serviceOptions
	var debugStart struct {
		Debug bool `long:"debug" description:"Run in the current terminal"`
	}
	var logsOptions struct {
		Lines  int  `short:"n" long:"lines" default:"100" description:"Number of recent lines"`
		Follow bool `short:"f" long:"follow" description:"Follow new lines"`
	}
	parser := flags.NewParser(&struct{}{}, flags.HelpFlag|flags.PassDoubleDash)
	parser.Name = "horizon agent"
	parser.LongDescription = "Manage the Horizon supervisor. Use start --debug to run in the current terminal."
	parser.AddCommand("start", "Start the service", "Start the installed service, or run in the current terminal with --debug.", &debugStart)
	parser.AddCommand("stop", "Stop the service", "Stop the installed service.", &struct{}{})
	parser.AddCommand("status", "Show supervisor status", "Show supervisor status.", &struct{}{})
	parser.AddCommand("validate", "Validate inputs", "Check supervisor and enabled adapter settings offline.", &struct{}{})
	parser.AddCommand("logs", "Show supervisor logs", "Print recent service logs.", &logsOptions)
	parser.AddCommand("recover-input", "Recover unknown input", "Unblock an input after investigating its unknown outcome.", &resolve)
	serviceStart, _ := parser.AddCommand("service-start", "Internal service start", "Internal CLI command.", &struct{}{})
	serviceStart.Hidden = true
	serviceStop, _ := parser.AddCommand("service-stop", "Internal service stop", "Internal CLI command.", &struct{}{})
	serviceStop.Hidden = true
	serviceCommand, _ := parser.AddCommand("service", "Manage service files", "Install or remove a user service file.", &struct{}{})
	serviceCommand.AddCommand("install", "Install service definition", "Create a new service file without starting it. Existing files are never overwritten.", &installOptions)
	serviceCommand.AddCommand("remove", "Remove service definition", "Remove a stopped service file.", &struct{}{})
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") && parser.Find(args[0]) == nil {
		return runAdapterAdmin(ctx, args, os.Stdin, out, diag)
	}
	if len(args) == 0 {
		args = []string{"--help"}
	}
	remaining, err := parser.ParseArgs(args)
	if err != nil {
		var flagErr *flags.Error
		if errors.As(err, &flagErr) && flagErr.Type == flags.ErrHelp {
			fmt.Fprint(out, flagErr.Message)
			if parser.Active == nil {
				printInputHelp(ctx, parser, out, diag)
			}
			return 0
		}
		fmt.Fprintln(diag, err)
		return 2
	}
	if len(remaining) > 0 {
		fmt.Fprintln(diag, "unexpected arguments")
		return 2
	}
	command := parser.Active.Name
	// Existing service files invoke horizon-agent start directly. Horizon sets
	// HORIZON_EXECUTABLE for CLI plugins, distinguishing the public service control.
	if command == "start" && !debugStart.Debug && os.Getenv("HORIZON_EXECUTABLE") != "" {
		command = "service-start"
	} else if command == "stop" && os.Getenv("HORIZON_EXECUTABLE") != "" {
		command = "service-stop"
	}
	home, err := config.ResolveHome("")
	if err == nil {
		home, err = session.NormalizeWorkspace(home)
	}
	if err == nil {
		switch command {
		case "service":
			err = service(home, parser.Active.Active.Name, installOptions, out)
		case "start":
			err = start(ctx, home)
		case "service-start", "service-stop":
			action := "start"
			if command == "service-stop" {
				action = "stop"
			}
			err = controlService(ctx, home, action, out)
		case "validate":
			err = validate(ctx, home, out)
		case "logs":
			err = logs(ctx, home, logsOptions.Lines, logsOptions.Follow, out)
		case "recover-input":
			switch os.Getenv("HORIZON_INHERITED_ACCESS") {
			case "", "write", "full":
			default:
				fmt.Fprintln(diag, "agent: resolving input requires write access")
				return 1
			}
			taskID := resolve.Args.ID
			var response daemon.Response
			response, err = daemon.Call(ctx, home, daemon.Request{ID: "resolve-input", Method: "resolve_input",
				OperationID: "resolve-input:" + taskID, TaskID: taskID, Caller: daemon.Caller{Kind: "operator"}})
			if err == nil {
				_, err = fmt.Fprintln(out, string(response.Data))
			}
		case "status", "stop":
			method := "hello"
			if command == "stop" {
				method = "stop"
			}
			var response daemon.Response
			response, err = daemon.Call(ctx, home, daemon.Request{ID: command, Method: method})
			if err == nil {
				_, err = fmt.Fprintln(out, string(response.Data))
			}
		default:
			fmt.Fprintln(diag, "unknown agent command")
			return 2
		}
	}
	if err != nil {
		fmt.Fprintln(diag, "agent:", err)
		return 1
	}
	return 0
}
func start(ctx context.Context, home string) error { return daemon.Start(ctx, home) }
