package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalHelp(t *testing.T) {
	t.Setenv("HORIZON_HOME", filepath.Join(t.TempDir(), "missing"))
	for _, args := range [][]string{{}, {"--help"}, {"start", "--help"}, {"send", "--help"}, {"list", "--help"}, {"service", "--help"}, {"horizon-plugin-metadata"}} {
		var out, diag bytes.Buffer
		if code := runCLI(context.Background(), args, &out, &diag); code != 0 || out.Len() == 0 || diag.Len() != 0 {
			t.Fatalf("%v: %d %s", args, code, &diag)
		}
		if len(args) == 0 && !strings.Contains(out.String(), "    telegram:\n      bot_token:") {
			t.Fatal("help lost example YAML indentation")
		}
	}
	var out, diag bytes.Buffer
	if code := runCLI(context.Background(), []string{"send"}, &out, &diag); code != 2 || !strings.Contains(diag.String(), "chat") {
		t.Fatalf("%d %s", code, &diag)
	}
}
