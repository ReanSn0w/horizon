package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/maintenance"
	"github.com/ReanSn0w/horizon/internal/telegram"
	flags "github.com/umputun/go-flags"
)

func cli(ctx context.Context, args []string, out, diag io.Writer) int {
	var list struct {
		JSON bool `long:"json" description:"Print JSON"`
	}
	var read struct {
		ID string `long:"operation-id" required:"yes" description:"Delivery operation ID" value-name:"ID"`
	}
	var send struct {
		Args struct {
			Text string `positional-arg-name:"TEXT" required:"yes"`
		} `positional-args:"yes"`
	}
	var migrate struct {
		Check    bool `long:"check" description:"Inspect legacy installation without changing it"`
		Apply    bool `long:"apply" description:"Migrate the stopped legacy installation"`
		Rollback bool `long:"rollback" description:"Restore migration backup"`
	}
	parser := flags.NewParser(&struct{}{}, flags.HelpFlag|flags.PassDoubleDash)
	parser.Name = "horizon telegram"
	parser.LongDescription = "Inspect Telegram delivery, check connectivity or explicitly send a test message to the configured owner. Polling runs through horizon agent start."
	parser.AddCommand("list", "List chats", "List known Telegram chats.", &list)
	parser.AddCommand("read", "Read a delivery", "Read a delivery journal entry.", &read)
	parser.AddCommand("check", "Check Telegram connection", "Validate bot identity and polling readiness without a model request or sending a message.", &struct{}{})
	parser.AddCommand("send", "Send to the owner", "Send TEXT to the configured owner without a model request. This explicit diagnostic does not require agent_plugins; there are no automatic retries.", &send)
	parser.AddCommand("migrate", "Migrate legacy Telegram", "Stop the old polling process and supervisor before applying migration.", &migrate)
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
	if err != nil {
		fmt.Fprintln(diag, "telegram: invalid home")
		return 2
	}
	if command == "migrate" {
		modes := []string{}
		if migrate.Check {
			modes = append(modes, "--check")
		}
		if migrate.Apply {
			modes = append(modes, "--apply")
		}
		if migrate.Rollback {
			modes = append(modes, "--rollback")
		}
		if len(modes) != 1 {
			fmt.Fprintln(diag, "choose exactly one of --check, --apply or --rollback")
			return 2
		}
		report, e := telegram.Migrate(ctx, home, modes[0])
		if e != nil {
			fmt.Fprintln(diag, "telegram:", e)
			return 1
		}
		fmt.Fprintf(out, "Migration %s. Chats %d, queued %d, unknown %d, archived %d.\nBackup: %s\n", report.State, report.Chats, report.Queued, report.Unknown, report.Archived, report.Backup)
		if report.LegacyService {
			fmt.Fprintln(out, "Disable legacy service auto-start before running horizon agent start.")
		}
		return 0
	}
	if command == "send" && (strings.TrimSpace(send.Args.Text) == "" || utf8.RuneCountInString(send.Args.Text) > 4096) {
		fmt.Fprintln(diag, "message must contain 1..4096 characters")
		return 2
	}
	if command == "send" {
		switch os.Getenv("HORIZON_INHERITED_ACCESS") {
		case "", "full":
		default:
			fmt.Fprintln(diag, "direct Telegram sending requires full access")
			return 2
		}
	}
	release, err := maintenance.Acquire(home, false)
	if err != nil {
		fmt.Fprintln(diag, err)
		return 1
	}
	defer release()
	switch command {
	case "list":
		argv := []string{"list"}
		if list.JSON {
			argv = append(argv, "--json")
		}
		err = inspect(home, argv, out)
	case "read":
		err = inspect(home, []string{"read", "--operation-id", read.ID}, out)
	default:
		err = diagnose(ctx, home, command, send.Args.Text, out)
	}
	if err != nil {
		fmt.Fprintln(diag, "telegram:", err)
		return 1
	}
	return 0
}

func diagnose(ctx context.Context, home, command, message string, out io.Writer) error {
	cfg, err := config.Load(home)
	if err != nil {
		return errors.New("cannot load Horizon configuration")
	}
	bot, err := telegram.ReadBot(cfg)
	if err != nil {
		return err
	}
	client := telegram.New(bot.Token)
	identity, err := client.Identity(ctx)
	if err != nil {
		return err
	}
	if identity.ID != bot.BotID() {
		return errors.New("bot identity does not match configured token")
	}
	if command == "check" {
		if err = client.CheckPolling(ctx); err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "Connected as @%s (%d); polling is available. Owner: %d.\n", identity.Username, identity.ID, bot.Owner)
		return err
	}
	id, err := client.Send(ctx, bot.Owner, message)
	if err != nil {
		return fmt.Errorf("test delivery was not confirmed; inspect Telegram before retrying: %w", err)
	}
	_, err = fmt.Fprintf(out, "Sent message %d to owner %d.\n", id, bot.Owner)
	return err
}
