package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ReanSn0w/horizon/internal/buildinfo"
	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/daemon"
	"github.com/ReanSn0w/horizon/internal/timer"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, time.Now))
}

func run(args []string, out, diag io.Writer, now func() time.Time) int {
	if len(args) != 0 {
		switch args[0] {
		case "agent-plugin-metadata":
			if len(args) == 1 {
				err := json.NewEncoder(out).Encode(daemon.AdapterMetadata{
					ProtocolVersion: 1,
					Version:         buildinfo.Version,
					Description:     "Markdown timer schedules",
					Capabilities:    []string{daemon.AdapterAdminCLI},
				})
				if err != nil {
					fmt.Fprintln(diag, err)
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
	const usage = "Usage: horizon agent timer validate [timer-id]"
	switch {
	case len(args) == 0, len(args) == 1 && (args[0] == "--help" || args[0] == "-h"),
		len(args) == 2 && args[0] == "validate" && (args[1] == "--help" || args[1] == "-h"):
		fmt.Fprintln(out, usage)
		return 0
	case len(args) > 2 || args[0] != "validate":
		fmt.Fprintln(diag, usage)
		return 2
	}
	home, err := config.ResolveHome("")
	if err != nil {
		fmt.Fprintln(diag, err)
		return 1
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
