package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ReanSn0w/horizon/internal/buildinfo"
	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/daemon"
	"github.com/ReanSn0w/horizon/internal/timer"
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
					Capabilities:          []string{daemon.AdapterAdminCLI},
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
	const usage = "Usage: horizon agent timer validate [timer-id] | status [timer-id] | unblock <timer-id>"
	switch {
	case len(args) == 0, len(args) == 1 && (args[0] == "--help" || args[0] == "-h"),
		len(args) == 2 && (args[1] == "--help" || args[1] == "-h"):
		fmt.Fprintln(out, usage)
		return 0
	case len(args) > 2 || args[0] != "validate" && args[0] != "status" && args[0] != "unblock" || args[0] == "unblock" && len(args) != 2:
		fmt.Fprintln(diag, usage)
		return 2
	}
	home, err := config.ResolveHome("")
	if err != nil {
		fmt.Fprintln(diag, err)
		return 1
	}
	if args[0] == "status" {
		return status(home, args[1:], out, diag)
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
