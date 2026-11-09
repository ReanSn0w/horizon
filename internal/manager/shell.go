package manager

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
func fishQuote(value string) string {
	return "'" + strings.NewReplacer("\\", "\\\\", "'", "\\'").Replace(value) + "'"
}
func shellCommands(shell, home string) string {
	if shell == "fish" {
		return "set -gx HORIZON_HOME " + fishQuote(home) + "\nif not contains -- \"$HORIZON_HOME/bin\" $PATH\n    set -gx PATH \"$HORIZON_HOME/bin\" $PATH\nend\n"
	}
	return "export HORIZON_HOME=" + shellQuote(home) + "\ncase \":$PATH:\" in\n  *\":$HORIZON_HOME/bin:\"*) ;;\n  *) export PATH=\"$HORIZON_HOME/bin:$PATH\" ;;\nesac\n"
}

// printShellSetup never opens or changes the user's startup files.
func printShellSetup(o Options, out io.Writer) {
	shell := o.Shell
	if shell == "" {
		shell = filepath.Base(os.Getenv("SHELL"))
	}
	if shell != "bash" && shell != "zsh" && shell != "fish" {
		shell = "zsh"
	}
	fmt.Fprintf(out, "Add these commands to your %s configuration, or run them in the current terminal:\n%s", shell, shellCommands(shell, o.Home))
}
