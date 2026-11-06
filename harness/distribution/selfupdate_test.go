package distribution

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestReplaceExecutingUpdater(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	path := filepath.Join(home, "plugins", "horizon-update")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(path, "-test.run=^TestExecutingUpdaterHelper$")
	command.Env = append(os.Environ(), "HORIZON_DISTRIBUTION_SELF_TEST="+root)
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	replaced, err := os.ReadFile(path)
	if err != nil || string(replaced) != "update" {
		t.Fatalf("update replacement: %q %v", replaced, err)
	}
}
func TestExecutingUpdaterHelper(t *testing.T) {
	root := os.Getenv("HORIZON_DISTRIBUTION_SELF_TEST")
	if root == "" {
		t.Skip("child only")
	}
	m, err := OpenManager(filepath.Join(root, "home"), filepath.Join(root, "bin", "horizon"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	manifest, _, archive := fixture(t, "v0.1.0")
	b, err := Extract(bytes.NewReader(archive), manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err = m.Install(context.Background(), b, nil, "linux_amd64", true); err != nil {
		t.Fatal(err)
	}
}
