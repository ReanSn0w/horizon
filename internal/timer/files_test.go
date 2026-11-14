package timer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func markdown(workspace, schedule, body string) string {
	return fmt.Sprintf("---\nname: Report\ndescription: Inspect local reports\nworkspace: %q\nschedule:\n%s\n---\n%s\n", workspace, schedule, body)
}

func putFile(t *testing.T, home, id, text string) {
	t.Helper()
	dir := filepath.Join(home, "timers")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".md"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

const minutely = "  every: 1m\n  timezone: UTC"

func TestTimerFileContract(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	linked := filepath.Join(home, "linked-workspace")
	if err := os.Symlink(workspace, linked); err != nil {
		t.Fatal(err)
	}
	expected, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	content := markdown(linked, minutely, "Inspect the reports.")
	putFile(t, home, "reports", content)
	putFile(t, home, "renamed", content)
	putFile(t, home, "broken", "missing frontmatter")
	putFile(t, home, "disabled", strings.Replace(content, "schedule:", "enabled: false\nschedule:", 1))
	files, err := LoadCatalog(home)
	if err != nil || len(files) != 4 || files[0].ID != "broken" || files[0].Err == nil {
		t.Fatalf("catalog = %+v, %v", files, err)
	}
	first, err := LoadFile(home, "reports")
	if err != nil || first.Workspace != expected || !first.Enabled || first.Message != "Inspect the reports." {
		t.Fatalf("definition = %+v, %v", first, err)
	}
	renamed, err := LoadFile(home, "renamed")
	if err != nil || renamed.ID == first.ID || renamed.Revision != first.Revision {
		t.Fatalf("renamed = %+v, %v", renamed, err)
	}
	disabled, err := LoadFile(home, "disabled")
	if err != nil || disabled.Enabled {
		t.Fatalf("disabled = %+v, %v", disabled, err)
	}
	putFile(t, home, "reports", strings.Replace(content, "Inspect the reports.", "Update the reports.", 1))
	updated, err := LoadFile(home, "reports")
	if err != nil || updated.ID != first.ID || updated.Revision == first.Revision {
		t.Fatalf("updated = %+v, %v", updated, err)
	}
	putFile(t, home, "indented", markdown(workspace, minutely, "    preserve Markdown indentation"))
	indented, err := LoadFile(home, "indented")
	if err != nil || indented.Message != "    preserve Markdown indentation" {
		t.Fatalf("Markdown indentation lost: %q %v", indented.Message, err)
	}
}

func TestTimerFileErrors(t *testing.T) {
	workspace := t.TempDir()
	base := markdown(workspace, minutely, "Do the job.")
	for _, tc := range []struct{ old, replacement, error string }{
		{"name: Report", "name: ", "name is required"},
		{"description: Inspect local reports", "description: ", "description is required"},
		{fmt.Sprintf("%q", workspace), "relative", "absolute"},
		{fmt.Sprintf("%q", workspace), filepath.Join(workspace, "missing"), "workspace:"},
		{"schedule:", "enabled: 'true'\nschedule:", "enabled must be a boolean"},
		{"schedule:", "enabled: null\nschedule:", "enabled must be a boolean"},
		{"schedule:", "chat_id: 123\nschedule:", "chat_id"},
		{"schedule:", "session_id: existing\nschedule:", "session_id"},
		{"schedule:", "name: Duplicate\nschedule:", "already defined"},
		{"  every: 1m", "  every: 1m\n  every: 2m", "already defined"},
		{"  every: 1m", "  every: ''\n  cron: '* * * * *'", "schedule.every must be a nonempty string"},
		{"  every: 1m", "  something: unknown", "something"},
		{"  every: 1m\n  timezone: UTC", "", "schedule is required"},
		{"Do the job.", "   ", "Markdown task is required"},
	} {
		t.Run(tc.error+"/"+tc.replacement, func(t *testing.T) {
			_, err := parseFile(t.TempDir(), "test", []byte(strings.Replace(base, tc.old, tc.replacement, 1)))
			if err == nil || !strings.Contains(err.Error(), tc.error) {
				t.Fatalf("wanted %q, got %v", tc.error, err)
			}
		})
	}
}

func TestCatalogRejectsSymlinksAndOversizedFiles(t *testing.T) {
	home, outside := t.TempDir(), t.TempDir()
	putFile(t, home, "large", strings.Repeat("a", maxFileSize+1))
	if err := os.Symlink(filepath.Join(outside, "target.md"), filepath.Join(home, "timers", "linked.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(home, "large"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatal(err)
	}
	if _, err := LoadFile(home, "linked"); err == nil {
		t.Fatal("accepted a symlink timer")
	}
	if _, err := LoadFile(home, "../escape"); err == nil {
		t.Fatal("accepted path traversal")
	}
	other := t.TempDir()
	if err := os.Symlink(filepath.Join(home, "timers"), filepath.Join(other, "timers")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCatalog(other); err == nil {
		t.Fatal("accepted a symlink catalog")
	}
}

func TestDefaultWorkspaceUsesFileIDWithoutCreatingDirectory(t *testing.T) {
	home := t.TempDir()
	linked := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(home, linked); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: Arbitrary title\ndescription: Job\nschedule:\n" + minutely + "\n---\nDo work.\n"
	for _, id := range []string{"nap", "different"} {
		putFile(t, home, id, body)
		def, err := LoadFile(linked, id)
		real, _ := filepath.EvalSymlinks(home)
		if err != nil || !def.DefaultWorkspace || def.Workspace != filepath.Join(real, "workspace", id) {
			t.Fatalf("%+v %v", def, err)
		}
	}
	if _, err := LoadCatalog(linked); err != nil {
		t.Fatal(err)
	}
	if _, err := SetEnabled(linked, "nap", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "workspace")); !os.IsNotExist(err) {
		t.Fatal("loader/editor created workspace", err)
	}
	for _, value := range []string{"", "null", "42", "true", "''", "relative"} {
		putFile(t, home, "invalid", strings.Replace(body, "schedule:", "workspace: "+value+"\nschedule:", 1))
		if _, err := LoadFile(home, "invalid"); err == nil {
			t.Fatal("accepted", value)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "workspace"), []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(home, "nap"); err == nil {
		t.Fatal("accepted file parent")
	}
}

func TestDefaultWorkspaceExistingSymlinkAndTitleChange(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	if err := os.Symlink(workspace, filepath.Join(home, "workspace")); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: One\ndescription: Job\nschedule:\n" + minutely + "\n---\nWork.\n"
	putFile(t, home, "nap", body)
	first, err := LoadFile(home, "nap")
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(workspace)
	if first.Workspace != filepath.Join(real, "nap") {
		t.Fatal(first.Workspace)
	}
	putFile(t, home, "nap", strings.Replace(body, "name: One", "name: Two", 1))
	second, err := LoadFile(home, "nap")
	if err != nil || first.Workspace != second.Workspace {
		t.Fatal(second, err)
	}
}

func TestNapExampleIsValidDisabledAndUsesDefaultWorkspace(t *testing.T) {
	data, err := os.ReadFile("../../examples/timers/nap.md")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	putFile(t, home, "nap", string(data))
	def, err := LoadFile(home, "nap")
	if err != nil {
		t.Fatal(err)
	}
	if def.Enabled || !def.DefaultWorkspace {
		t.Fatal("nap example must be disabled and use its default workspace")
	}
	if _, err := Compile(def.Schedule); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "workspace")); !os.IsNotExist(err) {
		t.Fatal("example validation created workspace")
	}
}
