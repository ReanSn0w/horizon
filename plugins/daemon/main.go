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
	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/daemon"
	"github.com/ReanSn0w/horizon/internal/plugins"
	"github.com/ReanSn0w/horizon/internal/session"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
func run(ctx context.Context, args []string, out, diag io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(diag, "usage: horizon daemon start|status|stop")
		return 2
	}
	switch args[0] {
	case "horizon-plugin-metadata":
		json.NewEncoder(out).Encode(plugins.Metadata{ProtocolVersion: 1, ConfigProtocolVersion: 1, Version: buildinfo.Version, Description: "Local Horizon process supervisor"})
		return 0
	case "horizon-plugin-config-template":
		json.NewEncoder(out).Encode(map[string]any{"config_protocol_version": 1, "section": map[string]any{"parallel_per_depth": 2, "max_depth": 4, "max_children": 16, "max_queue": 128, "adapters": []string{}}})
		return 0
	case "--help", "-h":
		fmt.Fprintln(out, "horizon daemon start|status|stop\nStart runs in foreground. Stop before manager updates. Configure plugins.daemon.")
		return 0
	}
	home, err := config.ResolveHome("")
	if err == nil {
		home, err = session.NormalizeWorkspace(home)
	}
	if err == nil {
		switch args[0] {
		case "start":
			err = start(ctx, home)
		case "status", "stop":
			method := "hello"
			if args[0] == "stop" {
				method = "stop"
			}
			var response daemon.Response
			response, err = daemon.Call(ctx, home, daemon.Request{ID: args[0], Method: method})
			if err == nil {
				_, err = fmt.Fprintln(out, string(response.Data))
			}
		default:
			fmt.Fprintln(diag, "unknown daemon command")
			return 2
		}
	}
	if err != nil {
		fmt.Fprintln(diag, "daemon:", err)
		return 1
	}
	return 0
}
func start(ctx context.Context, home string) error { return daemon.Start(ctx, home) }
