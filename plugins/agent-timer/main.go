package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/ReanSn0w/horizon/internal/buildinfo"
	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/daemon"
	"github.com/ReanSn0w/horizon/internal/timer"
	flags "github.com/umputun/go-flags"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, time.Now))
}

func run(ctx context.Context, args []string, out, diag io.Writer, now func() time.Time) int {
	if len(args) != 0 {
		switch args[0] {
		case "agent-plugin-metadata":
			if len(args) == 1 {
				err := json.NewEncoder(out).Encode(daemon.AdapterMetadata{
					ProtocolVersion:       1,
					ConfigProtocolVersion: 1,
					Version:               buildinfo.Version,
					Description:           "Markdown timer schedules",
					Capabilities:          []string{daemon.AdapterAdminCLI, daemon.AdapterValidate},
				})
				if err != nil {
					fmt.Fprintln(diag, err)
					return 1
				}
				return 0
			}
		case "agent-plugin-config-template":
			if len(args) == 1 {
				if err := json.NewEncoder(out).Encode(map[string]any{"config_protocol_version": 1, "section": timer.ConfigTemplate()}); err != nil {
					fmt.Fprintln(diag, err)
					return 1
				}
				return 0
			}
		case "agent-plugin-validate":
			if len(args) == 1 {
				home, err := config.ResolveHome("")
				if err == nil {
					_, err = timer.LoadSettings(home)
				}
				if err != nil {
					fmt.Fprintln(diag, err)
					return 1
				}
				return admin([]string{"validate"}, io.Discard, diag, now)
			}
		case "agent-run":
			if len(args) == 1 {
				// Daemon forwards the runtime's diagnostic stream to its stderr.
				if err := runSupervised(ctx, out); err != nil {
					fmt.Fprintln(out, "agent-timer:", err)
					return 1
				}
				return 0
			}
		case "agent-admin":
			return admin(args[1:], out, diag, now)
		}
	}
	fmt.Fprintln(diag, "Use horizon agent timer --help")
	return 2
}

func admin(args []string, out, diag io.Writer, now func() time.Time) int {
	var validateOptions, statusOptions struct {
		Args struct {
			ID string `positional-arg-name:"TIMER_ID"`
		} `positional-args:"yes"`
	}
	var unblockOptions struct {
		Args struct {
			ID string `positional-arg-name:"TIMER_ID" required:"yes"`
		} `positional-args:"yes"`
	}
	var listOptions struct {
		JSON bool `long:"json" description:"print a JSON array"`
	}
	parser := flags.NewParser(&struct{}{}, flags.HelpFlag|flags.PassDoubleDash)
	parser.Name = "horizon agent timer"
	parser.AddCommand("validate", "Validate schedules", "Check one timer or all timer files.", &validateOptions)
	parser.AddCommand("list", "List timer definitions", "Show every timer file, including invalid definitions.", &listOptions)
	parser.AddCommand("status", "Show timer status", "Show one timer or all timers.", &statusOptions)
	parser.AddCommand("unblock", "Unblock a timer", "Allow future slots after an unknown outcome.", &unblockOptions)
	parser.AddCommand("enable", "Enable a timer", "Enable a validated timer definition.", &unblockOptions)
	parser.AddCommand("disable", "Disable a timer", "Disable future firings of a timer.", &unblockOptions)
	parser.AddCommand("run", "Run a timer now", "Queue a fresh timer task without changing its schedule.", &unblockOptions)
	if len(args) == 0 {
		args = []string{"--help"}
	}
	remaining, err := parser.ParseArgs(args)
	if err != nil {
		var flagErr *flags.Error
		if errors.As(err, &flagErr) && flagErr.Type == flags.ErrHelp {
			fmt.Fprint(out, flagErr.Message)
			return 0
		}
		fmt.Fprintln(diag, err)
		return 2
	}
	if len(remaining) > 0 {
		fmt.Fprintln(diag, "unexpected arguments")
		return 2
	}
	args = []string{parser.Active.Name}
	id := validateOptions.Args.ID
	if args[0] == "status" {
		id = statusOptions.Args.ID
	}
	if args[0] == "unblock" || args[0] == "enable" || args[0] == "disable" || args[0] == "run" {
		id = unblockOptions.Args.ID
	}
	if id != "" {
		args = append(args, id)
	}
	home, err := config.ResolveHome("")
	if err != nil {
		fmt.Fprintln(diag, err)
		return 1
	}
	if args[0] == "status" {
		return status(home, args[1:], out, diag)
	}
	if args[0] == "list" {
		return list(home, listOptions.JSON, out, diag)
	}
	if args[0] == "run" {
		if os.Getenv("HORIZON_INHERITED_ACCESS") == "read" {
			fmt.Fprintln(diag, "running a timer requires write access")
			return 1
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		taskID, err := timer.RunNow(ctx, home, args[1])
		if err != nil {
			fmt.Fprintln(diag, err)
			return 1
		}
		fmt.Fprintln(out, taskID)
		return 0
	}
	if args[0] == "enable" || args[0] == "disable" {
		if os.Getenv("HORIZON_INHERITED_ACCESS") == "read" {
			fmt.Fprintln(diag, "changing a timer requires write access")
			return 1
		}
		_, err := timer.SetEnabled(home, args[1], args[0] == "enable")
		if err != nil {
			fmt.Fprintln(diag, err)
			return 1
		}
		fmt.Fprintf(out, "%s: %sd\n", args[1], args[0])
		return 0
	}
	if args[0] == "unblock" {
		if os.Getenv("HORIZON_INHERITED_ACCESS") == "read" {
			fmt.Fprintln(diag, "unblock requires write access")
			return 1
		}
		if err := timer.Unblock(home, args[1]); err != nil {
			fmt.Fprintln(diag, err)
			return 1
		}
		fmt.Fprintf(out, "%s: unblocked for future slots\n", args[1])
		return 0
	}
	var results []timer.FileResult
	if len(args) == 2 {
		def, err := timer.LoadFile(home, args[1])
		results = []timer.FileResult{{ID: args[1], Definition: def, Err: err}}
	} else {
		results, err = timer.LoadCatalog(home)
		if err != nil {
			fmt.Fprintln(diag, err)
			return 1
		}
	}
	code := 0
	at := now()
	for _, result := range results {
		err := result.Err
		if err == nil {
			schedule, compileErr := timer.Compile(result.Definition.Schedule)
			err = compileErr
			if err == nil {
				_, err = schedule.Next(at)
			}
		}
		if err != nil {
			fmt.Fprintf(diag, "%s: %v\n", result.ID, err)
			code = 1
			continue
		}
		status := "valid"
		if !result.Definition.Enabled {
			status += " (disabled)"
		}
		fmt.Fprintf(out, "%s: %s\n", result.ID, status)
	}
	return code
}

func list(home string, asJSON bool, out, diag io.Writer) int {
	results, err := timer.LoadCatalog(home)
	if err != nil {
		fmt.Fprintln(diag, err)
		return 1
	}
	type row struct {
		ID        string                `json:"id"`
		Name      string                `json:"name,omitempty"`
		Enabled   bool                  `json:"enabled"`
		Schedule  *timer.ScheduleConfig `json:"schedule,omitempty"`
		Workspace string                `json:"workspace,omitempty"`
		Error     string                `json:"error,omitempty"`
	}
	rows := make([]row, 0, len(results))
	for _, result := range results {
		item := row{ID: result.ID}
		if result.Err != nil {
			item.Error = result.Err.Error()
		} else {
			item.Name, item.Enabled, item.Schedule, item.Workspace = result.Definition.Name, result.Definition.Enabled, &result.Definition.Schedule, result.Definition.Workspace
			compiled, compileErr := timer.Compile(result.Definition.Schedule)
			if compileErr != nil {
				item.Error = compileErr.Error()
			} else if _, nextErr := compiled.Next(time.Now()); nextErr != nil {
				item.Error = nextErr.Error()
			}
		}
		rows = append(rows, item)
	}
	if asJSON {
		if err := json.NewEncoder(out).Encode(rows); err != nil {
			fmt.Fprintln(diag, err)
			return 1
		}
		return 0
	}
	writer := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tNAME\tENABLED\tSCHEDULE\tWORKSPACE\tERROR")
	for _, item := range rows {
		schedule := ""
		if item.Schedule != nil {
			schedule = item.Schedule.Cron
			if schedule == "" {
				schedule = item.Schedule.Every
			}
		}
		fmt.Fprintf(writer, "%s\t%s\t%t\t%s\t%s\t%s\n", item.ID, item.Name, item.Enabled, schedule, item.Workspace, item.Error)
	}
	if err := writer.Flush(); err != nil {
		fmt.Fprintln(diag, err)
		return 1
	}
	return 0
}

func status(home string, args []string, out, diag io.Writer) int {
	records, err := timer.Status(home)
	if err != nil {
		fmt.Fprintln(diag, err)
		return 1
	}
	if len(args) == 1 {
		if !timer.ValidID(args[0]) {
			fmt.Fprintln(diag, "invalid timer-id")
			return 2
		}
		record, found := records[args[0]]
		records = map[string]timer.Record{}
		if found {
			records[args[0]] = record
		}
	}
	if err := json.NewEncoder(out).Encode(records); err != nil {
		fmt.Fprintln(diag, err)
		return 1
	}
	return 0
}

func runSupervised(ctx context.Context, out io.Writer) error {
	home, err := config.ResolveHome("")
	if err != nil {
		return err
	}
	if os.Getenv("HORIZON_ADAPTER_NAME") != daemon.TimerSource || os.Getenv("HORIZON_DAEMON_SOCKET") != daemon.Socket(home) {
		return fmt.Errorf("agent-timer must run under the Horizon daemon supervisor")
	}
	settings, err := timer.LoadSettings(home)
	if err != nil {
		return err
	}
	return timer.Run(ctx, home, os.Getenv("HORIZON_ADAPTER_CREDENTIAL"), os.Getenv("HORIZON_INHERITED_ACCESS"), settings, out)
}
