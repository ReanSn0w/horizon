package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ReanSn0w/horizon/internal/buildinfo"
	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/plugins"
	"io"
	"strings"
)

const Help = `Usage:
  horizon-manager install [--home PATH] [--branch NAME | --release] [--yes] [--shell bash|zsh|fish] [--adopt NAME]
  horizon-manager update [--home PATH]
  horizon-manager plugins list [--home PATH]
  horizon-manager plugins install NAME [--home PATH]
  horizon-manager plugins remove NAME [--home PATH]
After installation: horizon manager <command>.
`

type Options struct {
	Command, Name, Home, Shell string
	Source                     *config.Source
	Yes, ExplicitHome          bool
	Adopt                      []string
}

func Parse(args []string) (Options, error) {
	var o Options
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--yes":
			o.Yes = true
		case "--release":
			if o.Source != nil {
				return o, errors.New("choose one source")
			}
			o.Source = &config.Source{Type: "release"}
		case "--home", "--branch", "--shell", "--adopt":
			i++
			if i >= len(args) || args[i] == "" {
				return o, fmt.Errorf("%s requires a value", arg)
			}
			switch arg {
			case "--home":
				o.Home = args[i]
				o.ExplicitHome = true
			case "--shell":
				o.Shell = args[i]
			case "--adopt":
				o.Adopt = append(o.Adopt, args[i])
			case "--branch":
				if o.Source != nil {
					return o, errors.New("choose one source")
				}
				o.Source = &config.Source{Type: "branch", Branch: args[i]}
			}
		default:
			if strings.HasPrefix(arg, "-") {
				return o, fmt.Errorf("unknown option %s", arg)
			}
			positional = append(positional, arg)
		}
	}
	if len(positional) == 1 && (positional[0] == "install" || positional[0] == "update") {
		o.Command = positional[0]
	} else if len(positional) >= 2 && positional[0] == "plugins" {
		o.Command = "plugins " + positional[1]
		if o.Command == "plugins list" && len(positional) == 2 {
		} else if (o.Command == "plugins install" || o.Command == "plugins remove") && len(positional) == 3 {
			o.Name = positional[2]
			if !namePattern.MatchString(o.Name) || o.Name == "manager" {
				return o, errors.New("choose an optional plugin; manager is mandatory")
			}
		} else {
			return o, errors.New("invalid plugin command")
		}
	} else {
		return o, errors.New("expected install, update or plugins command")
	}
	if o.Command != "install" && (o.Source != nil || o.Yes || o.Shell != "" || len(o.Adopt) > 0) {
		return o, errors.New("installation options require install")
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
	if len(args) == 1 && args[0] == "horizon-plugin-metadata" {
		if err := json.NewEncoder(out).Encode(plugins.Metadata{ProtocolVersion: 1, Version: buildinfo.Version, Description: "Install and maintain Horizon from source"}); err != nil {
			return 1
		}
		return 0
	}
	if len(args) == 0 || len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprint(out, Help)
		return 0
	}
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			fmt.Fprint(out, Help)
			return 0
		}
	}
	o, err := Parse(args)
	if err != nil {
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
