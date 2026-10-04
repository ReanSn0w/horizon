package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltInitInstalledPlugins(t *testing.T) {
	binary := buildBinary(t)
	home, cwd := t.TempDir(), t.TempDir()
	pluginDir := filepath.Join(home, "plugins")
	if err := os.Mkdir(pluginDir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"browser", "decision", "memory", "telegram"} {
		cmd := exec.Command("go", "build", "-o", filepath.Join(pluginDir, "horizon-"+name), "./plugins/"+name)
		cmd.Dir = "../.."
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v %s", name, err, output)
		}
	}
	if out, diag, err := run(binary, cwd, "", "--home", home, "init"); err != nil || diag != "" {
		t.Fatalf("init %v %q %q", err, out, diag)
	}
	path := filepath.Join(home, "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"browser:", "api_key:", "memory:", "threshold: 5", "telegram:", "bot_token:", "agent_plugins: []"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %q: %s", want, data)
		}
	}
	if strings.Contains(string(data), "plugins:\n    decision:") {
		t.Fatal("decision gained a redundant plugin section")
	}
	configured := strings.Replace(string(data), "api_key: \"\"", "api_key: private-browser", 1)
	configured = strings.Replace(configured, "bot_token: \"\"", "bot_token: private-telegram", 1)
	if configured == string(data) || !strings.Contains(configured, "private-browser") || !strings.Contains(configured, "private-telegram") {
		t.Fatalf("unexpected generated settings: %s", data)
	}
	if err := os.WriteFile(path, []byte(configured), 0600); err != nil {
		t.Fatal(err)
	}
	out, diag, err := run(binary, cwd, "", "--home", home, "init")
	again, _ := os.ReadFile(path)
	if err != nil || diag != "" || string(again) != configured || strings.Contains(out+diag, "private-browser") || strings.Contains(out+diag, "private-telegram") {
		t.Fatalf("repeat init %v %q %q", err, out, diag)
	}
	if out, _, err := run(binary, cwd, "", "--home", home, "telegram", "init"); err == nil || strings.Contains(out, "Telegram configuration") {
		t.Fatal("removed telegram init still accepted")
	}

	legacyHome := t.TempDir()
	if err := os.Mkdir(filepath.Join(legacyHome, "plugins"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(pluginDir, "horizon-telegram"), filepath.Join(legacyHome, "plugins", "horizon-telegram")); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(legacyHome, "config.yaml")
	legacy := "# retained\nmode: unit\nplugins:\n  gateway:\n    telegram:\n      bot_token: old-token\n"
	if err := os.WriteFile(legacyPath, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	if out, diag, err := run(binary, cwd, "", "--home", legacyHome, "init"); err != nil || diag != "" {
		t.Fatalf("migrate %v %q %q", err, out, diag)
	}
	migrated, _ := os.ReadFile(legacyPath)
	if !strings.Contains(string(migrated), "# retained") || !strings.Contains(string(migrated), "old-token") || !strings.Contains(string(migrated), "owner_user_id: 0") || strings.Contains(string(migrated), "gateway:") {
		t.Fatalf("migration lost config: %s", migrated)
	}
	conflict := legacy + "  telegram: {}\n"
	if err := os.WriteFile(legacyPath, []byte(conflict), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(binary, cwd, "", "--home", legacyHome, "init"); err == nil {
		t.Fatal("accepted conflicting sections")
	}
	afterConflict, _ := os.ReadFile(legacyPath)
	if string(afterConflict) != conflict {
		t.Fatal("conflict changed config")
	}
}
