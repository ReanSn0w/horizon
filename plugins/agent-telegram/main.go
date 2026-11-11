package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/ReanSn0w/horizon/internal/buildinfo"
	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/daemon"
	"github.com/ReanSn0w/horizon/internal/telegram"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 1 {
		switch args[0] {
		case "agent-plugin-metadata":
			if json.NewEncoder(os.Stdout).Encode(daemon.AdapterMetadata{ProtocolVersion: 1, Version: buildinfo.Version, Description: "Telegram input adapter", Capabilities: []string{}, ConfigProtocolVersion: 1}) == nil {
				return 0
			}
			return 1
		case "agent-plugin-config-template":
			home, err := config.ResolveHome("")
			if err == nil && json.NewEncoder(os.Stdout).Encode(map[string]any{"config_protocol_version": 1, "section": telegram.DefaultAdapter(home)}) == nil {
				return 0
			}
			return 1
		}
	}
	fmt.Fprintln(os.Stderr, "agent-telegram is supervised by horizon daemon")
	return 2
}
