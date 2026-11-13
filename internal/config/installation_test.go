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

func TestWriteInstallationPreservesSourceCommentsAndNoOp(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "config.yaml")
	initial := "installation:\n  # source comment\n  source:\n    type: branch # type comment\n    branch: old # branch comment\n"
	if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	value := Installation{Source: Source{Type: "branch", Branch: "old"}}
	if err := WriteInstallation(home, value); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != initial {
		t.Fatal("no-op rewrote config")
	}
	value.Source.Branch = "new"
	if err := WriteInstallation(home, value); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	for _, comment := range []string{"source comment", "type comment", "branch comment"} {
		if !strings.Contains(string(data), comment) {
			t.Fatal("comment lost", comment)
		}
	}
}
