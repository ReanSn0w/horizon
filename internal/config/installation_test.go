package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallationWithoutModelCredentials(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(target, []byte("# preserve\nmode: unit\nproviders: {}\n"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, "config.yaml")); err != nil {
		t.Fatal(err)
	}
	value := Installation{Source: Source{Type: "branch", Branch: "codex/repository-refactoring"}}
	if err := WriteInstallation(home, value); err != nil {
		t.Fatal(err)
	}
	got, err := ReadInstallation(home)
	if err != nil || got != value {
		t.Fatal(got, err)
	}
	data, _ := os.ReadFile(target)
	info, _ := os.Stat(target)
	link, _ := os.Lstat(filepath.Join(home, "config.yaml"))
	if !strings.Contains(string(data), "# preserve") || info.Mode().Perm() != 0640 || link.Mode()&os.ModeSymlink == 0 {
		t.Fatal("configuration not preserved")
	}
	for _, s := range []Source{{}, {Type: "branch"}, {Type: "release", Branch: "main"}, {Type: "other"}} {
		if s.Validate() == nil {
			t.Fatal("accepted invalid source", s)
		}
	}
}
