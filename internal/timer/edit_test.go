package timer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetEnabledPreservesOtherContentAndMode(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	content := "---\n# keep comment\nname: Test\ndescription: Test task\nworkspace: " + workspace + "\nenabled: false # existing comment\nschedule:\n  every: 1h\n  timezone: UTC\n---\n# body\nDo the task.\n"
	putFile(t, home, "test", content)
	path := filepath.Join(home, "timers", "test.md")
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	changed, err := SetEnabled(home, "test", true)
	if err != nil || !changed {
		t.Fatalf("enable: %v %t", err, changed)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	expected := strings.Replace(content, "enabled: false # existing comment", "enabled: true # existing comment", 1)
	if string(data) != expected {
		t.Fatalf("content changed unexpectedly: %q", data)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("mode: %v %v", info, err)
	}
	before := info.ModTime()
	changed, err = SetEnabled(home, "test", true)
	if err != nil || changed {
		t.Fatalf("repeat: %v %t", err, changed)
	}
	info, err = os.Stat(path)
	if err != nil || !info.ModTime().Equal(before) {
		t.Fatal("idempotent edit rewrote file")
	}
	if changed, err := SetEnabled(home, "test", false); err != nil || !changed {
		t.Fatalf("disable: %v %t", err, changed)
	}
}

func TestSetEnabledRejectsInvalidAndSymlink(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	putFile(t, home, "valid", markdown(workspace, minutely, "Task"))
	if err := os.Symlink(filepath.Join(home, "timers", "valid.md"), filepath.Join(home, "timers", "linked.md")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"linked", "missing", "../escape"} {
		if _, err := SetEnabled(home, id, false); err == nil {
			t.Fatalf("accepted %s", id)
		}
	}
}
