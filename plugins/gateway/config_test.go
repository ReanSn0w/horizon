package main

import (
	"github.com/ReanSn0w/horizon/internal/config"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func configHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("# keep\nmode: unit\ndisabled_skills: []\nplugins:\n  other:\n    custom: value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := initSettings(home); err != nil {
		t.Fatal(err)
	}
	return home
}
func TestConfigPreservation(t *testing.T) {
	home := configHome(t)
	before, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
	if changed, err := initSettings(home); err != nil || changed {
		t.Fatalf("%t %v", changed, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := ensureGroup(home, -123); err != nil {
				t.Error(err)
			}
			if _, err := config.UpdateDisabledSkills(home, "test", true, nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	data, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
	if !strings.Contains(string(data), "# keep") || !strings.Contains(string(data), "custom: value") {
		t.Fatal("lost unrelated settings")
	}
	s, err := loadSettings(home, false)
	if err != nil || !s.Groups["-123"].OwnerOnly || s.GroupAccess != "read" {
		t.Fatalf("%+v %v", s, err)
	}
	if !strings.Contains(string(before), "owner_user_id: 0") {
		t.Fatal("incorrect defaults")
	}
	target := filepath.Join(home, "config.yaml")
	alias := t.TempDir()
	if err := os.Symlink(target, filepath.Join(alias, "config.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureGroup(alias, -456); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(filepath.Join(alias, "config.yaml")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("replaced symlink")
	}
}
func TestConfigValidation(t *testing.T) {
	home := configHome(t)
	if _, err := loadSettings(home, true); err == nil {
		t.Fatal("accepted missing credentials")
	}
	p := filepath.Join(home, "config.yaml")
	data, _ := os.ReadFile(p)
	data = append(data, []byte("\ninvalid: [\n")...)
	os.WriteFile(p, data, 0600)
	if _, err := loadSettings(home, false); err == nil {
		t.Fatal("accepted invalid YAML")
	}
	s := defaults(home)
	t.Setenv("HORIZON_INHERITED_ACCESS", "write")
	if checkInherited(s) == nil {
		t.Fatal("inherited mode overrides group read")
	}
}
