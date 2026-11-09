package manager

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ReanSn0w/horizon/internal/bootstrap"
	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/maintenance"
	"github.com/ReanSn0w/horizon/internal/plugins"
	"golang.org/x/term"
)

func Execute(ctx context.Context, o Options, in io.Reader, out io.Writer) error {
	return engine().execute(ctx, o, in, out)
}
func interactive(in io.Reader) bool {
	f, ok := in.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}
func chooseHome(o *Options, in io.Reader, out io.Writer) error {
	if o.Yes {
		return nil
	}
	if !interactive(in) {
		return errors.New("non-interactive installation requires --yes")
	}
	if !o.ExplicitHome {
		fmt.Fprintf(out, "Horizon home [%s]: ", o.Home)
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil {
			return err
		}
		if strings.TrimSpace(line) != "" {
			o.Home, err = config.ResolveHome(strings.TrimSpace(line))
			if err != nil {
				return err
			}
		}
	}
	return nil
}
func (e *Engine) execute(ctx context.Context, o Options, in io.Reader, out io.Writer) error {
	if o.Command == "install" {
		if err := chooseHome(&o, in, out); err != nil {
			return err
		}
	}
	if o.Command != "install" {
		if _, err := os.Stat(filepath.Join(o.Home, ".manager")); err != nil {
			return fmt.Errorf("installation is not managed; run horizon-manager install")
		}
	}
	if o.Command == "install" {
		if _, err := os.Stat(filepath.Join(o.Home, ".manager")); os.IsNotExist(err) {
			if err = checkTools(ctx); err != nil {
				return err
			}
		}
	}
	if err := os.MkdirAll(filepath.Join(o.Home, ".manager"), 0700); err != nil {
		return err
	}
	for _, name := range []string{".manager", "bin"} {
		info, err := os.Lstat(filepath.Join(o.Home, name))
		if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return fmt.Errorf("%s must be a directory, not a symlink", name)
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	unlock, err := maintenance.Lock(filepath.Join(o.Home, ".manager", "operation.lock"), true)
	if err != nil {
		return err
	}
	defer unlock()
	release, err := maintenance.Acquire(o.Home, o.Command != "plugins list")
	if err != nil {
		return err
	}
	defer release()
	if o.Command == "plugins list" {
		if _, err := os.Stat(filepath.Join(transactionDir(o.Home), "journal.json")); err == nil {
			return errors.New("interrupted installation; run manager update or install to recover")
		}
	} else {
		if err = recoverTransaction(o.Home); err != nil {
			return err
		}
		if err = checkLegacyBusy(o.Home); err != nil {
			return err
		}
	}
	old, err := ReadState(o.Home)
	installed := err == nil
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if !installed && o.Command != "install" {
		return errors.New("installation state is missing; run horizon-manager install")
	}
	if o.Command == "plugins list" {
		return listPlugins(o.Home, old, out)
	}
	if o.Command != "plugins remove" {
		if err = checkTools(ctx); err != nil {
			return err
		}
	}
	expectedCommit := old.Commit
	if o.Command == "install" {
		if _, err := os.Stat(filepath.Join(o.Home, "source")); os.IsNotExist(err) {
			expectedCommit = ""
		}
	}
	if err = e.checkCheckout(ctx, filepath.Join(o.Home, "source"), expectedCommit); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Join(o.Home, ".manager"), "staging-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if o.Command == "plugins install" || o.Command == "plugins remove" {
		return e.changePlugin(ctx, o, old, stage, out)
	}
	source := config.Source{Type: "release"}
	if installed {
		settings, err := config.ReadInstallation(o.Home)
		if err != nil {
			return err
		}
		source = settings.Source
		if _, err := os.Stat(filepath.Join(o.Home, "config.yaml")); os.IsNotExist(err) {
			source = old.Source
		}
	}
	if o.Source != nil {
		source = *o.Source
	}
	commit, ref, catalog, err := e.prepare(ctx, stage, source)
	if err != nil {
		return err
	}
	names := append([]string(nil), old.Plugins...)
	for _, name := range o.Adopt {
		if _, ok := catalog[name]; !ok {
			return fmt.Errorf("unknown legacy plugin %s", name)
		}
		if _, err := os.Stat(filepath.Join(o.Home, "plugins", "horizon-"+name)); err != nil {
			return fmt.Errorf("legacy plugin %s not found", name)
		}
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, name := range append([]string{"horizon", "horizon-manager"}, binaryNames(names)...) {
		owned := installed && (name == "horizon" || name == "horizon-manager" || slices.Contains(binaryNames(old.Plugins), name))
		if !owned {
			if _, err := os.Lstat(filepath.Join(o.Home, "bin", name)); err == nil {
				return fmt.Errorf("unmanaged binary %s already exists; move it aside explicitly", name)
			} else if !os.IsNotExist(err) {
				return err
			}
		}
	}
	if err = e.buildSet(ctx, filepath.Join(stage, "source"), stage, catalog, names); err != nil {
		return err
	}
	if _, err = bootstrap.Ensure(o.Home); err != nil {
		return err
	}
	if err = prepareConfig(o.Home, stage, append([]string{"manager"}, names...)); err != nil {
		return err
	}
	if err = config.WriteInstallation(stage, config.Installation{Source: source}); err != nil {
		return err
	}
	state := State{Version: stateVersion, Commit: commit, Ref: ref, Source: source, Plugins: names}
	if err = writeJSON(filepath.Join(stage, ".manager", "state.json"), state); err != nil {
		return err
	}
	paths := append([]string{"source", "bin/horizon"}, pluginPaths(names)...)
	paths = append(paths, "config.yaml", "bin/horizon-manager", ".manager/state.json")
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = e.deploy(o.Home, stage, paths, nil); err != nil {
		return err
	}
	fmt.Fprintf(out, "Installed Horizon in %s\nSource: %s (%s)\nFill providers, models, default_model and decision_model in %s before running Horizon.\n", o.Home, ref, commit, filepath.Join(o.Home, "config.yaml"))
	fmt.Fprintf(out, "Global instructions: %s\n", filepath.Join(o.Home, "AGENTS.md"))
	if o.Command == "install" {
		if path, err := exec.LookPath("horizon"); err == nil {
			absolute, _ := filepath.Abs(path)
			if absolute != filepath.Join(o.Home, "bin", "horizon") {
				fmt.Fprintf(out, "Existing horizon in PATH: %s. It was preserved; check PATH order after setup.\n", path)
			}
		}
		if entries, err := os.ReadDir(filepath.Join(o.Home, "plugins")); err == nil && len(entries) > 0 {
			fmt.Fprintln(out, "Legacy plugins were preserved in home/plugins. Select known plugins with install --adopt NAME; stop and reinstall existing Telegram services for the new binary path.")
		}
		printShellSetup(o, out)
	}
	return nil
}
func binaryNames(names []string) []string {
	r := make([]string, len(names))
	for i, n := range names {
		r[i] = "horizon-" + n
	}
	return r
}
func pluginPaths(names []string) []string {
	r := binaryNames(names)
	for i := range r {
		r[i] = "bin/" + r[i]
	}
	return r
}
func (e *Engine) buildSet(ctx context.Context, source, stage string, catalog map[string]Plugin, names []string) error {
	for _, name := range append([]string{"manager"}, names...) {
		if _, ok := catalog[name]; !ok {
			return fmt.Errorf("installed plugin %s is absent from selected source; remove it explicitly before updating", name)
		}
	}
	if err := os.MkdirAll(filepath.Join(stage, "bin"), 0700); err != nil {
		return err
	}
	if err := e.buildBinary(ctx, source, ".", filepath.Join(stage, "bin", "horizon")); err != nil {
		return err
	}
	for _, name := range append([]string{"manager"}, names...) {
		p := catalog[name]
		path := filepath.Join(stage, "bin", p.Binary)
		if err := e.buildBinary(ctx, source, p.Package, path); err != nil {
			return err
		}
		if entry := plugins.Inspect(stage, name, nil); entry.Err != nil {
			return fmt.Errorf("incompatible metadata for %s: %w", name, entry.Err)
		}

	}
	return nil
}
