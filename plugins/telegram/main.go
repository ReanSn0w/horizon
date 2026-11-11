package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/ReanSn0w/horizon/internal/buildinfo"
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
		return encode(enc, plugins.Metadata{ProtocolVersion: 1, ConfigProtocolVersion: 1, Version: buildinfo.Version, Description: "Explicit Telegram message delivery"})
	case "horizon-plugin-config-template":
		return encode(enc, map[string]any{"config_protocol_version": 1, "section": telegram.BotSettings{}})
	case "start", "service":
		fmt.Fprintln(diag, "Telegram polling now runs under horizon daemon. Use horizon telegram migrate --check for an existing installation.")
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
