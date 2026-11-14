package main

import (
	"context"
	"encoding/json"
	"errors"
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
	flags "github.com/umputun/go-flags"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fail := func(code int, err error) int { fmt.Fprintf(stderr, "browser: %s\n", err); return code }
	if len(args) == 1 && args[0] == "horizon-plugin-metadata" {
		if err := json.NewEncoder(stdout).Encode(plugins.Metadata{ProtocolVersion: 1, AgentProtocolVersion: 1, ConfigProtocolVersion: 1, Version: buildinfo.Version, Description: "Remote Browser Use browser"}); err != nil {
			return fail(1, err)
		}
		return 0
	}
	if len(args) == 1 && args[0] == "horizon-plugin-config-template" {
		section, err := browserConfigTemplate()
		if err != nil {
			return fail(1, err)
		}
		if err := json.NewEncoder(stdout).Encode(map[string]any{"config_protocol_version": 1, "section": section}); err != nil {
			return fail(1, err)
		}
		return 0
	}
	if len(args) == 0 {
		args = []string{"--help"}
	}
	var closeOptions struct {
		Stale bool `long:"stale" description:"Close browsers left by inactive turns"`
		Args  struct {
			ID string `positional-arg-name:"ID"`
		} `positional-args:"yes"`
	}
	parser := flags.NewParser(&struct{}{}, flags.HelpFlag|flags.PassDoubleDash)
	parser.Name = "horizon browser"
	parser.LongDescription = "Configure plugins.browser.api_key. Enable model tools with extensions.browser.enabled: true and resume --access full."
	parser.AddCommand("list", "List browsers", "List active and orphaned browser sessions.", &struct{}{})
	parser.AddCommand("close", "Close browsers", "Specify a browser ID or --stale.", &closeOptions)
	protocolCall := len(args) == 1 && strings.HasPrefix(args[0], "horizon-plugin-agent-")
	if !protocolCall {
		remaining, err := parser.ParseArgs(args)
		if err != nil {
			var flagErr *flags.Error
			if errors.As(err, &flagErr) && flagErr.Type == flags.ErrHelp {
				fmt.Fprint(stdout, flagErr.Message)
				return 0
			}
			return fail(2, err)
		}
		if len(remaining) > 0 {
			return fail(2, fmt.Errorf("unexpected arguments"))
		}
	}
	home, err := config.ResolveHome("")
	if err != nil {
		return fail(2, err)
	}
	release, err := maintenance.Acquire(home, false)
	if err != nil {
		return fail(1, err)
	}
	defer release()
	if len(args) == 1 && strings.HasPrefix(args[0], "horizon-plugin-agent-") {
		return protocol(ctx, strings.TrimPrefix(args[0], "horizon-plugin-agent-"), stdin, stdout, stderr)
	}
	var positional []string
	if closeOptions.Args.ID != "" {
		positional = []string{closeOptions.Args.ID}
	}
	return browserCLI(ctx, parser.Active.Name, positional, closeOptions.Stale, stdout, stderr)
}

func configuration(home string) (settings, error) {
	cfg, err := config.Load(home)
	if err != nil {
		return settings{}, fmt.Errorf("cannot load config.yaml; check configuration format and values")
	}
	return loadSettings(cfg)
}
