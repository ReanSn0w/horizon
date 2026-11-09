package manager

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/maintenance"
)

func TestRealGoBuildAndPinnedRevision(t *testing.T) {
	e := fixtureEngine(t)
	e.build = nil
	program := `package main
import("fmt";"os")
func main(){if len(os.Args)>1&&os.Args[1]=="horizon-plugin-metadata"{fmt.Println("{\"protocol_version\":1,\"version\":\"fixture\",\"description\":\"compiled fixture\"}")}else{fmt.Println("fixture core")}}
`
	for _, p := range []string{"main.go", "plugins/manager/main.go"} {
		if err := os.WriteFile(filepath.Join(e.repository, p), []byte(program), 0600); err != nil {
			t.Fatal(err)
		}
	}
	commitFixture(t, e.repository)
	home := t.TempDir()
	if err := e.execute(context.Background(), Options{Command: "install", Home: home, Yes: true, Source: &config.Source{Type: "branch", Branch: "feature/install"}}, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	output, err := command(context.Background(), "", filepath.Join(home, "bin/horizon"), "--version")
	if err != nil || output != "fixture core" {
		t.Fatal(output, err)
	}
	state, _ := ReadState(home)
	head, err := command(context.Background(), e.repository, "git", "rev-parse", "HEAD")
	if err != nil || state.Commit != head {
		t.Fatal(state, head, err)
	}
}
func TestReleaseBranchSwitchAndConfigSymlink(t *testing.T) {
	e, home := installFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"tag_name":"v1"}`) }))
	defer server.Close()
	e.releaseURL = server.URL
	path := filepath.Join(home, "config.yaml")
	target := filepath.Join(t.TempDir(), "custom.yaml")
	if err := os.Rename(path, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	os.Chmod(target, 0640)
	if err := config.WriteInstallation(home, config.Installation{Source: config.Source{Type: "release"}}); err != nil {
		t.Fatal(err)
	}
	if err := runOperation(e, home, "update", ""); err != nil {
		t.Fatal(err)
	}
	state, _ := ReadState(home)
	if state.Ref != "refs/tags/v1" {
		t.Fatal(state)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("config symlink replaced")
	}
	info, _ = os.Stat(target)
	if info.Mode().Perm() != 0640 {
		t.Fatal("config mode replaced")
	}
	if err := config.WriteInstallation(home, config.Installation{Source: config.Source{Type: "branch", Branch: "feature/install"}}); err != nil {
		t.Fatal(err)
	}
	if err := runOperation(e, home, "update", ""); err != nil {
		t.Fatal(err)
	}
	state, _ = ReadState(home)
	if state.Ref != "refs/heads/feature/install" {
		t.Fatal(state)
	}
}
func TestMissingToolsAndCancellation(t *testing.T) {
	t.Run("tools", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "not-created")
		t.Setenv("PATH", t.TempDir())
		err := engine().execute(context.Background(), Options{Command: "install", Home: home, Yes: true}, nil, io.Discard)
		if err == nil {
			t.Fatal("missing tools accepted")
		}
		if _, err = os.Stat(home); !os.IsNotExist(err) {
			t.Fatal("home created before tool check")
		}
	})
	t.Run("cancel", func(t *testing.T) {
		e, home := installFixture(t)
		before, _ := os.ReadFile(filepath.Join(home, ".manager/state.json"))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := e.execute(ctx, Options{Command: "update", Home: home}, nil, io.Discard)
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		after, _ := os.ReadFile(filepath.Join(home, ".manager/state.json"))
		if !bytes.Equal(before, after) {
			t.Fatal("cancel changed state")
		}
	})
}
func TestIncompatibleSourceAndGo(t *testing.T) {
	for _, kind := range []string{"structure", "go"} {
		t.Run(kind, func(t *testing.T) {
			e := fixtureEngine(t)
			if kind == "structure" {
				os.Remove(filepath.Join(e.repository, "plugins/manager/plugin.json"))
			} else {
				os.WriteFile(filepath.Join(e.repository, "go.mod"), []byte("module example.test/test\ngo 99.0\n"), 0600)
			}
			commitFixture(t, e.repository)
			_, _, _, err := e.prepare(context.Background(), t.TempDir(), config.Source{Type: "branch", Branch: "feature/install"})
			if err == nil {
				t.Fatal("incompatible source accepted")
			}
		})
	}
}
func TestBuildFailureAndDisappearingPluginLeaveInstallation(t *testing.T) {
	e, home := installFixture(t)
	if err := runOperation(e, home, "plugins install", "browser"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(home, ".manager/state.json"))
	e.build = func(context.Context, string, string, string) error { return errors.New("build error") }
	if err := runOperation(e, home, "update", ""); err == nil {
		t.Fatal("build failure ignored")
	}
	os.Remove(filepath.Join(e.repository, "plugins/browser/plugin.json"))
	commitFixture(t, e.repository)
	if err := runOperation(e, home, "update", ""); err == nil || !strings.Contains(err.Error(), "browser") {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(home, ".manager/state.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("failed update changed state")
	}
}

func TestSourceIsPinnedWhileRemoteBranchMoves(t *testing.T) {
	e, home := installFixture(t)
	build := e.build
	moved := false
	var builtCommit string
	e.build = func(ctx context.Context, source, pkg, out string) error {
		commit, err := command(ctx, source, "git", "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		if !moved {
			builtCommit = commit
			os.WriteFile(filepath.Join(e.repository, "moved"), []byte("new head"), 0600)
			commitFixture(t, e.repository)
			moved = true
		} else if commit != builtCommit {
			t.Fatal("build changed commit")
		}
		return build(ctx, source, pkg, out)
	}
	if err := runOperation(e, home, "update", ""); err != nil {
		t.Fatal(err)
	}
	state, _ := ReadState(home)
	head, _ := command(context.Background(), e.repository, "git", "rev-parse", "HEAD")
	if state.Commit != builtCommit || state.Commit == head {
		t.Fatal("operation did not pin source", state.Commit, builtCommit, head)
	}
}
func TestUnwritableDestinationRollsBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	e, home := installFixture(t)
	before, _ := os.ReadFile(filepath.Join(home, ".manager/state.json"))
	bin := filepath.Join(home, "bin")
	if err := os.Chmod(bin, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(bin, 0700)
	if err := runOperation(e, home, "update", ""); err == nil {
		t.Fatal("unwritable bin accepted")
	}
	after, _ := os.ReadFile(filepath.Join(home, ".manager/state.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("state changed on permission failure")
	}
	// Restore permissions and recover the retained journal, if rollback also lacked
	// permission. A subsequent update must be able to proceed.
	os.Chmod(bin, 0700)
	if err := runOperation(e, home, "update", ""); err != nil {
		t.Fatal(err)
	}
}
func TestUnknownOriginAndLegacyActiveSessionAreRejected(t *testing.T) {
	e, home := installFixture(t)
	if _, err := command(context.Background(), filepath.Join(home, "source"), "git", "remote", "set-url", "origin", "https://example.test/foreign.git"); err != nil {
		t.Fatal(err)
	}
	if err := runOperation(e, home, "update", ""); err == nil {
		t.Fatal("foreign origin overwritten")
	}
	if _, err := command(context.Background(), filepath.Join(home, "source"), "git", "remote", "set-url", "origin", e.repository); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "dialogs", "workspace")
	os.MkdirAll(dir, 0700)
	release, err := maintenance.Lock(filepath.Join(dir, "session.lock"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := runOperation(e, home, "update", ""); err == nil {
		t.Fatal("legacy session ignored")
	}
}

// This opt-in check uses only the local repository and temporary homes. It
// compiles the real core and both optional plugins without calling their APIs.
func TestLocalRepositoryInstallation(t *testing.T) {
	if os.Getenv("HORIZON_MANAGER_REPO_CHECK") != "1" {
		t.Skip("explicit local repository build check")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	branch, err := command(context.Background(), root, "git", "branch", "--show-current")
	if err != nil || branch == "" {
		t.Fatal(branch, err)
	}
	e := engine()
	e.repository = root
	home := t.TempDir()
	if err = e.execute(context.Background(), Options{Command: "install", Home: home, Yes: true, Source: &config.Source{Type: "branch", Branch: branch}}, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"browser", "telegram"} {
		if err = runOperation(e, home, "plugins install", name); err != nil {
			t.Fatal(err)
		}
	}
	enabled, err := enabledPlugins(home)
	if err != nil || len(enabled) != 0 {
		t.Fatal("plugins enabled automatically", enabled, err)
	}
	if err = runOperation(e, home, "update", ""); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"browser", "telegram"} {
		if err = runOperation(e, home, "plugins remove", name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = os.Stat(filepath.Join(home, "gateway")); !os.IsNotExist(err) {
		t.Fatal("installation started plugin state")
	}
}

func TestCommandCancellationTerminatesProcessGroup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := command(ctx, "", "/bin/sh", "-c", "sleep 30 & wait")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 3*time.Second {
		t.Fatal("cancellation did not complete promptly", err, time.Since(started))
	}
}
func TestNoninteractiveInstallRequiresExplicitConsent(t *testing.T) {
	home := filepath.Join(t.TempDir(), "absent")
	err := engine().execute(context.Background(), Options{Command: "install", Home: home}, strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatal(err)
	}
	if _, err = os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("home created")
	}
}
