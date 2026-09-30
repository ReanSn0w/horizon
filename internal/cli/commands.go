package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/ReanSn0w/horizon/internal/agent"
	"github.com/ReanSn0w/horizon/internal/bootstrap"
	"github.com/ReanSn0w/horizon/internal/eventstream"
	"github.com/ReanSn0w/horizon/internal/instructions"
	"github.com/ReanSn0w/horizon/internal/responses"
	"github.com/ReanSn0w/horizon/internal/session"
	flags "github.com/umputun/go-flags"
)

type optionalString struct {
	Value string
	Set   bool
}

type initCommand struct {
	app    *App
	global *globalOptions
}

func (command *initCommand) Execute(args []string) error {
	if err := rejectArgs(args); err != nil {
		return err
	}
	home := command.global.Home
	if _, err := bootstrap.Ensure(home); err != nil {
		return failure(err.Error(), err)
	}
	fmt.Fprintf(command.app.out, "Horizon home: %s\n", home)
	fmt.Fprintf(command.app.out, "Отредактируйте %s: задайте provider.url, provider.key и models.chatting.model; проверьте models.chatting.compact_threshold.\n", filepath.Join(home, "config.yaml"))
	fmt.Fprintf(command.app.out, "При необходимости добавьте инструкции агента в %s.\n", filepath.Join(home, "AGENTS.md"))
	return nil
}

func (value *optionalString) UnmarshalFlag(text string) error {
	value.Value = text
	value.Set = true
	return nil
}

type modelsCommand struct {
	app    *App
	global *globalOptions
}

func (command *modelsCommand) Execute(args []string) error {
	if err := rejectArgs(args); err != nil {
		return err
	}
	_, cfg, err := command.app.loadConfig(command.global)
	if err != nil {
		return err
	}
	if len(cfg.Models) == 0 {
		fmt.Fprintln(command.app.out, "No model profiles configured.")
		return nil
	}

	names := make([]string, 0, len(cfg.Models))
	for name := range cfg.Models {
		names = append(names, name)
	}
	sort.Strings(names)

	writer := tabwriter.NewWriter(command.app.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "NAME\tMODEL\tREASONING\tSCORE\tDEFAULT")
	for _, name := range names {
		profile := cfg.Models[name]
		reasoning := profile.Reasoning
		if reasoning == "" {
			reasoning = "-"
		}
		score := "-"
		if profile.Score != nil {
			score = fmt.Sprint(*profile.Score)
		}
		isDefault := "-"
		if name == cfg.DefaultModel {
			isDefault = "*"
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", name, profile.Model, reasoning, score, isDefault)
	}
	return writer.Flush()
}

type resumeCommand struct {
	app     *App
	global  *globalOptions
	Session string         `long:"session" description:"session ID; defaults to the most recently accessed session in this workspace"`
	Mode    string         `short:"o" long:"mode" default:"text" choice:"text" choice:"plain" choice:"jsonl" description:"text: tool status and answer; plain: answer only (-m or stdin); jsonl: all events"`
	Model   string         `long:"model" description:"named model profile; defaults to default_model"`
	Message optionalString `short:"m" long:"message" description:"message; otherwise read redirected stdin or open the interactive editor"`
}

func (command *resumeCommand) Execute(args []string) error {
	if err := rejectArgs(args); err != nil {
		return err
	}
	home, cfg, err := command.app.loadConfig(command.global)
	if err != nil {
		return err
	}
	profileName, profile, err := cfg.SelectModel(command.Model)
	if err != nil {
		return usage(err.Error(), err)
	}
	message, err := command.app.readMessageMode(command.Message, command.Mode)
	if err != nil {
		return err
	}
	store, workspace, err := command.app.sessionContext(command.global, "")
	if err != nil {
		return err
	}
	locked, value, err := store.AcquireForResume(workspace, command.Session, message)
	if err != nil {
		return failure(err.Error(), err)
	}
	defer locked.Close()
	snapshot, err := buildInstructions(home, workspace.Dir, cfg.DisabledSkills)
	if err != nil {
		return failure(err.Error(), err)
	}
	responsesURL, err := cfg.Endpoint("responses")
	if err != nil {
		return failure(err.Error(), err)
	}
	compactURL, err := cfg.Endpoint("responses/compact")
	if err != nil {
		return failure(err.Error(), err)
	}
	runtime := &agent.Runtime{
		Client: &responses.Client{ResponsesURL: responsesURL, CompactURL: compactURL, APIKey: cfg.Provider.Key, HTTPClient: http.DefaultClient},
		Locked: locked, Store: store, Workspace: workspace, Session: value,
		ProfileName:  profileName,
		Profile:      session.ModelProfile{Name: profileName, Model: profile.Model, Reasoning: profile.Reasoning, CompactThreshold: profile.CompactThreshold},
		Instructions: snapshot,
		MaxRequests:  cfg.Limits.MaxModelRequests,
		MaxDuration:  cfg.Limits.MaxTurnDuration,
		Publish:      newEventPublisher(command.Mode, command.global.Verbose, command.app.out, command.app.errOut),
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	result, err := runtime.Run(ctx, message)
	if err != nil {
		var runtimeError *agent.Error
		if errors.As(err, &runtimeError) && runtimeError.Code == "turn_cancelled" {
			return &Error{Code: ExitInterrupted, Message: err.Error(), Cause: err}
		}
		return failure(err.Error(), err)
	}
	if command.Mode != "jsonl" && result.Text != "" {
		fmt.Fprintln(command.app.out, result.Text)
	}
	return nil
}

type sessionsListCommand struct {
	app       *App
	global    *globalOptions
	Workspace string `long:"workspace" description:"filter by workspace directory"`
	Active    bool   `long:"active" description:"show only sessions with an active turn"`
}

func (command *sessionsListCommand) Execute(args []string) error {
	if err := rejectArgs(args); err != nil {
		return err
	}
	store, workspace, err := command.app.sessionContext(command.global, command.Workspace)
	if err != nil {
		return err
	}
	items, err := store.List(workspace, command.Active)
	if err != nil {
		return failure(err.Error(), err)
	}
	writer := tabwriter.NewWriter(command.app.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tTITLE\tLAST ACCESSED\tACTIVE")
	for _, item := range items {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%t\n", item.Session.SessionID, item.Session.Title, item.Session.LastAccessedAt.Format("2006-01-02T15:04:05Z07:00"), item.Active)
	}
	return writer.Flush()
}

type sessionsCreateCommand struct {
	app    *App
	global *globalOptions
	Fork   string `long:"fork" description:"source session ID to fork"`
}

func (command *sessionsCreateCommand) Execute(args []string) error {
	if err := rejectArgs(args); err != nil {
		return err
	}
	store, workspace, err := command.app.sessionContext(command.global, "")
	if err != nil {
		return err
	}
	if command.Fork != "" {
		created, err := store.Fork(workspace, command.Fork)
		if err != nil {
			return failure(err.Error(), err)
		}
		fmt.Fprintln(command.app.out, created.SessionID)
		return nil
	}
	created, err := store.Create(workspace)
	if err != nil {
		return failure(err.Error(), err)
	}
	fmt.Fprintln(command.app.out, created.SessionID)
	return nil
}

type sessionsReadCommand struct {
	app    *App
	global *globalOptions
	ID     string `long:"id" required:"true" description:"session ID"`
	Turns  int    `long:"turns" default:"16" description:"number of complete turns to show, including failed and cancelled turns"`
}

func (command *sessionsReadCommand) Execute(args []string) error {
	if command.Turns <= 0 {
		return usage("--turns must be positive", nil)
	}
	store, workspace, err := command.app.sessionContext(command.global, "")
	if err != nil {
		return err
	}
	value, err := store.ReadForWorkspace(workspace, command.ID)
	if err != nil {
		return failure(err.Error(), err)
	}
	if err := session.WriteHistory(command.app.out, value, command.Turns); err != nil {
		return failure(err.Error(), err)
	}
	return nil
}

type sessionsDeleteCommand struct {
	app    *App
	global *globalOptions
	ID     string `long:"id" required:"true" description:"session ID"`
}

func (command *sessionsDeleteCommand) Execute(args []string) error {
	if err := rejectArgs(args); err != nil {
		return err
	}
	store, workspace, err := command.app.sessionContext(command.global, "")
	if err != nil {
		return err
	}
	if err := store.Delete(workspace, command.ID); err != nil {
		return failure(err.Error(), err)
	}
	return nil
}

type sessionsCompactCommand struct {
	app    *App
	global *globalOptions
	ID     string `long:"id" description:"session ID; defaults to the most recently accessed session"`
	Mode   string `short:"o" long:"mode" default:"text" choice:"text" choice:"jsonl" description:"output format"`
}

type sessionsCommand struct{}

func (command *sessionsCompactCommand) Execute(args []string) error {
	if err := rejectArgs(args); err != nil {
		return err
	}
	store, workspace, err := command.app.sessionContext(command.global, "")
	if err != nil {
		return err
	}
	locked, value, err := store.AcquireForCompact(workspace, command.ID)
	if err != nil {
		return failure(err.Error(), err)
	}
	defer locked.Close()
	publish := newEventPublisher(command.Mode, command.global.Verbose, command.app.out, command.app.errOut)
	if _, ok := value.LastCompletedTurn(); !ok {
		if command.Mode == "jsonl" {
			publish(eventstream.New("compaction_completed", value.SessionID, nil, map[string]any{"compacted": false}))
		} else {
			fmt.Fprintln(command.app.out, "сжимать нечего")
		}
		return nil
	}
	home, cfg, err := command.app.loadConfig(command.global)
	if err != nil {
		return err
	}
	snapshot, err := buildInstructions(home, workspace.Dir, cfg.DisabledSkills)
	if err != nil {
		return failure(err.Error(), err)
	}
	responsesURL, err := cfg.Endpoint("responses")
	if err != nil {
		return failure(err.Error(), err)
	}
	compactURL, err := cfg.Endpoint("responses/compact")
	if err != nil {
		return failure(err.Error(), err)
	}
	client := &responses.Client{ResponsesURL: responsesURL, CompactURL: compactURL, APIKey: cfg.Provider.Key, HTTPClient: http.DefaultClient}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	result, err := agent.Compact(ctx, client, locked, value, snapshot.Prompt, cfg.Limits.MaxTurnDuration, publish)
	if err != nil {
		return failure(err.Error(), err)
	}
	if command.Mode == "text" {
		fmt.Fprintf(command.app.out, "Сессия %s сжата.\n", result.SessionID)
	}
	return nil
}

func (a *App) addCommands(parser *flags.Parser, global *globalOptions) error {
	if err := a.addSkillCommands(parser, global); err != nil {
		return err
	}
	commands := []struct {
		name, short, long string
		data              any
	}{
		{"init", "Initialize Horizon home", "Create missing home files and show what to configure.", &initCommand{app: a, global: global}},
		{"models", "List model profiles", "Print configured model profiles without contacting the provider.", &modelsCommand{app: a, global: global}},
		{"resume", "Run one agent turn", "Read a message and execute one turn.", &resumeCommand{app: a, global: global}},
	}
	for _, item := range commands {
		if _, err := parser.AddCommand(item.name, item.short, item.long, item.data); err != nil {
			return err
		}
	}

	sessions, err := parser.AddCommand("sessions", "Manage sessions", "Create, inspect, compact, and delete stored sessions.", &sessionsCommand{})
	if err != nil {
		return err
	}
	subcommands := []struct {
		name, short, long string
		data              any
	}{
		{"list", "List sessions", "List stored sessions.", &sessionsListCommand{app: a, global: global}},
		{"create", "Create a session", "Create an empty session or fork a session in this workspace.", &sessionsCreateCommand{app: a, global: global}},
		{"read", "Read session history", "Print the latest complete turns in a session.", &sessionsReadCommand{app: a, global: global}},
		{"delete", "Delete a session", "Delete a session and its artifacts.", &sessionsDeleteCommand{app: a, global: global}},
		{"compact", "Compact session context", "Compact the working context of a session.", &sessionsCompactCommand{app: a, global: global}},
	}
	for _, item := range subcommands {
		if _, err := sessions.AddCommand(item.name, item.short, item.long, item.data); err != nil {
			return err
		}
	}
	return nil
}

func rejectArgs(args []string) error {
	if len(args) == 0 {
		return nil
	}
	return usage(fmt.Sprintf("unexpected arguments: %v", args), nil)
}

func buildInstructions(home, workspace string, disabled []string) (*instructions.Snapshot, error) {
	return instructions.Build(home, workspace, agent.Introduction, instructions.Options{DisabledSkills: disabled})
}
