package manager

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/maintenance"
)

func installFixture(t *testing.T) (*Engine, string) {
	t.Helper()
	e := fixtureEngine(t)
	for _, name := range []string{"browser", "telegram", "agent-telegram"} {
		path := filepath.Join(e.repository, "plugins", name)
		os.MkdirAll(path, 0700)
		data, err := os.ReadFile("../../plugins/" + name + "/plugin.json")
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(path, "plugin.json"), data, 0600)
	}
	commitFixture(t, e.repository)
	home := t.TempDir()
	if err := e.execute(context.Background(), Options{Command: "install", Home: home, Yes: true, Source: &config.Source{Type: "branch", Branch: "feature/install"}}, strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}
	return e, home
}
func commitFixture(t *testing.T, repo string) {
	t.Helper()
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "update fixture"}} {
		if _, err := command(context.Background(), repo, "git", args...); err != nil {
			t.Fatal(err)
		}
	}
}
func runOperation(e *Engine, home, cmd, name string) error {
	return e.execute(context.Background(), Options{Command: cmd, Home: home, Name: name}, strings.NewReader(""), io.Discard)
}
func TestPluginLifecycleUsesInstalledRevision(t *testing.T) {
	e, home := installFixture(t)
	before, _ := ReadState(home)
	for _, name := range []string{"browser", "telegram"} {
		if err := runOperation(e, home, "plugins install", name); err != nil {
			t.Fatal(err)
		}
	}
	state, _ := ReadState(home)
	if state.Commit != before.Commit || !slices.Equal(state.Plugins, []string{"browser", "telegram"}) {
		t.Fatal(state)
	}
	data, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
	data = append(data, []byte("\nagent_plugins: [browser]\n")...) // template has no agent_plugins field
	if strings.Count(string(data), "agent_plugins:") > 1 {
		data = bytes.Replace(data, []byte("agent_plugins: []"), []byte("agent_plugins: [browser]"), 1)
		index := bytes.LastIndex(data, []byte("\nagent_plugins: [browser]\n"))
		data = data[:index]
	}
	os.WriteFile(filepath.Join(home, "config.yaml"), data, 0600)
	sentinel := filepath.Join(home, "browser", "keep")
	os.MkdirAll(filepath.Dir(sentinel), 0700)
	os.WriteFile(sentinel, []byte("user data"), 0600)
	if err := runOperation(e, home, "plugins remove", "browser"); err != nil {
		t.Fatal(err)
	}
	enabled, err := enabledPlugins(home)
	if err != nil || slices.Contains(enabled, "browser") {
		t.Fatal(enabled, err)
	}
	if _, err = os.Stat(sentinel); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(home, "bin/horizon-browser")); !os.IsNotExist(err) {
		t.Fatal("binary retained")
	}
	var out bytes.Buffer
	if err = e.execute(context.Background(), Options{Command: "plugins list", Home: home}, nil, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	availableColumn := strings.Index(lines[0], "AVAILABLE")
	for _, line := range lines[1:] {
		if strings.Contains(line, "\t") || strings.Index(line, "true") != availableColumn {
			t.Fatalf("misaligned table: %q", out.String())
		}
	}
	if !strings.Contains(strings.Join(strings.Fields(out.String()), " "), "browser true false false") {
		t.Fatal(out.String())
	}
}
func TestUpdateRollbackAtEveryReplacement(t *testing.T) {
	e, home := installFixture(t)
	for _, name := range []string{"browser", "telegram"} {
		if err := runOperation(e, home, "plugins install", name); err != nil {
			t.Fatal(err)
		}
	}
	paths := []string{"bin/horizon", "bin/horizon-browser", "bin/horizon-telegram", "bin/horizon-manager", ".manager/state.json", "config.yaml"}
	original := map[string][]byte{}
	for _, p := range paths {
		original[p], _ = os.ReadFile(filepath.Join(home, p))
	}
	os.WriteFile(filepath.Join(e.repository, "revision"), []byte("next"), 0600)
	commitFixture(t, e.repository)
	// Unchanged configuration is not a deployment entry.
	for index := 0; index < 6; index++ {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			e.afterReplace = func(i int) error {
				if i == index {
					return errors.New("injected deployment error")
				}
				return nil
			}
			if err := runOperation(e, home, "update", ""); err == nil {
				t.Fatal("expected failure")
			}
			for _, p := range paths {
				data, err := os.ReadFile(filepath.Join(home, p))
				if err != nil || !bytes.Equal(data, original[p]) {
					t.Fatalf("not restored %s: %v", p, err)
				}
			}
			state, _ := ReadState(home)
			if err := e.checkCheckout(context.Background(), filepath.Join(home, "source"), state.Commit); err != nil {
				t.Fatal(err)
			}
		})
	}
	e.afterReplace = nil
	if err := runOperation(e, home, "update", ""); err != nil {
		t.Fatal(err)
	}
	state, _ := ReadState(home)
	if bytes.Contains(original[".manager/state.json"], []byte(state.Commit)) {
		t.Fatal("revision not updated")
	}
}
func TestBusyAndUnknownFilesArePreserved(t *testing.T) {
	e, home := installFixture(t)
	release, err := maintenance.Acquire(home, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = runOperation(e, home, "update", ""); err == nil {
		t.Fatal("busy update succeeded")
	}
	release()
	path := filepath.Join(home, "bin", "horizon-browser")
	os.WriteFile(path, []byte("foreign"), 0700)
	if err = runOperation(e, home, "plugins install", "browser"); err == nil {
		t.Fatal("foreign binary replaced")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "foreign" {
		t.Fatal("foreign data changed")
	}
}
func TestRecoverInterruptedTransaction(t *testing.T) {
	e, home := installFixture(t)
	stage := t.TempDir()
	os.MkdirAll(filepath.Join(stage, "bin"), 0700)
	os.WriteFile(filepath.Join(stage, "bin/horizon"), []byte("new binary"), 0700)
	old, _ := os.ReadFile(filepath.Join(home, "bin/horizon"))
	// Panic simulates process interruption: deployment's ordinary error rollback
	// does not run. Next invocation must restore from the durable journal.
	e.afterReplace = func(int) error { panic("power interruption") }
	func() { defer func() { _ = recover() }(); _ = e.deploy(home, stage, []string{"bin/horizon"}, nil) }()
	if err := recoverTransaction(home); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(home, "bin/horizon"))
	if !bytes.Equal(data, old) {
		t.Fatal("interrupted replacement not recovered")
	}
	if err := recoverTransaction(home); err != nil {
		t.Fatal(err)
	}
}
