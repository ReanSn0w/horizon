package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReanSn0w/horizon/internal/bootstrap"
	"github.com/ReanSn0w/horizon/internal/config"
)

func runSkillCLI(t *testing.T, home string, args ...string) (int, string, string) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	app := New(forbiddenReader{}, &out, &diagnostics)
	code := app.Run(append([]string{"--home", home, "skills"}, args...))
	return code, out.String(), diagnostics.String()
}
func writeSkill(t *testing.T, home, id, content string) {
	t.Helper()
	dir := filepath.Join(home, "skills", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestSkillsCommandsWithoutProvider(t *testing.T) {
	home := t.TempDir()
	if _, err := bootstrap.Ensure(home); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, home, "review-id", "---\nname: review\ndescription: 'Review files'\n---\n")
	writeSkill(t, home, "broken", "---\nname: broken\ndescription: 1\n---\n")
	code, out, diagnostics := runSkillCLI(t, home, "list")
	if code != 0 || !strings.Contains(out, "review-id") || !strings.Contains(out, "ACTIVE") || !strings.Contains(diagnostics, "broken") {
		t.Fatalf("%d %s %s", code, out, diagnostics)
	}
	code, out, diagnostics = runSkillCLI(t, home, "validate", "--id", "broken")
	if code != 1 || out != "" || !strings.Contains(diagnostics, "line 3") {
		t.Fatalf("%d %s %s", code, out, diagnostics)
	}
	for _, id := range []string{"broken", "review-id"} {
		code, _, diagnostics = runSkillCLI(t, home, "disable", "--id", id)
		if code != 0 {
			t.Fatalf("%d %s", code, diagnostics)
		}
	}
	code, _, diagnostics = runSkillCLI(t, home, "enable", "--id", "broken")
	if code != 1 {
		t.Fatalf("%d %s", code, diagnostics)
	}
	code, _, diagnostics = runSkillCLI(t, home, "enable", "--id", "review-id")
	if code != 0 {
		t.Fatalf("%d %s", code, diagnostics)
	}
	ids, err := config.LoadDisabledSkills(home)
	if err != nil || len(ids) != 1 || ids[0] != "broken" {
		t.Fatalf("%v %v", ids, err)
	}
	entries, _ := os.ReadDir(filepath.Join(home, "dialogs"))
	if len(entries) != 0 {
		t.Fatal("created a session")
	}
}
func TestSkillArgumentErrorsDoNotBootstrap(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"validate"}, {"enable", "--id", "../bad"}, {"disable", "--id", "/tmp"}, {"list", "extra"}} {
		home := filepath.Join(t.TempDir(), "absent")
		code, _, _ := runSkillCLI(t, home, args...)
		if code != 2 && !(len(args) == 1 && args[0] == "--help" && code == 0) {
			t.Fatalf("%v: %d", args, code)
		}
		if _, err := os.Stat(home); !os.IsNotExist(err) {
			t.Fatalf("created home for %v", args)
		}
	}
}
