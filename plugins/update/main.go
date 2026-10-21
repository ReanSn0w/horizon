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
	"runtime"
	"strings"

	"github.com/ReanSn0w/horizon/internal/buildinfo"
	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/distribution"
	catalog "github.com/ReanSn0w/horizon/plugins"
	flags "github.com/umputun/go-flags"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, distribution.NewClient()))
}
func parse(args []string, options any, name string, out, diag io.Writer) (int, error) {
	p := flags.NewNamedParser(name, flags.HelpFlag|flags.PassDoubleDash)
	if _, err := p.AddGroup("Options", "", options); err != nil {
		return 1, err
	}
	rest, err := p.ParseArgs(args)
	if err != nil {
		var e *flags.Error
		if errors.As(err, &e) && e.Type == flags.ErrHelp {
			fmt.Fprintln(out, e.Message)
			return 0, err
		}
		return 2, err
	}
	if len(rest) != 0 {
		return 2, fmt.Errorf("unexpected arguments: %s", strings.Join(rest, " "))
	}
	return 0, nil
}
func run(ctx context.Context, args []string, out, diag io.Writer, client *distribution.Client) int {
	fail := func(err error) int {
		fmt.Fprintln(diag, "update:", err)
		if ctx.Err() != nil {
			return 130
		}
		return 1
	}
	if len(args) == 1 && args[0] == "horizon-plugin-metadata" {
		if err := json.NewEncoder(out).Encode(map[string]any{"protocol_version": 1, "version": buildinfo.Version, "description": catalog.Description("update")}); err != nil {
			return fail(err)
		}
		return 0
	}
	if len(args) > 0 && args[0] == "bootstrap" {
		return bootstrap(ctx, args[1:], out, diag)
	}
	var options struct {
		Version string `long:"version" description:"exact release tag (default: latest stable)"`
	}
	code, err := parse(args, &options, "horizon update", out, diag)
	if err != nil {
		if code != 0 {
			fmt.Fprintln(diag, "update:", err)
		}
		return code
	}
	home, err := config.ResolveHome("")
	if err != nil {
		return fail(err)
	}
	executable := os.Getenv("HORIZON_EXECUTABLE")
	if executable == "" {
		return fail(fmt.Errorf("run through horizon update, with HORIZON_EXECUTABLE set by the host"))
	}
	m, err := distribution.OpenManager(home, executable)
	if err != nil {
		return fail(err)
	}
	defer m.Close()
	state, err := distribution.LoadState(m.Home)
	if err != nil {
		return fail(err)
	}
	if err = state.CheckRunning(executable, buildinfo.Version); err != nil {
		return fail(err)
	}
	tag, err := client.Resolve(ctx, options.Version)
	if err != nil {
		return fail(err)
	}
	if err = distribution.NoDowngrade(state.Manifest.Tag, tag); err != nil {
		return fail(err)
	}
	fmt.Fprintf(out, "Update %s -> %s: horizon, %s\n", state.Manifest.Tag, tag, strings.Join(state.Installed, ", "))
	if tag == state.Manifest.Tag {
		healthy := state.Healthy("horizon")
		for _, binary := range state.Binaries() {
			healthy = healthy && state.Healthy(binary)
		}
		if healthy {
			fmt.Fprintln(out, "The complete installation is already up to date.")
			return 0
		}
	}
	bundle, err := client.Bundle(ctx, tag, state.Platform)
	if err != nil {
		return fail(err)
	}
	defer bundle.Close()
	selected, err := bundle.Manifest.Catalog.Closure(append(state.Installed, bundle.Manifest.Catalog.Required()...))
	if err != nil {
		return fail(fmt.Errorf("target release cannot preserve installed plugins: %w", err))
	}
	fmt.Fprintf(out, "Target components: horizon, %s\n", strings.Join(selected, ", "))
	if _, err = m.Install(ctx, bundle, selected, state.Platform, false); err != nil {
		return fail(err)
	}
	fmt.Fprintf(out, "Installed %s. Run horizon --home %q init. Restart any services you stopped before updating.\n", tag, m.Home)
	return 0
}
func bootstrap(ctx context.Context, args []string, out, diag io.Writer) int {
	fail := func(err error) int {
		fmt.Fprintln(diag, "bootstrap:", err)
		if ctx.Err() != nil {
			return 130
		}
		return 1
	}
	var options struct {
		Bundle string `long:"bundle" required:"true" description:"verified extracted release directory"`
		Home   string `long:"home" required:"true" description:"installation home"`
		BinDir string `long:"bin-dir" required:"true" description:"directory for horizon"`
		Adopt  bool   `long:"adopt" description:"explicitly replace existing official binaries"`
	}
	code, err := parse(args, &options, "horizon-update bootstrap (installer only)", out, diag)
	if err != nil {
		if code != 0 {
			fmt.Fprintln(diag, "bootstrap:", err)
		}
		return code
	}
	b, err := distribution.ReadBundle(options.Bundle)
	if err != nil {
		return fail(err)
	}
	if b.Manifest.Tag != buildinfo.Version || b.Manifest.Commit != buildinfo.Commit {
		return fail(fmt.Errorf("bootstrap executable does not match bundle release identity"))
	}
	m, err := distribution.OpenManager(options.Home, filepath.Join(options.BinDir, "horizon"))
	if err != nil {
		return fail(err)
	}
	defer m.Close()
	var ids []string
	old, err := distribution.LoadState(m.Home)
	if err == nil {
		ids = old.Installed
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	if options.Adopt && err != nil {
		for _, p := range b.Manifest.Catalog.Plugins {
			if _, err := os.Lstat(filepath.Join(m.Home, "plugins", p.Binary)); err == nil {
				fmt.Fprintf(out, "Adopt official plugin: %s\n", p.ID)
			}
		}
		if _, err := os.Lstat(m.Executable); err == nil {
			fmt.Fprintf(out, "Adopt Horizon: %s\n", m.Executable)
		}
	}
	fmt.Fprintf(out, "Installing %s into %s (home: %s)\n", b.Manifest.Tag, m.Executable, m.Home)
	state, err := m.Install(ctx, b, ids, runtime.GOOS+"_"+runtime.GOARCH, options.Adopt)
	if err != nil {
		return fail(err)
	}
	fmt.Fprintf(out, "Installed %s: horizon, %s\nNext: %s --home %q init\n", state.Manifest.Tag, strings.Join(state.Installed, ", "), state.Executable, state.Home)
	return 0
}
