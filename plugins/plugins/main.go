package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/ReanSn0w/horizon/internal/buildinfo"
	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/distribution"
	catalog "github.com/ReanSn0w/horizon/plugins"
	flags "github.com/umputun/go-flags"
	"gopkg.in/yaml.v3"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, distribution.NewClient()))
}

const usage = "Usage: horizon plugins list | install <name> | remove <name>\nUses the installed Horizon release. Installation does not enable agent plugins."

func run(ctx context.Context, args []string, out, diag io.Writer, client *distribution.Client) int {
	fail := func(err error) int {
		fmt.Fprintln(diag, "plugins:", err)
		if ctx.Err() != nil {
			return 130
		}
		return 1
	}
	if len(args) == 1 && args[0] == "horizon-plugin-metadata" {
		if err := json.NewEncoder(out).Encode(map[string]any{"protocol_version": 1, "version": buildinfo.Version, "description": catalog.Description("plugins")}); err != nil {
			return fail(err)
		}
		return 0
	}
	var options struct{}
	p := flags.NewNamedParser("horizon plugins", flags.HelpFlag|flags.PassDoubleDash)
	p.AddGroup("Options", "", &options)
	remaining, err := p.ParseArgs(args)
	if err != nil {
		var e *flags.Error
		if errors.As(err, &e) && e.Type == flags.ErrHelp {
			fmt.Fprintln(out, usage)
			return 0
		}
		fmt.Fprintln(diag, "plugins:", err)
		return 2
	}
	if len(remaining) == 0 || (remaining[0] != "list" && remaining[0] != "install" && remaining[0] != "remove") || (remaining[0] == "list" && len(remaining) != 1) || (remaining[0] != "list" && len(remaining) != 2) {
		fmt.Fprintln(diag, usage)
		return 2
	}
	home, err := config.ResolveHome("")
	if err != nil {
		return fail(err)
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		return fail(err)
	}
	executable := os.Getenv("HORIZON_EXECUTABLE")
	if executable == "" {
		return fail(fmt.Errorf("run through horizon plugins, with HORIZON_EXECUTABLE set by the host"))
	}
	if remaining[0] == "list" {
		state, err := distribution.LoadState(home)
		if err != nil {
			return fail(err)
		}
		if err = state.CheckRunning(executable, buildinfo.Version); err != nil {
			return fail(err)
		}
		manifest, _, err := client.Manifest(ctx, state.Manifest.Tag)
		if err != nil {
			if ctx.Err() != nil {
				return fail(err)
			}
			fmt.Fprintf(diag, "Using saved catalog for %s (GitHub unavailable: %v).\n", state.Manifest.Tag, err)
			manifest = state.Manifest
		} else if !distribution.SameManifest(manifest, state.Manifest) {
			return fail(fmt.Errorf("published manifest changed for installed tag"))
		}
		writer := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintf(writer, "Release: %s\nNAME\tSTATE\tREQUIRES\tDESCRIPTION\n", manifest.Tag)
		for _, plugin := range manifest.Catalog.Plugins {
			status := "available"
			if slices.Contains(state.Installed, plugin.ID) {
				status = "installed"
				if !state.Healthy(plugin.Binary) {
					status = "missing/modified"
				}
			} else if _, err := os.Lstat(filepath.Join(home, "plugins", plugin.Binary)); err == nil {
				status = "unmanaged"
			}
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", plugin.ID, status, strings.Join(plugin.Requires, ","), plugin.Description)
		}
		if err := writer.Flush(); err != nil {
			return fail(err)
		}
		return 0
	}
	manager, err := distribution.OpenManager(home, executable)
	if err != nil {
		return fail(err)
	}
	defer manager.Close()
	state, err := distribution.LoadState(manager.Home)
	if err != nil {
		return fail(err)
	}
	if err = state.CheckRunning(executable, buildinfo.Version); err != nil {
		return fail(err)
	}
	id := remaining[1]
	if remaining[0] == "remove" {
		active, err := activePlugin(home, id)
		if err != nil {
			return fail(err)
		}
		if active {
			return fail(fmt.Errorf("disable %s in agent_plugins before removing it", id))
		}
		if _, err = manager.Remove(ctx, id); err != nil {
			return fail(err)
		}
		fmt.Fprintf(out, "Plugin %s removed (or already absent); dependencies and user data retained.\n", id)
		return 0
	}
	selected, err := state.Manifest.Catalog.Closure(append(slices.Clone(state.Installed), id))
	if err != nil {
		return fail(err)
	}
	healthy := true
	for _, id := range selected {
		if !slices.Contains(state.Installed, id) || !state.Healthy("horizon-"+id) {
			healthy = false
		}
	}
	if healthy {
		fmt.Fprintf(out, "Plugin %s and its dependencies are already installed at %s.\n", id, state.Manifest.Tag)
		return 0
	}
	bundle, err := client.Bundle(ctx, state.Manifest.Tag, state.Platform)
	if err != nil {
		return fail(err)
	}
	defer bundle.Close()
	if !distribution.SameManifest(bundle.Manifest, state.Manifest) {
		return fail(fmt.Errorf("published manifest changed for installed tag"))
	}
	if _, err = manager.Install(ctx, bundle, selected, state.Platform, false); err != nil {
		return fail(err)
	}
	fmt.Fprintf(out, "Installed %s at %s. Run horizon --home %q init to add missing settings.\n", strings.Join(selected, ", "), state.Manifest.Tag, home)
	return 0
}
func activePlugin(home, id string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(home, "config.yaml"))
	if err != nil {
		return false, err
	}
	var config struct {
		AgentPlugins []string `yaml:"agent_plugins"`
	}
	if err = yaml.Unmarshal(data, &config); err != nil {
		return false, fmt.Errorf("read agent_plugins: %w", err)
	}
	return slices.Contains(config.AgentPlugins, id), nil
}
