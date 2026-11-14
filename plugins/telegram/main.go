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
	if len(args) == 0 || !strings.HasPrefix(args[0], "horizon-plugin-") {
		return cli(ctx, args, out, diag)
	}
	enc := json.NewEncoder(out)
	switch args[0] {
	case "horizon-plugin-metadata":
		return encode(enc, plugins.Metadata{ProtocolVersion: 1, ConfigProtocolVersion: 1, AgentProtocolVersion: plugins.ChannelProtocolVersion, DefaultCommunication: true, Version: buildinfo.Version, Description: "Explicit Telegram message delivery"})
	case "horizon-plugin-config-template":
		return encode(enc, map[string]any{"config_protocol_version": 1, "section": telegram.BotSettings{}})
	}
	op, agent := strings.CutPrefix(args[0], "horizon-plugin-agent-")
	if agent && len(args) == 1 {
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
		return protocol(ctx, op, home, in, out)
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
