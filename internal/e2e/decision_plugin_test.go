package e2e

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltDecisionPlugin(t *testing.T) {
	binary := buildBinary(t)
	home, workspace := t.TempDir(), t.TempDir()
	plugins := filepath.Join(home, "plugins")
	if err := os.Mkdir(plugins, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", filepath.Join(plugins, "horizon-decision"), "./plugins/decision")
	cmd.Dir = "../.."
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, data)
	}
	if out, diag, err := run(binary, workspace, "", "--home", home, "decision", "--help"); err != nil || diag != "" || !strings.Contains(out, "Usage:") {
		t.Fatalf("help: %v %q %q", err, out, diag)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/alpha/decisions" || r.Header.Get("Authorization") != "Bearer local-secret" {
			t.Error("incorrect endpoint or key")
		}
		fmt.Fprint(w, `{"id":"local","model":"jev","provider":"test","answers":{"ready":{"type":"noul","noul":1}},"usage":{"cost":0}}`)
	}))
	defer server.Close()
	cfg := fmt.Sprintf("mode: unit\nprovider:\n  url: https://example.test\n  key: unused\ndecision:\n  provider:\n    url: %s\n    key: local-secret\n  model: jev\n", server.URL)
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	input := `{"state":{},"questions":{"ready":{"type":"noul","instructions":"Ready?"}}}`
	out, diag, err := run(binary, workspace, input, "--home", home, "decision", "-")
	if err != nil || diag != "" || !strings.Contains(out, `"id":"local"`) {
		t.Fatalf("decision: %v %q %q", err, out, diag)
	}
	if _, err := os.Stat(filepath.Join(home, "dialogs")); !os.IsNotExist(err) {
		t.Fatal("plugin created session")
	}
}
