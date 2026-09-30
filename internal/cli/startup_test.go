package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type forbiddenReader struct{}

func (forbiddenReader) Read([]byte) (int, error) { panic("input read before configuration was ready") }

func TestFirstRunStopsBeforeInput(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		for _, mode := range []string{"text", "plain", "jsonl"} {
			t.Run(mode+map[bool]string{false: "/pipe", true: "/terminal"}[interactive], func(t *testing.T) {
				home := filepath.Join(t.TempDir(), "custom-home")
				var out, diagnostics bytes.Buffer
				editor := &testEditor{message: "never"}
				app := New(forbiddenReader{}, &out, &diagnostics)
				app.interactive = func() bool { return interactive }
				app.editor = editor
				code := app.Run([]string{"--home", home, "--mode", mode})
				if code != ExitUsage || out.Len() != 0 || editor.calls != 0 {
					t.Fatalf("code=%d out=%q editor=%d err=%q", code, out.String(), editor.calls, diagnostics.String())
				}
				for _, want := range []string{filepath.Join(home, "config.yaml"), "provider.url", "provider.key", "models.chatting.model", "compact_threshold"} {
					if !strings.Contains(diagnostics.String(), want) {
						t.Fatalf("missing %q: %s", want, diagnostics.String())
					}
				}
				entries, err := os.ReadDir(filepath.Join(home, "dialogs"))
				if err != nil || len(entries) != 0 {
					t.Fatalf("session state created: %v %v", entries, err)
				}
			})
		}
	}
}

func TestHelpAndArgumentErrorsDoNotInitialize(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"resume", "--help"}, {"sessions", "read", "--help"}, {"unknown"}, {"--bad"}, {"-m", "x", "extra"}, {"sessions", "read"}} {
		home := filepath.Join(t.TempDir(), "absent")
		code, _, _ := runApp(t, "", append([]string{"--home", home}, args...)...)
		if code != ExitUsage && code != ExitOK {
			t.Fatalf("%q: code=%d", args, code)
		}
		if _, err := os.Stat(home); !os.IsNotExist(err) {
			t.Fatalf("%q created home: %v", args, err)
		}
	}
}
