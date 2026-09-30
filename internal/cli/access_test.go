package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEffectiveAccessPrecedence(t *testing.T) {
	t.Setenv(inheritedAccessEnv, "read")
	for _, requested := range []string{"", "write", "full"} {
		got, err := effectiveAccess(requested)
		if err != nil || got != "read" {
			t.Fatalf("requested %q: %q %v", requested, got, err)
		}
	}
	t.Setenv(inheritedAccessEnv, "full")
	got, err := effectiveAccess("read")
	if err != nil || got != "full" {
		t.Fatalf("inherited full: %q %v", got, err)
	}
	t.Setenv(inheritedAccessEnv, "invalid")
	if _, err := effectiveAccess("full"); err == nil || !strings.Contains(err.Error(), "invalid inherited") {
		t.Fatalf("invalid inherited access: %v", err)
	}
}

func TestResumeRequiresDecisionProviderBeforeSession(t *testing.T) {
	home := t.TempDir()
	config := "mode: unit\ndefault_model: coding\nmodels:\n  coding:\n    model: model-code\n    compact_threshold: 1000\nprovider:\n  url: https://example.test/v1\n  key: secret\n"
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, diagnostics := runApp(t, "", "--home", home, "resume", "--access", "read", "-m", "hello")
	if code != ExitUsage || out != "" || !strings.Contains(diagnostics, "decision.provider.key") {
		t.Fatalf("resume code=%d out=%q err=%q", code, out, diagnostics)
	}
	if _, err := os.Stat(filepath.Join(home, "dialogs")); !os.IsNotExist(err) {
		t.Fatalf("resume created session state: %v", err)
	}
}

func TestEffectiveAccessStandalone(t *testing.T) {
	t.Setenv(inheritedAccessEnv, "placeholder")
	if err := os.Unsetenv(inheritedAccessEnv); err != nil {
		t.Fatal(err)
	}
	for requested, want := range map[string]string{"": "write", "read": "read", "full": "full"} {
		got, err := effectiveAccess(requested)
		if err != nil || got != want {
			t.Fatalf("requested %q: %q %v", requested, got, err)
		}
	}
}
