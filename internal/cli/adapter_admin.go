package cli

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/ReanSn0w/horizon/internal/daemon"
	"github.com/ReanSn0w/horizon/internal/maintenance"
	"github.com/ReanSn0w/horizon/internal/plugins"
)

const adapterAdminUsage = "Usage: horizon agent <adapter> [arguments...]"

type adapterAdminCommand struct{}

func (*adapterAdminCommand) Execute([]string) error { return usage(adapterAdminUsage, nil) }

func (a *App) runAdapterAdmin(home string, args []string) int {
	if len(args) == 0 || slices.Contains([]string{"start", "status", "stop", "resolve-input", "service", "--help", "-h"}, args[0]) {
		entry := plugins.Inspect(home, "agent", nil)
		if entry.Err == nil {
			return a.runPlugin(entry, args, home)
		}
		if len(args) > 0 && args[0] != "--help" && args[0] != "-h" {
			fmt.Fprintf(a.errOut, "horizon: install the supervisor with horizon manager plugins install agent: %v\n", entry.Err)
			return ExitUsage
		}
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(a.out, adapterAdminUsage)
		return ExitOK
	}
	if len(args) == 0 {
		fmt.Fprintln(a.errOut, adapterAdminUsage)
		return ExitUsage
	}
	if _, err := effectiveAccess(""); err != nil {
		fmt.Fprintf(a.errOut, "horizon: %v\n", err)
		return ExitUsage
	}
	release, err := maintenance.AcquireReadOnly(home)
	if err != nil {
		fmt.Fprintf(a.errOut, "horizon: %v\n", err)
		return ExitFailure
	}
	defer release()

	name := args[0]
	path, err := daemon.AdapterPath(home, name)
	if err != nil {
		fmt.Fprintf(a.errOut, "horizon: adapter %q unavailable: %v\n", name, err)
		return ExitUsage
	}
	metadata, err := daemon.AdapterInspect(context.Background(), home, name)
	if err != nil {
		fmt.Fprintf(a.errOut, "horizon: adapter %q metadata: %v\n", name, err)
		return ExitUsage
	}
	if !slices.Contains(metadata.Capabilities, daemon.AdapterAdminCLI) {
		fmt.Fprintf(a.errOut, "horizon: adapter %q does not expose administrative commands\n", name)
		return ExitUsage
	}
	env := slices.DeleteFunc(os.Environ(), func(item string) bool {
		return strings.HasPrefix(item, "HORIZON_ADAPTER_") || strings.HasPrefix(item, "HORIZON_DAEMON_SOCKET=")
	})
	return a.runExternal(plugins.Entry{Name: "agent " + name, Path: path}, append([]string{"agent-admin"}, args[1:]...), home, env)
}
