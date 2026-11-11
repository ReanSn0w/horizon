package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ReanSn0w/horizon/internal/buildinfo"
	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/maintenance"
	"github.com/ReanSn0w/horizon/internal/plugins"
	"github.com/ReanSn0w/horizon/internal/telegram"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, in io.Reader, out, diag io.Writer) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(out, "Usage: horizon telegram <list|read|send|migrate>\nRun Telegram polling through horizon daemon start.")
		return 0
	}
	enc := json.NewEncoder(out)
	switch args[0] {
	case "horizon-plugin-metadata":
		return encode(enc, plugins.Metadata{ProtocolVersion: 1, ConfigProtocolVersion: 1, AgentProtocolVersion: plugins.ChannelProtocolVersion, Version: buildinfo.Version, Description: "Explicit Telegram message delivery"})
	case "horizon-plugin-config-template":
		return encode(enc, map[string]any{"config_protocol_version": 1, "section": telegram.BotSettings{}})
	case "start", "service":
		fmt.Fprintln(diag, "Telegram polling now runs under horizon daemon. Use horizon telegram migrate --check for an existing installation.")
		return 2
	}
	if args[0] == "migrate" {
		if len(args) != 2 {
			fmt.Fprintln(diag, "Usage: horizon telegram migrate --check|--apply|--rollback")
			return 2
		}
		home, err := config.ResolveHome("")
		if err != nil {
			fmt.Fprintln(diag, "telegram: invalid home")
			return 2
		}
		report, err := telegram.Migrate(ctx, home, args[1])
		if err != nil {
			fmt.Fprintln(diag, "telegram:", err)
			return 1
		}
		fmt.Fprintf(out, "Migration %s. Chats %d, queued %d, unknown %d, archived %d.\nBackup: %s\n", report.State, report.Chats, report.Queued, report.Unknown, report.Archived, report.Backup)
		if report.LegacyService {
			fmt.Fprintln(out, "Legacy service registration found. Disable its auto-start before running horizon daemon start.")
		}
		return 0
	}
	op, agent := strings.CutPrefix(args[0], "horizon-plugin-agent-")
	if agent || args[0] == "list" || args[0] == "read" {
		home, err := config.ResolveHome("")
		if err != nil {
			fmt.Fprintln(diag, "telegram: invalid home")
			return 2
		}
		release, err := maintenance.Acquire(home, false)
		if err != nil {
			fmt.Fprintln(diag, err)
			return 1
		}
		defer release()
		if agent && len(args) == 1 {
			return protocol(ctx, op, home, in, out)
		}
		if !agent {
			if err := inspect(home, args, out); err != nil {
				fmt.Fprintln(diag, "telegram:", err)
				return 1
			}
			return 0
		}
	}
	if args[0] == "send" {
		fmt.Fprintln(diag, "telegram send --message has been retired. Enable the telegram agent extension to use telegram__send_message in a Horizon turn.")
		return 2
	}
	fmt.Fprintln(diag, "telegram: unknown command")
	return 2
}

func encode(enc *json.Encoder, value any) int {
	if enc.Encode(value) != nil {
		return 1
	}
	return 0
}
