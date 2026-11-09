package cli

import (
	"bytes"
	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/manager"
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
