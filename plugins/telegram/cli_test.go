package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIHelpAndValidationBeforeConfiguration(t *testing.T) {
	home := filepath.Join(t.TempDir(), "absent")
	t.Setenv("HORIZON_HOME", home)
	for _, args := range [][]string{{"--help"}, {"send", "--help"}, {"read", "--help"}, {"migrate", "--help"}} {
		var out bytes.Buffer
		if code := cli(context.Background(), args, &out, &out); code != 0 || !strings.Contains(out.String(), "Usage:") {
			t.Fatal(args, code, out.String())
		}
	}
	for _, args := range [][]string{{"send"}, {"send", ""}, {"check", "extra"}, {"read"}, {"migrate"}, {"migrate", "--check", "--apply"}} {
		var out bytes.Buffer
		if code := cli(context.Background(), args, &out, &out); code != 2 {
			t.Fatal(args, code, out.String())
		}
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("created home")
	}
}
