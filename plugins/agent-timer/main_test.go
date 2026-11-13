package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestAdminEnableDisableChecksAccess(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	t.Setenv("HORIZON_HOME", home)
	if err := os.Mkdir(filepath.Join(home, "timers"), 0700); err != nil {
		t.Fatal(err)
	}
	data := "---\nname: Test\ndescription: Test schedule\nworkspace: " + workspace + "\nenabled: false\nschedule:\n  every: 1h\n  timezone: UTC\n---\nTask\n"
	path := filepath.Join(home, "timers", "test.md")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diag bytes.Buffer
	t.Setenv("HORIZON_INHERITED_ACCESS", "read")
	if code := run(context.Background(), []string{"agent-admin", "enable", "test"}, &out, &diag, time.Now); code == 0 {
		t.Fatal("read access enabled timer")
	}
	t.Setenv("HORIZON_INHERITED_ACCESS", "write")
	out.Reset()
	diag.Reset()
	if code := run(context.Background(), []string{"agent-admin", "enable", "test"}, &out, &diag, time.Now); code != 0 {
		t.Fatalf("enable: %d %q", code, diag.String())
	}
	out.Reset()
	diag.Reset()
	if code := run(context.Background(), []string{"agent-admin", "disable", "test"}, &out, &diag, time.Now); code != 0 {
		t.Fatalf("disable: %d %q", code, diag.String())
	}
}
