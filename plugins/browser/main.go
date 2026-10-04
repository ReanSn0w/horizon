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

	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/plugins"
	flags "github.com/umputun/go-flags"
)

const usage = `Usage: horizon browser <list|close> [options]
  horizon browser list
  horizon browser close <id>
  horizon browser close --stale

Configure plugins.browser.api_key in config.yaml. Enable agent tools with
agent_plugins: [browser] and use resume --access full.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fail := func(code int, err error) int { fmt.Fprintf(stderr, "browser: %s\n", err); return code }
	if len(args) == 1 && args[0] == "horizon-plugin-metadata" {
		if err := json.NewEncoder(stdout).Encode(plugins.Metadata{ProtocolVersion: 1, AgentProtocolVersion: 1, ConfigProtocolVersion: 1, Version: "1.0.0", Description: "Remote Browser Use browser"}); err != nil {
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
	if len(args) == 0 || len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := io.WriteString(stdout, usage)
		if err != nil {
			return fail(1, err)
		}
		return 0
	}
	if len(args) == 1 && strings.HasPrefix(args[0], "horizon-plugin-agent-") {
		return protocol(ctx, strings.TrimPrefix(args[0], "horizon-plugin-agent-"), stdin, stdout, stderr)
	}
	if args[0] != "list" && args[0] != "close" {
		return fail(2, fmt.Errorf("unknown command %q", args[0]))
	}
	var opts struct {
		Stale bool `long:"stale" description:"Close inactive turns"`
	}
	parser := flags.NewParser(&opts, flags.HelpFlag|flags.PassDoubleDash)
	parser.Name = "horizon browser " + args[0]
	remaining, err := parser.ParseArgs(args[1:])
	if err != nil {
		var flagErr *flags.Error
		if errors.As(err, &flagErr) && flagErr.Type == flags.ErrHelp {
			_, _ = io.WriteString(stdout, usage)
			return 0
		}
		return fail(2, err)
	}
	return browserCLI(ctx, args[0], remaining, opts.Stale, stdout, stderr)
}

func configuration(home string) (settings, error) {
	cfg, err := config.Load(home)
	if err != nil {
		return settings{}, fmt.Errorf("cannot load config.yaml; check configuration format and values")
	}
	return loadSettings(cfg)
}
