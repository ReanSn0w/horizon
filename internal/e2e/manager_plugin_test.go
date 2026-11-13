package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltManagerDelegationWithoutModelConfiguration(t *testing.T) {
	binary := buildBinary(t)
	home := t.TempDir()
	os.Mkdir(filepath.Join(home, "bin"), 0700)
	build := exec.Command("go", "build", "-o", filepath.Join(home, "bin/horizon-manager"), "./plugins/manager")
	build.Dir = "../.."
	if data, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, data)
	}
	// Help and metadata have no initialization side effects.
	if out, diag, err := run(binary, t.TempDir(), "", "--home", home, "manager", "--help"); err != nil || diag != "" || (!strings.Contains(out, "horizon-manager") || !strings.Contains(out, "Install Horizon")) {
		t.Fatal(out, diag, err)
	}
	if _, err := os.Stat(filepath.Join(home, "config.yaml")); !os.IsNotExist(err) {
		t.Fatal("help created config")
	}
	os.Mkdir(filepath.Join(home, ".manager"), 0700)
	for _, name := range []string{"manager", "browser", "telegram", "agent-telegram"} {
		dir := filepath.Join(home, "source/plugins", name)
		os.MkdirAll(dir, 0700)
		data, err := os.ReadFile("../../plugins/" + name + "/plugin.json")
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, "plugin.json"), data, 0600)
	}
	os.WriteFile(filepath.Join(home, "config.yaml"), []byte("installation:\n  source:\n    type: release\nagent_plugins: []\n"), 0600)
	os.WriteFile(filepath.Join(home, ".manager/state.json"), []byte(`{"version":1,"commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ref":"refs/tags/v1","source":{"type":"release"},"plugins":[]}`), 0600)
	out, diag, err := run(binary, t.TempDir(), "", "--home", home, "manager", "plugins", "list")
	if err != nil || diag != "" || !strings.Contains(strings.Join(strings.Fields(out), " "), "manager true true false") {
		t.Fatal(out, diag, err)
	}
}
