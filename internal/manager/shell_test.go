package manager

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestShellInstructionsNeverChangeFiles(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	for _, shell := range []string{"bash", "zsh", "fish"} {
		var out bytes.Buffer
		printShellSetup(Options{Home: "/tmp/custom home", Shell: shell}, &out)
		if !strings.Contains(out.String(), "HORIZON_HOME") || !strings.Contains(out.String(), "PATH") || !strings.Contains(out.String(), shell) {
			t.Fatal(out.String())
		}
	}
	files, err := os.ReadDir(userHome)
	if err != nil || len(files) != 0 {
		t.Fatal("shell setup changed user home", files, err)
	}
}
func TestShellCommandsQuotePathsAndAvoidDuplicatePATH(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			executable, err := exec.LookPath(shell)
			if err != nil {
				t.Skip(shell + " is unavailable")
			}
			home := `/tmp/space ' $HOME $(false) \\ final`
			script := shellCommands(shell, home) + shellCommands(shell, home)
			if shell == "fish" {
				script += "printf '%s\\n' \"$HORIZON_HOME\"; string join : $PATH"
			} else {
				script += "printf '%s\\n%s' \"$HORIZON_HOME\" \"$PATH\""
			}
			path := filepath.Join(t.TempDir(), "setup")
			if err := os.WriteFile(path, []byte(script), 0600); err != nil {
				t.Fatal(err)
			}
			data, err := exec.Command(executable, path).Output()
			if err != nil {
				t.Fatal(err)
			}
			parts := strings.SplitN(string(data), "\n", 2)
			if len(parts) != 2 || parts[0] != home || strings.Count(parts[1], home+"/bin") != 1 {
				t.Fatalf("bad environment %q", data)
			}
		})
	}
}
