package cli

import (
	"bytes"
	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/maintenance"
	"github.com/ReanSn0w/horizon/internal/manager"
	"os"
	"path/filepath"
	"testing"
)

// Resource fixtures now use the manager; the removed CLI command is never intercepted.
func runInitialize(t *testing.T, _ string, args ...string) (int, string, string) {
	t.Helper()
	home := ""
	for i, a := range args {
		if a == "--home" && i+1 < len(args) {
			home = args[i+1]
		}
	}
	home, err := config.ResolveHome(home)
	if err != nil {
		t.Fatal(err)
	}
	var out, diag bytes.Buffer
	err = manager.Initialize(home, &out, &diag)
	if err != nil {
		return 1, out.String(), diag.String() + err.Error()
	}
	return 0, out.String(), diag.String()
}
func TestInitCommandRemoved(t *testing.T) {
	code, _, _ := runApp(t, "", "--home", t.TempDir(), "init")
	if code != ExitUsage {
		t.Fatal(code)
	}
}

func TestManagerRecoveryBypassesConfigAndRuntimeGate(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".manager"), 0700); err != nil {
		t.Fatal(err)
	}
	pluginScript(t, home, "manager", "exit 17\n")
	release, err := maintenance.Acquire(home, true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	code, _, diag := runApp(t, "", "--home", home, "manager", "update")
	if code != 17 {
		t.Fatalf("manager recovery: %d %s", code, diag)
	}
}
