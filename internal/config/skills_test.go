package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestDisabledSkillConfig(t *testing.T) {
	for _, tc := range []struct {
		value string
		bad   bool
	}{
		{"", false}, {"disabled_skills: []", false}, {"disabled_skills: [a, a, b]", false},
		{"disabled_skills: null", true}, {"disabled_skills: hello", true}, {"disabled_skills: [1]", true}, {"disabled_skills: ['../x']", true},
		{"unknown: true", true}, {"disabled_skills: []\n---\ndisabled_skills: []", true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			home := t.TempDir()
			os.WriteFile(filepath.Join(home, "config.yaml"), []byte("mode: unit\n"+tc.value), 0600)
			ids, err := LoadDisabledSkills(home)
			if (err != nil) != tc.bad {
				t.Fatalf("ids=%v err=%v", ids, err)
			}
			if tc.value == "disabled_skills: [a, a, b]" && !reflect.DeepEqual(ids, []string{"a", "b"}) {
				t.Fatal(ids)
			}
		})
	}
}

func TestSkillUpdatesPreserveConfigAndSymlink(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(t.TempDir(), "real.yaml")
	path := filepath.Join(home, "config.yaml")
	original := "# keep this comment\nmode: unit\nprovider:\n  url: ''\n  key: '' # private field\n"
	if err := os.WriteFile(target, []byte(original), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	changed, err := UpdateDisabledSkills(home, "a", true, nil)
	if err != nil || !changed {
		t.Fatalf("%v %v", changed, err)
	}
	data, _ := os.ReadFile(target)
	for _, want := range []string{"# keep this comment", "# private field", "disabled_skills:"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %s", want)
		}
	}
	info, _ := os.Lstat(path)
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("replaced link")
	}
	info, _ = os.Stat(target)
	if info.Mode().Perm() != 0640 {
		t.Fatal("changed permissions")
	}
	changed, err = UpdateDisabledSkills(home, "a", true, nil)
	again, _ := os.ReadFile(target)
	if err != nil || changed || string(data) != string(again) {
		t.Fatal("non-idempotent update")
	}
	sentinel := errors.New("invalid skill")
	_, err = UpdateDisabledSkills(home, "a", false, func([]string) error { return sentinel })
	again, _ = os.ReadFile(target)
	if !errors.Is(err, sentinel) || string(data) != string(again) {
		t.Fatal("failed validation changed config")
	}
	if _, err := UpdateDisabledSkills(home, "a", false, nil); err != nil {
		t.Fatal(err)
	}
	ids, err := LoadDisabledSkills(home)
	if err != nil || len(ids) != 0 {
		t.Fatalf("%v %v", ids, err)
	}
}

func TestConcurrentDisabledSkillUpdates(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "config.yaml"), []byte("mode: unit\n"), 0600)
	var wg sync.WaitGroup
	for _, id := range []string{"a", "b", "c", "d"} {
		wg.Go(func() {
			if _, err := UpdateDisabledSkills(home, id, true, nil); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	ids, err := LoadDisabledSkills(home)
	slices.Sort(ids)
	if err != nil || !reflect.DeepEqual(ids, []string{"a", "b", "c", "d"}) {
		t.Fatalf("%v %v", ids, err)
	}
}
