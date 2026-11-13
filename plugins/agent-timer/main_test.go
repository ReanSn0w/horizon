package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListIncludesInvalidAndDisabledWithoutDaemon(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	var out, diag bytes.Buffer
	if code := list(home, true, &out, &diag); code != 0 || out.String() != "[]\n" {
		t.Fatalf("empty list: %d %q %q", code, out.String(), diag.String())
	}
	if err := os.Mkdir(filepath.Join(home, "timers"), 0700); err != nil {
		t.Fatal(err)
	}
	valid := "---\nname: Test\ndescription: Test schedule\nworkspace: " + workspace + "\nenabled: false\nschedule:\n  every: 1h\n  timezone: UTC\n---\nTask\n"
	if err := os.WriteFile(filepath.Join(home, "timers", "zeta.md"), []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "timers", "alpha.md"), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := list(home, true, &out, &diag); code != 0 {
		t.Fatalf("list: %d %q", code, diag.String())
	}
	var rows []struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 2 || rows[0].ID != "alpha" || rows[0].Error == "" || rows[1].ID != "zeta" || rows[1].Enabled {
		t.Fatalf("rows: %+v %v", rows, err)
	}
	out.Reset()
	if code := list(home, false, &out, &diag); code != 0 || !strings.Contains(out.String(), "WORKSPACE") {
		t.Fatalf("table: %d %q", code, out.String())
	}
}
