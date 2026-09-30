package instructions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDisabledSkillsAndManagementSnapshot(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	dir := filepath.Join(home, "skills", "broken")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	opts := Options{DisabledSkills: []string{"broken"}, Executable: "/tmp/a ' b/horizon"}
	snapshot, err := Build(home, workspace, "INTRO", opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Skills.Summaries()) != 0 {
		t.Fatal("disabled skill in catalog")
	}
	if _, err := snapshot.Skills.Read("broken"); err == nil {
		t.Fatal("read disabled skill")
	}
	for _, want := range []string{"skills validate --id", "--home '" + home + "'", `'/tmp/a '"'"' b/horizon'`} {
		if !strings.Contains(snapshot.Prompt, want) {
			t.Fatalf("missing %q in %s", want, snapshot.Prompt)
		}
	}
	if _, err := Build(home, workspace, "INTRO"); err == nil {
		t.Fatal("accepted active broken skill")
	}
}
