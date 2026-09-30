package instructions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildOrderDirectWorkspaceAndImmutableSnapshot(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	parent := filepath.Join(root, "parent")
	workspace := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(filepath.Join(home, "skills", "review", "scripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(home, "AGENTS.md"):                        "GLOBAL",
		filepath.Join(home, "SOUL.md"):                          "MUST NOT APPEAR",
		filepath.Join(parent, "AGENTS.md"):                      "PARENT MUST NOT APPEAR",
		filepath.Join(workspace, "AGENTS.md"):                   "LOCAL",
		filepath.Join(home, "skills", "review", "SKILL.md"):     "---\r\nname: review\r\ndescription: Review changes\r\n---\r\nBody line\r\n",
		filepath.Join(home, "skills", "review", "scripts", "x"): "#!/bin/sh\ntouch should-not-exist\n",
	}
	for path, contents := range files {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := Build(home, workspace, "INTRO")
	if err != nil {
		t.Fatal(err)
	}
	indices := []int{strings.Index(snapshot.Prompt, "INTRO"), strings.Index(snapshot.Prompt, "GLOBAL"), strings.Index(snapshot.Prompt, "review: Review changes"), strings.Index(snapshot.Prompt, "LOCAL")}
	for index := 1; index < len(indices); index++ {
		if indices[index-1] < 0 || indices[index] <= indices[index-1] {
			t.Fatalf("prompt order = %v\n%s", indices, snapshot.Prompt)
		}
	}
	if strings.Contains(snapshot.Prompt, "PARENT") || strings.Contains(snapshot.Prompt, "MUST NOT APPEAR") {
		t.Fatalf("prompt contains excluded instructions:\n%s", snapshot.Prompt)
	}
	skill, err := snapshot.Skills.Read("review")
	if err != nil {
		t.Fatal(err)
	}
	if skill.Content != "Body line\r\n" || !filepath.IsAbs(skill.BaseDir) {
		t.Fatalf("skill = %+v", skill)
	}
	if _, err := os.Stat(filepath.Join(skill.BaseDir, "scripts", "x")); err != nil {
		t.Fatalf("base_dir does not resolve resources: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "should-not-exist")); !os.IsNotExist(err) {
		t.Fatalf("skill scan executed a script: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("CHANGED"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill.BaseDir, "SKILL.md"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldSkill, err := snapshot.Skills.Read("review")
	if err != nil || oldSkill.Content != "Body line\r\n" || strings.Contains(snapshot.Prompt, "CHANGED") {
		t.Fatalf("snapshot changed after source edits: skill=%+v err=%v prompt=%q", oldSkill, err, snapshot.Prompt)
	}
}

func TestSkillCatalogErrorsAndSorting(t *testing.T) {
	home := t.TempDir()
	for directory, metadata := range map[string]string{
		"z": "---\nname: zebra\ndescription: Last\n---\nZ\n",
		"a": "---\nname: alpha\ndescription: First\n---\nA\n",
	} {
		path := filepath.Join(home, "skills", directory)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte(metadata), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := ScanSkills(home)
	if err != nil {
		t.Fatal(err)
	}
	summaries := catalog.Summaries()
	if len(summaries) != 2 || summaries[0].Name != "alpha" || summaries[1].Name != "zebra" {
		t.Fatalf("skill order = %+v", summaries)
	}
	if _, err := catalog.Read("missing"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing skill error = %v", err)
	}

	duplicate := filepath.Join(home, "skills", "duplicate")
	if err := os.MkdirAll(duplicate, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(duplicate, "SKILL.md"), []byte("---\nname: alpha\ndescription: Duplicate\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanSkills(home); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("duplicate skill error = %v", err)
	}
	if err := os.RemoveAll(duplicate); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "skills", "a", "SKILL.md"), []byte("no frontmatter"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanSkills(home); err == nil || !strings.Contains(err.Error(), "invalid skill") {
		t.Fatalf("invalid skill error = %v", err)
	}
}

func TestExistingInstructionPathThatCannotBeReadAsFileFails(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "AGENTS.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(home, workspace, ""); err == nil || !strings.Contains(err.Error(), "read instructions") {
		t.Fatalf("instruction read error = %v", err)
	}
}
