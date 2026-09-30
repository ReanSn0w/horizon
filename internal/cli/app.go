package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/plugins"
	"github.com/ReanSn0w/horizon/internal/session"
	flags "github.com/umputun/go-flags"
)

type App struct {
	in          io.Reader
	out         io.Writer
	errOut      io.Writer
	interactive func() bool
	editor      MessageEditor
	workingDir  func() (string, error)
}

type globalOptions struct {
	Verbose bool   `short:"v" long:"verbose" description:"show full tool results and diagnostics"`
	Home    string `long:"home" description:"Horizon home directory (default: user home/.horizon)"`
}

type commandFunc func([]string) error

func (fn commandFunc) Execute(args []string) error { return fn(args) }

func New(in io.Reader, out, errOut io.Writer) *App {
	app := &App{
		in:          in,
		out:         out,
		errOut:      errOut,
		interactive: func() bool { return isInteractive(in) },
		workingDir:  os.Getwd,
	}
	inputFile, inputOK := in.(*os.File)
	errorFile, errorOK := errOut.(*os.File)
	if inputOK && errorOK {
		app.editor = newTerminalEditor(inputFile, errorFile)
	}
	return app
}

func (a *App) Run(args []string) int {
	var options globalOptions
	parser := flags.NewParser(&options, flags.HelpFlag|flags.PassDoubleDash)
	parser.Name = "horizon"
	parser.ShortDescription = "CLI agent for repository work"
	parser.LongDescription = "Horizon executes one agent turn per process and stores session state in its home directory. Without a command, runs resume (including -m and other resume options)."
	parser.SubcommandsOptional = false
	parser.CommandHandler = func(command flags.Commander, remaining []string) error {
		if err := rejectArgs(remaining); err != nil {
			return err
		}
		if validator, ok := command.(interface{ ValidateArgs() error }); ok {
			if err := validator.ValidateArgs(); err != nil {
				return err
			}
		}
		home, err := config.ResolveHome(options.Home)
		if err != nil {
			return usage(err.Error(), err)
		}
		options.Home = home
		if _, ok := command.(*initCommand); !ok {
			if err := requireInitializedHome(home); err != nil {
				return err
			}
		}
		return command.Execute(remaining)
	}
	if err := a.addCommands(parser, &options); err != nil {
		fmt.Fprintf(a.errOut, "horizon: initialize CLI: %v\n", err)
		return ExitFailure
	}
	reserved := commandNames(parser)
	if index, homeFlag, ok := pluginCommandIndex(args); ok {
		home, err := config.ResolveHome(homeFlag)
		if err != nil {
			fmt.Fprintf(a.errOut, "horizon: %v\n", err)
			return ExitUsage
		}
		name := args[index]
		if parser.Find(name) == nil {
			entry := plugins.Inspect(home, name, reserved)
			if entry.Err == nil || !os.IsNotExist(entry.Err) {
				if entry.Err != nil {
					fmt.Fprintf(a.errOut, "horizon: plugin %q unavailable: %v\n", name, entry.Err)
					return ExitUsage
				}
				if !pluginHelp(args[index+1:]) {
					if err := requireInitializedHome(home); err != nil {
						fmt.Fprintf(a.errOut, "horizon: %v\n", err)
						return exitCode(err)
					}
				}
				return a.runPlugin(entry, args[index+1:], home)
			}
		}
	}

	_, err := parser.ParseArgs(defaultResumeArgs(args))
	if err == nil {
		return ExitOK
	}
	var flagError *flags.Error
	if errors.As(err, &flagError) {
		if flagError.Type == flags.ErrHelp {
			fmt.Fprint(a.out, flagError.Message)
			if rootHelp(args) {
				a.printPluginHelp(options.Home, reserved)
			}
			return ExitOK
		}
		fmt.Fprintf(a.errOut, "horizon: %s\n", flagError.Message)
		return ExitUsage
	}
	fmt.Fprintf(a.errOut, "horizon: %v\n", err)
	return exitCode(err)
}

func pluginHelp(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}

func commandNames(parser *flags.Parser) []string {
	var names []string
	for _, command := range parser.Commands() {
		names = append(names, command.Name)
		names = append(names, command.Aliases...)
	}
	return names
}

// pluginCommandIndex recognizes only supported global flags before a command.
func pluginCommandIndex(args []string) (int, string, bool) {
	var home string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--home":
			if i+1 >= len(args) {
				return 0, "", false
			}
			home = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--home="):
			home = strings.TrimPrefix(args[i], "--home=")
		case args[i] == "--verbose" || args[i] == "-v":
		case args[i] == "--help" || args[i] == "-h":
			return 0, home, false
		case strings.HasPrefix(args[i], "-"):
			return 0, home, false
		default:
			return i, home, true
		}
	}
	return 0, home, false
}

func rootHelp(args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--help" || arg == "-h" {
			return true
		}
		if arg == "--home" {
			i++
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			return false
		}
	}
	return false
}

func requireInitializedHome(home string) error {
	path := filepath.Join(home, "config.yaml")
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return usage(fmt.Sprintf("Horizon home %q is not initialized; run 'horizon init' with the same --home or HORIZON_HOME", home), err)
	}
	if err != nil {
		return failure(fmt.Sprintf("read Horizon home configuration %q: %v", path, err), err)
	}
	if !info.Mode().IsRegular() {
		return usage(fmt.Sprintf("Horizon home configuration %q is not a regular file", path), nil)
	}
	return nil
}

func (a *App) loadConfig(options *globalOptions) (string, config.Config, error) {
	home, err := config.ResolveHome(options.Home)
	if err != nil {
		return "", config.Config{}, usage(err.Error(), err)
	}
	loaded, err := config.Load(home)
	if err != nil {
		var pathError *os.PathError
		if errors.As(err, &pathError) {
			return "", config.Config{}, failure(err.Error(), err)
		}
		return "", config.Config{}, usage(err.Error(), err)
	}
	return home, loaded, nil
}

func (a *App) sessionContext(options *globalOptions, workspacePath string) (*session.Store, session.Workspace, error) {
	home, err := config.ResolveHome(options.Home)
	if err != nil {
		return nil, session.Workspace{}, usage(err.Error(), err)
	}
	if workspacePath == "" {
		workspacePath, err = a.workingDir()
		if err != nil {
			return nil, session.Workspace{}, failure(fmt.Sprintf("get working directory: %v", err), err)
		}
	}
	store := session.NewStore(home)
	workspace, err := store.ResolveWorkspace(workspacePath)
	if err != nil {
		return nil, session.Workspace{}, failure(err.Error(), err)
	}
	return store, workspace, nil
}

// defaultResumeArgs skips only global options and their values. Once a command
// or a resume option is reached, the parser owns the remainder of the arguments.
func defaultResumeArgs(args []string) []string {
	i := 0
	for i < len(args) {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			return args
		case arg == "--verbose" || arg == "-v":
			i++
		case arg == "--home":
			if i+1 == len(args) {
				return args
			}
			i += 2
		case strings.HasPrefix(arg, "--home="):
			i++
		default:
			if !strings.HasPrefix(arg, "-") {
				return args
			}
			return insertResume(args, i)
		}
	}
	return insertResume(args, i)
}

func insertResume(args []string, index int) []string {
	result := make([]string, 0, len(args)+1)
	result = append(result, args[:index]...)
	result = append(result, "resume")
	return append(result, args[index:]...)
}
