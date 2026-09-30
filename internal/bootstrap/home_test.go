package bootstrap

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestEnsureCreatesAndPreservesHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	result, err := Ensure(home)
	if err != nil || !result.ConfigCreated {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, name := range []string{"", "dialogs", "plugins", "skills", "skills/skill-creator", "skills/filesystem", "config.yaml", "AGENTS.md", "skills/skill-creator/SKILL.md", "skills/filesystem/SKILL.md"} {
		info, err := os.Stat(filepath.Join(home, name))
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0600)
		if info.IsDir() {
			want = 0700
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode=%v", name, info.Mode())
		}
	}
	custom := []byte("user content")
	pluginDir := filepath.Join(home, "plugins")
	if err := os.WriteFile(filepath.Join(pluginDir, "horizon-personal"), custom, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(pluginDir, 0750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.yaml", "AGENTS.md", "skills/skill-creator/SKILL.md", "skills/filesystem/SKILL.md"} {
		if err := os.WriteFile(filepath.Join(home, name), custom, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(home, "dialogs")); err != nil {
		t.Fatal(err)
	}
	result, err = Ensure(home)
	if err != nil || result.ConfigCreated {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if info, err := os.Stat(pluginDir); err != nil || info.Mode().Perm() != 0750 {
		t.Fatalf("plugin directory permissions: %v %v", info, err)
	}
	if data, err := os.ReadFile(filepath.Join(pluginDir, "horizon-personal")); err != nil || !bytes.Equal(data, custom) {
		t.Fatalf("installed plugin changed: %q %v", data, err)
	}
	for _, name := range []string{"config.yaml", "AGENTS.md", "skills/skill-creator/SKILL.md", "skills/filesystem/SKILL.md"} {
		data, err := os.ReadFile(filepath.Join(home, name))
		if err != nil || !bytes.Equal(data, custom) {
			t.Fatalf("%s: %q %v", name, data, err)
		}
	}
	if err := os.Remove(filepath.Join(home, "skills", "skill-creator", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(home); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(home, "skills", "skill-creator", "SKILL.md"))
	if !bytes.Equal(data, skillTemplate) {
		t.Fatal("missing skill not restored")
	}
}

func TestEnsureConcurrent(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	var wg sync.WaitGroup
	results := make(chan Result, 20)
	for range 20 {
		wg.Go(func() {
			result, err := Ensure(home)
			if err != nil {
				t.Error(err)
			}
			results <- result
		})
	}
	wg.Wait()
	close(results)
	created := 0
	for result := range results {
		if result.ConfigCreated {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("config created %d times", created)
	}
	for name, want := range map[string][]byte{"config.yaml": configTemplate, "AGENTS.md": {}, "skills/skill-creator/SKILL.md": skillTemplate, "skills/filesystem/SKILL.md": filesystemSkillTemplate} {
		data, err := os.ReadFile(filepath.Join(home, name))
		if err != nil || !bytes.Equal(data, want) {
			t.Fatalf("%s: %v, complete=%t", name, err, bytes.Equal(data, want))
		}
	}
	matches, _ := filepath.Glob(filepath.Join(home, ".horizon-init-*"))
	if len(matches) != 0 {
		t.Fatalf("temporary files=%v", matches)
	}
}

func TestEnsureRejectsConflicts(t *testing.T) {
	for _, name := range []string{"dialogs", "skills", "config.yaml", "AGENTS.md", "skills/skill-creator/SKILL.md", "skills/filesystem/SKILL.md"} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, name)
			if name == "config.yaml" || name == "AGENTS.md" || name == "skills/skill-creator/SKILL.md" || name == "skills/filesystem/SKILL.md" {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Ensure(home); err == nil {
				t.Fatal("expected type conflict")
			}
			if name == "dialogs" || name == "skills" {
				data, _ := os.ReadFile(path)
				if string(data) != "keep" {
					t.Fatal("conflicting file changed")
				}
			}
		})
	}
}

func TestEnsurePermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permissions")
	}
	home := t.TempDir()
	if err := os.Chmod(home, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(home, 0700)
	if _, err := Ensure(home); err == nil {
		t.Fatal("expected permission error")
	}
}
