package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	builtinmemory "github.com/ReanSn0w/horizon/internal/memory"
)

func TestMemoryCLIAndInheritedAccess(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	configuration := `mode: unit
default_model: test
models:
  test:
    provider: default
    model: unused
    compact_threshold: 1000
providers:
  default:
    url: https://unused.test/v1
    key: unused
memory:
  enabled: true
`
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diag bytes.Buffer
	run := func(args ...string) int {
		out.Reset()
		diag.Reset()
		return New(strings.NewReader(""), &out, &diag).Run(append([]string{"--home", home}, args...))
	}
	if code := run("memory", "add", "--scope", "workspace", "--workspace", workspace, "--text", "saved note", "--id", "test-id"); code != 0 || !strings.Contains(out.String(), `"saved":true`) {
		t.Fatalf("add: code=%d stdout=%s stderr=%s", code, out.String(), diag.String())
	}
	if code := run("memory", "show", "--scope", "workspace", "--workspace", workspace); code != 0 || !strings.Contains(out.String(), "saved note") {
		t.Fatalf("show: code=%d stdout=%s stderr=%s", code, out.String(), diag.String())
	}
	t.Setenv("HORIZON_INHERITED_ACCESS", "read")
	if code := run("memory", "add", "--scope", "user", "--workspace", workspace, "--text", "forbidden"); code != 2 || !strings.Contains(diag.String(), "writes forbidden") {
		t.Fatalf("read add: code=%d stdout=%s stderr=%s", code, out.String(), diag.String())
	}
	if _, err := os.Stat(filepath.Join(home, "memory", "user.json")); !os.IsNotExist(err) {
		t.Fatalf("read access created user memory: %v", err)
	}
	t.Setenv("HORIZON_INHERITED_ACCESS", "")
	for _, args := range [][]string{{"memory", "show", "--text", "unexpected"}, {"memory", "watch", "--scope", "user"}, {"memory", "add", "--scope", "user", "extra"}, {"memory", "watch", "--period", "0"}} {
		if code := run(args...); code != 2 {
			t.Fatalf("accepted %v: code=%d stdout=%s stderr=%s", args, code, out.String(), diag.String())
		}
	}
}

func TestMemoryHelpWithoutHomeAndGuidance(t *testing.T) {
	home := filepath.Join(t.TempDir(), "missing")
	var out, diag bytes.Buffer
	if code := New(strings.NewReader(""), &out, &diag).Run([]string{"--home", home, "memory", "add", "--help"}); code != 0 || !strings.Contains(out.String(), "Usage:") || diag.Len() != 0 {
		t.Fatalf("help: code=%d stdout=%s stderr=%s", code, out.String(), diag.String())
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("help initialized home")
	}
	for name, text := range map[string]string{"instructions": builtinmemory.Instructions, "add": builtinmemory.AddDescription} {
		for _, want := range []string{"Proactively save", "do not wait for an explicit request", "personal meanings of words", "avoid duplicates", "Ask before saving sensitive information", "respect requests not to remember"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s missing guidance %q", name, want)
			}
		}
	}
}
