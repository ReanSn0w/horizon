package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ReanSn0w/horizon/internal/bootstrap"
	"github.com/ReanSn0w/horizon/internal/config"
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
		home, err := config.ResolveHome(options.Home)
		if err != nil {
			return usage(err.Error(), err)
		}
		prepared, err := bootstrap.Ensure(home)
		if err != nil {
			return failure(err.Error(), err)
		}
		options.Home = home
		if prepared.ConfigCreated {
			switch command.(type) {
			case *sessionsListCommand, *sessionsCreateCommand, *sessionsReadCommand, *sessionsDeleteCommand:
				fmt.Fprintf(a.errOut, "Horizon: создан %s/config.yaml; заполните provider.url, provider.key и models.<профиль>.model, проверьте compact_threshold перед обращением к провайдеру.\n", home)
			}
		}
		return command.Execute(remaining)
	}
	if err := a.addCommands(parser, &options); err != nil {
		fmt.Fprintf(a.errOut, "horizon: initialize CLI: %v\n", err)
		return ExitFailure
	}

	_, err := parser.ParseArgs(defaultResumeArgs(args))
	if err == nil {
		return ExitOK
	}
	var flagError *flags.Error
	if errors.As(err, &flagError) {
		if flagError.Type == flags.ErrHelp {
			fmt.Fprint(a.out, flagError.Message)
			return ExitOK
		}
		fmt.Fprintf(a.errOut, "horizon: %s\n", flagError.Message)
		return ExitUsage
	}
	fmt.Fprintf(a.errOut, "horizon: %v\n", err)
	return exitCode(err)
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
