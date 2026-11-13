package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
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
	var printOptions, installOptions serviceOptions
	parser := flags.NewParser(&struct{}{}, flags.HelpFlag|flags.PassDoubleDash)
	parser.Name = "horizon agent"
	parser.LongDescription = "Run the Horizon supervisor in foreground. Configure plugins.agent. Adapter commands remain horizon agent <adapter> (for example timer). Stop the service before manager updates."
	for _, command := range []struct{ name, description string }{{"start", "Run the supervisor in foreground"}, {"status", "Show supervisor status"}, {"stop", "Stop the supervisor"}} {
		parser.AddCommand(command.name, command.description, command.description, &struct{}{})
	}
	parser.AddCommand("resolve-input", "Resolve unknown input", "Explicitly resolve an input task with unknown outcome.", &resolve)
	serviceCommand, _ := parser.AddCommand("service", "Manage service files", "Generate or install a user service without starting it.", &struct{}{})
	serviceCommand.AddCommand("print", "Print service definition", "Print a systemd unit or launchd plist.", &printOptions)
	serviceCommand.AddCommand("install", "Install service definition", "Create a new service file and print activation commands. Existing files are never overwritten.", &installOptions)
	if len(args) == 0 {
		args = []string{"--help"}
	}
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
	command := parser.Active.Name
	home, err := config.ResolveHome("")
	if err == nil {
		home, err = session.NormalizeWorkspace(home)
	}
	if err == nil {
		switch command {
		case "service":
			action := parser.Active.Active.Name
			options := printOptions
			if action == "install" {
				options = installOptions
			}
			err = service(home, action, options, out)
		case "start":
			err = start(ctx, home)
		case "resolve-input":
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
