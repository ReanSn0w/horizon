package manager

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/ReanSn0w/horizon/internal/bootstrap"
	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/maintenance"
	"github.com/ReanSn0w/horizon/internal/plugins"
	"gopkg.in/yaml.v3"
)

func checkLegacyBusy(home string) error {
	files, err := filepath.Glob(filepath.Join(home, "dialogs", "*", "*.lock"))
	if err != nil {
		return err
	}
	files = append(files, filepath.Join(home, "gateway", "process.lock"), filepath.Join(home, "run", "daemon.lock"))
	for _, path := range files {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		release, err := maintenance.Lock(path, true)
		if err != nil {
			return err
		}
		release()
	}
	return nil
}
func prepareConfig(home, stage, source string, names []string, fresh, agentSupervisor bool) error {
	if err := prepareCoreConfig(home, stage, source, fresh, agentSupervisor); err != nil {
		return err
	}
	var templates []config.PluginTemplate
	for _, name := range names {
		if strings.HasPrefix(name, "agent:") {
			template, e := adapterTemplate(stage, home, name)
			if e != nil {
				return e
			}
			templates = append(templates, template)
			continue
		}
		entry := plugins.Inspect(stage, name, nil)
		if entry.Err != nil {
			return fmt.Errorf("validate %s: %w", name, entry.Err)
		}
		if entry.Metadata.ConfigProtocolVersion == 0 {
			continue
		}
		if entry.Metadata.ConfigProtocolVersion != plugins.ConfigProtocolVersion {
			return fmt.Errorf("unsupported config protocol for %s", name)
		}
		template, err := plugins.ReadConfigTemplate(entry, home)
		if err != nil {
			return fmt.Errorf("cannot read config template for %s", name)
		}
		templates = append(templates, template)
	}
	if len(templates) > 0 {
		_, err := config.MergePluginTemplates(stage, templates)
		return err
	}
	return nil
}
func enabledPlugins(home string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(home, "config.yaml"))
	if err != nil {
		return nil, err
	}
	var raw struct {
		Names   []string `yaml:"agent_plugins"`
		Plugins struct {
			Agent struct {
				Adapters []string `yaml:"adapters"`
			} `yaml:"agent"`
			LegacyDaemon struct {
				Adapters []string `yaml:"adapters"`
			} `yaml:"daemon"`
		} `yaml:"plugins"`
	}
	if err = yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("cannot read agent_plugins from configuration")
	}
	for _, name := range append(raw.Plugins.Agent.Adapters, raw.Plugins.LegacyDaemon.Adapters...) {
		raw.Names = append(raw.Names, "agent:"+name)
	}
	return raw.Names, nil
}
func listPlugins(home string, state State, out io.Writer) error {
	catalog, err := Catalog(filepath.Join(home, "source"))
	if err != nil {
		return err
	}
	enabled, err := enabledPlugins(home)
	if err != nil {
		return err
	}
	names := sortedKeys(catalog)
	for _, name := range state.Plugins {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "NAME\tAVAILABLE\tMANAGED\tENABLED\tDESCRIPTION")
	for _, name := range names {
		p, available := catalog[name]
		fmt.Fprintf(table, "%s\t%t\t%t\t%t\t%s\n", name, available, name == "manager" || slices.Contains(state.Plugins, name), slices.Contains(enabled, name), p.Description)
	}
	return table.Flush()
}
func disablePlugin(home, name string) error {
	if strings.HasPrefix(name, "agent:") {
		return disableAdapter(home, name)
	}
	_, err := config.UpdateDocument(home, func(doc *yaml.Node) (bool, error) {
		root := doc.Content[0]
		changed := false
		for i := 0; i+1 < len(root.Content); i += 2 {
			if root.Content[i].Value != "agent_plugins" && root.Content[i].Value != "agent_communication" {
				continue
			}
			node := root.Content[i+1]
			if node.Kind != yaml.SequenceNode {
				return false, fmt.Errorf("%s must be a sequence", root.Content[i].Value)
			}
			kept := node.Content[:0]
			for _, item := range node.Content {
				if item.Value == name {
					changed = true
				} else {
					kept = append(kept, item)
				}
			}
			node.Content = kept
		}
		return changed, nil
	})
	return err
}
func (e *Engine) changePlugin(ctx context.Context, o Options, state State, stage string, out io.Writer) error {
	catalog, err := Catalog(filepath.Join(o.Home, "source"))
	if err != nil {
		return err
	}
	path := "bin/" + managedBinary(o.Name)
	removing := o.Command == "plugins remove"
	if removing {
		if !slices.Contains(state.Plugins, o.Name) {
			return fmt.Errorf("plugin %s is not managed", o.Name)
		}
		if err = copyPrivate(filepath.Join(o.Home, "config.yaml"), filepath.Join(stage, "config.yaml")); err != nil {
			return err
		}
		if err = disablePlugin(stage, o.Name); err != nil {
			return err
		}
		state.Plugins = slices.DeleteFunc(state.Plugins, func(n string) bool { return n == o.Name })
	} else {
		p, ok := catalog[o.Name]
		if !ok {
			return fmt.Errorf("plugin %s is not available in installed source", o.Name)
		}
		if !slices.Contains(state.Plugins, o.Name) {
			if _, err := os.Lstat(filepath.Join(o.Home, path)); err == nil {
				return fmt.Errorf("unmanaged binary %s already exists", path)
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		if err = checkGoVersion(ctx, filepath.Join(o.Home, "source")); err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Join(stage, "bin"), 0700); err != nil {
			return err
		}
		if err = e.buildBinary(ctx, filepath.Join(o.Home, "source"), p.Package, filepath.Join(stage, path)); err != nil {
			return err
		}
		_, agentSupervisor := catalog["agent"]
		if err = prepareConfig(o.Home, stage, filepath.Join(o.Home, "source"), []string{o.Name}, false, agentSupervisor); err != nil {
			return err
		}
		if o.Name == "agent:timer" {
			if err = bootstrap.EnsureTimerSkill(o.Home); err != nil {
				return err
			}
		}
		if !slices.Contains(state.Plugins, o.Name) {
			state.Plugins = append(state.Plugins, o.Name)
		}
		slices.Sort(state.Plugins)
	}
	if err = writeJSON(filepath.Join(stage, ".manager", "state.json"), state); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	configChanged, err := configurationChanged(o.Home, stage)
	if err != nil {
		return err
	}
	paths := []string{path}
	if configChanged {
		paths = append(paths, "config.yaml")
	}
	paths = append(paths, ".manager/state.json")
	if err = e.deploy(o.Home, stage, paths, map[string]bool{path: removing}); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s: %s (plugin data and existing settings preserved)\n", o.Command, o.Name)
	return nil
}
