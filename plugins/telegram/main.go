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

	"github.com/ReanSn0w/horizon/internal/config"
	flags "github.com/umputun/go-flags"
)

type app struct {
	telegram    *telegram
	runner      runProcess
	ctx         context.Context
	home        string
	out, errOut io.Writer
	dispatch    func(string, any) error
}
type command struct {
	app  *app
	name string
}

func (c *command) call(value any, args []string) error {
	if len(args) != 0 {
		return &cliError{2, errors.New("unexpected arguments")}
	}
	return c.app.dispatch(c.name, value)
}

type startCommand struct {
	command
	LogFile string `long:"log-file" description:"Write diagnostics to a rotating private log file"`
}

func (c *startCommand) Execute(args []string) error { return c.call(c, args) }

type listCommand struct {
	command
	JSON bool `long:"json" description:"Print one JSON object with schema_version and chats"`
}

func (c *listCommand) Execute(args []string) error { return c.call(c, args) }

type sendCommand struct {
	command
	Chat    int64  `long:"chat" required:"true" description:"Decimal chat ID from telegram list"`
	Message string `short:"m" long:"message" description:"Instruction for Horizon; default: continue the conversation"`
	Thread  int64  `long:"thread" description:"Telegram forum topic ID"`
	Wait    bool   `long:"wait" description:"Wait for delivery; interrupting the wait leaves the accepted job running"`
	JSON    bool   `long:"json" description:"Print request ID and status as JSON"`
}

func (c *sendCommand) Execute(args []string) error { return c.call(c, args) }

type serviceCommand struct {
	command
	Manager string `long:"manager" choice:"launchd" choice:"systemd" description:"User service manager (defaults to the current platform)"`
	Args    struct {
		Action string `positional-arg-name:"action" required:"true" description:"install, uninstall, start, stop, restart or status"`
	} `positional-args:"yes"`
}

func (c *serviceCommand) Execute(args []string) error { return c.call(c, args) }

type cliError struct {
	code int
	err  error
}

func (e *cliError) Error() string { return e.err.Error() }
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := runCLI(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
func runCLI(ctx context.Context, args []string, out, errOut io.Writer) int {
	if len(args) == 1 && args[0] == "horizon-plugin-metadata" {
		if json.NewEncoder(out).Encode(map[string]any{"protocol_version": 1, "config_protocol_version": 1, "version": "1.0.0", "description": "Telegram integration for Horizon"}) != nil {
			return 1
		}
		return 0
	}
	if len(args) == 1 && args[0] == "horizon-plugin-config-template" {
		home, err := config.ResolveHome("")
		if err != nil {
			fmt.Fprintln(errOut, "telegram: cannot resolve home")
			return 2
		}
		section, err := telegramConfigTemplate(home)
		if err != nil {
			fmt.Fprintln(errOut, "telegram: cannot prepare config template")
			return 1
		}
		if err := json.NewEncoder(out).Encode(map[string]any{"config_protocol_version": 1, "section": section, "legacy_names": []string{"gateway"}}); err != nil {
			return 1
		}
		return 0
	}
	a := &app{ctx: ctx, out: out, errOut: errOut}
	a.dispatch = func(name string, value any) error { return execute(a, name, value) }
	parser := flags.NewNamedParser("horizon telegram", flags.HelpFlag|flags.PassDoubleDash)
	parser.LongDescription = telegramHelp
	commands := []struct {
		name, short, description string
		value                    any
	}{
		{"start", "Run the Telegram integration", "Run in the foreground with long polling. Only the owner is accepted in private chats.", &startCommand{command: command{a, "start"}}},
		{"list", "List known chats", "Print CHAT ID beside human-readable NAME. JSON output is suitable for other plugins.", &listCommand{command: command{a, "list"}}},
		{"send", "Generate and send a new message", "Enqueue a Horizon turn using the chat's history. Requires a running telegram. The message option is an instruction, not literal Telegram text.", &sendCommand{command: command{a, "send"}}},
		{"service", "Manage a user service", "Manage a launchd LaunchAgent or systemd --user service. Install enables future autostart; start runs it now.", &serviceCommand{command: command{a, "service"}}},
	}
	for _, c := range commands {
		if _, err := parser.AddCommand(c.name, c.short, c.description, c.value); err != nil {
			fmt.Fprintln(errOut, err)
			return 2
		}
	}
	if len(args) == 0 {
		writeHelp(parser, out)
		return 0
	}
	if _, err := parser.ParseArgs(args); err != nil {
		var flagErr *flags.Error
		if errors.As(err, &flagErr) && flagErr.Type == flags.ErrHelp {
			writeHelp(parser, out)
			return 0
		}
		if ctx.Err() != nil {
			fmt.Fprintln(errOut, "telegram: cancelled")
			return 130
		}
		code := 1
		var ce *cliError
		if errors.As(err, &ce) {
			code = ce.code
		} else if errors.As(err, &flagErr) {
			code = 2
		}
		fmt.Fprintln(a.errOut, "telegram:", err)
		return code
	}
	return 0
}

func writeHelp(parser *flags.Parser, out io.Writer) {
	parser.WriteHelp(out)
	if parser.Active == nil || parser.Active.Name == "start" {
		fmt.Fprint(out, telegramConfigHelp)
	}
}

func (a *app) resolveHome() error { home, err := config.ResolveHome(""); a.home = home; return err }
