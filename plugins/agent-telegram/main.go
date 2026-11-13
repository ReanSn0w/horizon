package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/ReanSn0w/horizon/internal/buildinfo"
	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/daemon"
	"github.com/ReanSn0w/horizon/internal/telegram"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:]))
}

func run(ctx context.Context, args []string) int {
	if len(args) == 1 {
		switch args[0] {
		case "agent-plugin-metadata":
			if json.NewEncoder(os.Stdout).Encode(daemon.AdapterMetadata{ProtocolVersion: 1, Version: buildinfo.Version, Description: "Telegram input adapter", Capabilities: []string{daemon.ActiveInputCapability, daemon.AdapterValidate}, ConfigProtocolVersion: 1}) == nil {
				return 0
			}
			return 1
		case "agent-run":
			if err := start(ctx); err != nil && ctx.Err() == nil {
				fmt.Fprintln(os.Stderr, err)
				if home, e := config.ResolveHome(""); e == nil {
					_ = telegram.WritePrivate(filepath.Join(home, "gateway", "telegram-adapter.error"), []byte(err.Error()+"\n"), 0600)
				}
				return 1
			}
			return 0
		case "agent-plugin-validate":
			home, err := config.ResolveHome("")
			if err == nil {
				_, _, err = validateSettings(home)
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			return 0
		case "agent-plugin-config-template":
			home, err := config.ResolveHome("")
			if err == nil && json.NewEncoder(os.Stdout).Encode(map[string]any{"config_protocol_version": 1, "section": telegram.DefaultAdapter(home)}) == nil {
				return 0
			}
			return 1
		}
	}
	fmt.Fprintln(os.Stderr, "agent-telegram is supervised by horizon agent")
	return 2
}
