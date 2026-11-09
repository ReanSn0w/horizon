package manager

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReanSn0w/horizon/internal/config"
)

func TestStateSeparatesInstalledRevision(t *testing.T) {
	home := t.TempDir()
	s := State{Version: 1, Commit: strings.Repeat("a", 40), Ref: "main", Source: config.Source{Type: "branch", Branch: "main"}, Plugins: []string{"browser"}}
	if err := writeJSON(filepath.Join(home, ".manager", "state.json"), s); err != nil {
		t.Fatal(err)
	}
	got, err := ReadState(home)
	if err != nil || got.Commit != s.Commit || len(got.Plugins) != 1 {
		t.Fatal(got, err)
	}
	s.Plugins = []string{"../escape"}
	_ = writeJSON(filepath.Join(home, ".manager", "state.json"), s)
	if _, err := ReadState(home); err == nil {
		t.Fatal("accepted invalid state")
	}
}
