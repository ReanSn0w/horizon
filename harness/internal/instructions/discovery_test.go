package instructions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverySurvivesBrokenSkills(t *testing.T) {
	home := t.TempDir()
	for id, content := range map[string]string{"good": "---\nname: display-name\ndescription: Good\n---\nbody", "broken": "bad"} {
		dir := filepath.Join(home, "skills", id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(home, "skills", "missing"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "skills", "good"), filepath.Join(home, "skills", "link")); err != nil {
		t.Fatal(err)
	}
	entries, err := ListSkills(home)
	if err != nil || len(entries) != 3 {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
	if entries[0].ID != "broken" || entries[0].Err == nil || entries[1].Skill.Name != "display-name" || entries[2].Err == nil {
		t.Fatalf("entries=%+v", entries)
	}
	catalog, err := ScanSkills(home, "broken", "missing")
	if err != nil {
		t.Fatal(err)
	}
	skill, err := catalog.Read("display-name")
	if err != nil || skill.ID != "good" {
		t.Fatalf("skill=%+v err=%v", skill, err)
	}
	if _, err := ScanSkills(home); err == nil {
		t.Fatal("accepted broken active skill")
	}
	for _, id := range []string{"", ".", "..", "../good", "/tmp", "a/b", "a\\b"} {
		if ValidSkillID(id) {
			t.Errorf("accepted ID %q", id)
		}
	}
	if _, err := InspectSkill(home, "link"); err == nil {
		t.Fatal("followed directory symlink")
	}
}
