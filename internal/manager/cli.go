package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/ReanSn0w/horizon/internal/buildinfo"
	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/plugins"
	flags "github.com/umputun/go-flags"
)

type Options struct {
	Command, Name, Home, Shell string
	Source                     *config.Source
	Yes, ExplicitHome          bool
	Adopt                      []string
}

type installFlags struct {
	Branch  string   `long:"branch" description:"Install a branch" value-name:"NAME"`
	Release bool     `long:"release" description:"Install the latest release"`
	Yes     bool     `long:"yes" description:"Use the selected home without prompting"`
	Shell   string   `long:"shell" choice:"bash" choice:"zsh" choice:"fish" description:"Print shell setup instructions"`
	Adopt   []string `long:"adopt" description:"Adopt an existing optional plugin" value-name:"NAME"`
}
type pluginNameFlags struct {
	Args struct {
		Name string `positional-arg-name:"NAME" required:"yes"`
	} `positional-args:"yes"`
}

func Parse(args []string) (Options, error) {
	var o Options
	var global struct {
		Home string `long:"home" description:"Horizon home directory" value-name:"PATH"`
	}
	var install installFlags
	var add, remove pluginNameFlags
	parser := flags.NewParser(&global, flags.HelpFlag|flags.PassDoubleDash)
	parser.Name = "horizon-manager"
	parser.LongDescription = "Install and maintain Horizon from source. Adapter names use agent:NAME; CLI plugin names use NAME."
	parser.AddCommand("install", "Install Horizon", "Build and install Horizon from source.", &install)
	parser.AddCommand("update", "Update the installation", "Update the managed binaries from the configured source.", &struct{}{})
	pluginsCommand, _ := parser.AddCommand("plugins", "Manage plugins", "List, install or remove managed plugins.", &struct{}{})
	pluginsCommand.AddCommand("list", "List plugins", "Show available, managed and enabled plugins.", &struct{}{})
	pluginsCommand.AddCommand("install", "Install a plugin", "Use agent:NAME for an adapter.", &add)
	pluginsCommand.AddCommand("remove", "Remove a plugin", "Preserve plugin data and settings.", &remove)
	remaining, err := parser.ParseArgs(args)
	if err != nil {
		return o, err
	}
	if len(remaining) != 0 {
		return o, errors.New("unexpected arguments")
	}
	o.Command = parser.Active.Name
	if o.Command == "plugins" {
		o.Command += " " + parser.Active.Active.Name
		if o.Command == "plugins install" {
			o.Name = add.Args.Name
		}
		if o.Command == "plugins remove" {
			o.Name = remove.Args.Name
		}
		if o.Name != "" && (!validManagedName(o.Name) || o.Name == "manager") {
			return o, errors.New("choose an optional plugin; manager is mandatory")
		}
	}
	o.Home, o.ExplicitHome = global.Home, global.Home != ""
	o.Yes, o.Shell, o.Adopt = install.Yes, install.Shell, install.Adopt
	if install.Release && install.Branch != "" {
		return o, errors.New("choose one source")
	}
	if install.Release {
		o.Source = &config.Source{Type: "release"}
	}
	if install.Branch != "" {
		o.Source = &config.Source{Type: "branch", Branch: install.Branch}
	}
	if o.Source != nil {
		if err := o.Source.Validate(); err != nil {
			return o, err
		}
	}
	for _, name := range o.Adopt {
		if !namePattern.MatchString(name) || name == "manager" {
			return o, errors.New("invalid legacy plugin name")
		}
	}
	if o.Shell != "" && o.Shell != "bash" && o.Shell != "zsh" && o.Shell != "fish" {
		return o, errors.New("shell must be bash, zsh or fish")
	}
	home, err := config.ResolveHome(o.Home)
	o.Home = home
	return o, err
}
func Run(ctx context.Context, args []string, in io.Reader, out, diag io.Writer, execute func(context.Context, Options, io.Reader, io.Writer) error) int {
	if len(args) == 2 && args[0] == continueUpdate {
		if err := resumePrepared(ctx, args[1], out); err != nil {
			fmt.Fprintln(diag, "manager:", err)
			if ctx.Err() != nil {
				return 130
			}
			return 1
		}
		return 0
	}
	if len(args) == 1 && args[0] == "horizon-plugin-metadata" {
		if err := json.NewEncoder(out).Encode(plugins.Metadata{ProtocolVersion: 1, Version: buildinfo.Version, Description: "Install and maintain Horizon from source"}); err != nil {
			return 1
		}
		return 0
	}
	if len(args) == 0 {
		args = []string{"--help"}
	}
	o, err := Parse(args)
	if err != nil {
		var flagErr *flags.Error
		if errors.As(err, &flagErr) && flagErr.Type == flags.ErrHelp {
			fmt.Fprint(out, flagErr.Message)
			return 0
		}
		fmt.Fprintln(diag, "manager:", err)
		return 2
	}
	if execute == nil {
		execute = Execute
	}
	if err = execute(ctx, o, in, out); err != nil {
		fmt.Fprintln(diag, "manager:", err)
		if ctx.Err() != nil {
			return 130
		}
		return 1
	}
	return 0
}
