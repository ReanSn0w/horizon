package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ReanSn0w/horizon/internal/memory"
	"github.com/ReanSn0w/horizon/session"
	flags "github.com/umputun/go-flags"
)

type memoryCommand struct{}

type memoryOptions struct {
	app       *App
	global    *globalOptions
	Scope     string `long:"scope" choice:"agent" choice:"user" choice:"workspace" required:"true" description:"memory scope"`
	Workspace string `long:"workspace" description:"workspace directory; defaults to current directory"`
}

type memoryAddCommand struct {
	memoryOptions
	Text string `long:"text" required:"true" description:"note text"`
	ID   string `long:"id" description:"deduplication ID"`
}

type memoryShowCommand struct{ memoryOptions }
type memoryCompactCommand struct{ memoryOptions }
type memoryClearCommand struct{ memoryOptions }

type memoryWatchCommand struct {
	app       *App
	global    *globalOptions
	Workspace string `long:"workspace" description:"workspace directory; defaults to current directory"`
	Period    string `long:"period" default:"1m" description:"polling interval from 1s to 24h"`
}

func (command memoryOptions) service(write bool) (*memory.Service, context.Context, context.CancelFunc, error) {
	access, err := effectiveAccess("write")
	if err != nil {
		return nil, nil, nil, usage("memory: "+err.Error(), err)
	}
	if write && access == "read" {
		return nil, nil, nil, usage("memory: writes forbidden in access read", nil)
	}
	home, cfg, err := command.app.loadConfig(command.global)
	if err != nil {
		return nil, nil, nil, err
	}
	service, err := memory.NewForCLI(home, command.Workspace, cfg)
	if err != nil {
		return nil, nil, nil, failure("memory: "+err.Error(), err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	return service, ctx, stop, nil
}

func writeMemoryResult(command memoryOptions, value any, err error) error {
	if err != nil {
		return failure("memory: "+err.Error(), err)
	}
	if err := json.NewEncoder(command.app.out).Encode(value); err != nil {
		return failure("memory: "+err.Error(), err)
	}
	return nil
}

func (command *memoryAddCommand) Execute(args []string) error {
	if err := rejectArgs(args); err != nil {
		return err
	}
	service, ctx, stop, err := command.memoryOptions.service(true)
	if err != nil {
		return err
	}
	defer stop()
	if command.ID == "" {
		command.ID, err = session.NewID()
	}
	if err != nil {
		return failure("memory: "+err.Error(), err)
	}
	value, err := service.Add(ctx, command.Scope, command.Text, command.ID)
	return writeMemoryResult(command.memoryOptions, value, err)
}

func (command *memoryShowCommand) Execute(args []string) error {
	if err := rejectArgs(args); err != nil {
		return err
	}
	service, _, stop, err := command.memoryOptions.service(false)
	if err != nil {
		return err
	}
	defer stop()
	value, err := service.Show(command.Scope)
	return writeMemoryResult(command.memoryOptions, value, err)
}

func (command *memoryCompactCommand) Execute(args []string) error {
	if err := rejectArgs(args); err != nil {
		return err
	}
	service, ctx, stop, err := command.memoryOptions.service(true)
	if err != nil {
		return err
	}
	defer stop()
	value, err := service.Compact(ctx, command.Scope, true)
	return writeMemoryResult(command.memoryOptions, value, err)
}

func (command *memoryClearCommand) Execute(args []string) error {
	if err := rejectArgs(args); err != nil {
		return err
	}
	service, ctx, stop, err := command.memoryOptions.service(true)
	if err != nil {
		return err
	}
	defer stop()
	value, err := service.Clear(ctx, command.Scope)
	return writeMemoryResult(command.memoryOptions, value, err)
}

func (command *memoryWatchCommand) Execute(args []string) error {
	if err := rejectArgs(args); err != nil {
		return err
	}
	period, err := time.ParseDuration(command.Period)
	if err != nil {
		return usage("memory: invalid --period", err)
	}
	options := memoryOptions{app: command.app, global: command.global, Workspace: command.Workspace}
	service, ctx, stop, err := options.service(true)
	if err != nil {
		return err
	}
	defer stop()
	if err := service.Watch(ctx, period, command.app.errOut); err != nil {
		if ctx.Err() != nil {
			return &Error{Code: ExitInterrupted, Message: "memory: cancelled", Cause: ctx.Err()}
		}
		return failure("memory: "+err.Error(), err)
	}
	return nil
}

func (a *App) addMemoryCommands(parser *flags.Parser, global *globalOptions) error {
	parent, err := parser.AddCommand("memory", "Manage persistent memory", "Read, add, compact, clear, or watch built-in persistent memory.", &memoryCommand{})
	if err != nil {
		return err
	}
	commands := []struct {
		name, short string
		data        any
	}{
		{"add", "Add a memory note", &memoryAddCommand{memoryOptions: memoryOptions{app: a, global: global}}},
		{"show", "Show a memory scope", &memoryShowCommand{memoryOptions{app: a, global: global}}},
		{"compact", "Compact a memory scope", &memoryCompactCommand{memoryOptions{app: a, global: global}}},
		{"clear", "Clear a memory scope", &memoryClearCommand{memoryOptions{app: a, global: global}}},
		{"watch", "Continuously compact existing memory", &memoryWatchCommand{app: a, global: global}},
	}
	for _, item := range commands {
		if _, err := parent.AddCommand(item.name, item.short, item.short, item.data); err != nil {
			return fmt.Errorf("add memory command %s: %w", item.name, err)
		}
	}
	return nil
}
