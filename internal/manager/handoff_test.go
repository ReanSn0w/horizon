package manager

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ReanSn0w/horizon/internal/maintenance"
)

func TestUpdateHandsOffBeforeParsingSelectedTemplate(t *testing.T) {
	e, home := installFixture(t)
	if err := os.WriteFile(filepath.Join(e.repository, "config.example.yaml"), []byte("future_core_setting: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	commitFixture(t, e.repository)
	before, _ := os.ReadFile(filepath.Join(home, ".manager", "state.json"))
	builds := 0
	build := e.build
	e.build = func(ctx context.Context, source, pkg, output string) error {
		builds++
		if pkg != "./plugins/manager" {
			t.Fatalf("bootstrap built %s", pkg)
		}
		return build(ctx, source, pkg, output)
	}
	sentinel := errors.New("selected manager reached")
	e.handoff = func(ctx context.Context, p preparedUpdate, locks []*os.File, in io.Reader, out io.Writer) error {
		if len(locks) != 2 || p.Options.Home != home || p.Version != 1 {
			t.Fatal(p)
		}
		if release, err := maintenance.Acquire(home, false); err == nil {
			release()
			t.Fatal("maintenance lock released before handoff")
		}
		if release, err := maintenance.Lock(filepath.Join(home, ".manager", "operation.lock"), true); err == nil {
			release()
			t.Fatal("operation lock released before handoff")
		}
		return sentinel
	}
	if err := runOperation(e, home, "update", ""); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(home, ".manager", "state.json"))
	if builds != 1 || !bytes.Equal(before, after) {
		t.Fatal("bootstrap changed installed state")
	}
}

func TestPreparedUpdateRunsSelectedManagerProcess(t *testing.T) {
	// A real selected manager runs against a tiny local source fixture; no network
	// or provider and no test hook in the continuation process.
	binary := filepath.Join(t.TempDir(), "horizon-manager")
	if _, err := command(context.Background(), "../..", "go", "build", "-o", binary, "./plugins/manager"); err != nil {
		t.Fatal(err)
	}
	e, home := installFixture(t)
	if err := os.WriteFile(filepath.Join(e.repository, "main.go"), []byte("package main\nfunc main() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	commitFixture(t, e.repository)
	stage, err := os.MkdirTemp(filepath.Join(home, ".manager"), "staging-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(stage)
	stateBefore, err := ReadState(home)
	if err != nil {
		t.Fatal(err)
	}
	envelope := preparedUpdate{Version: 1, Options: Options{Command: "update", Home: home}, Stage: stage, Commit: stateBefore.Commit, Ref: stateBefore.Ref, Source: stateBefore.Source, Repository: e.repository}
	handoffPath := filepath.Join(stage, "update.json")
	if err := writeJSON(handoffPath, envelope); err != nil {
		t.Fatal(err)
	}
	if _, err := command(context.Background(), "", binary, continueUpdate, handoffPath); err == nil {
		t.Fatal("continuation without inherited locks accepted")
	}
	e.handoff = nil
	e.build = func(ctx context.Context, source, pkg, output string) error {
		if pkg != "./plugins/manager" {
			t.Fatalf("old manager built %s", pkg)
		}
		return copyPrivate(binary, output)
	}
	var output bytes.Buffer
	if err := e.execute(context.Background(), Options{Command: "update", Home: home}, nil, &output); err != nil {
		t.Fatalf("%v\n%s", err, output.String())
	}
	state, err := ReadState(home)
	if err != nil {
		t.Fatal(err)
	}
	head, err := command(context.Background(), e.repository, "git", "rev-parse", "HEAD")
	if err != nil || state.Commit != head {
		t.Fatal(state, head, err)
	}
	installed, err := os.ReadFile(filepath.Join(home, "bin", "horizon-manager"))
	built, _ := os.ReadFile(binary)
	if err != nil || !bytes.Equal(installed, built) {
		t.Fatal("deployed manager differs from continuation")
	}
	if release, err := maintenance.Acquire(home, true); err != nil {
		t.Fatal(err)
	} else {
		release()
	}
	// A failing child must leave the installed configuration and state intact.
	before, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
	oldState, _ := os.ReadFile(filepath.Join(home, ".manager", "state.json"))
	if err := os.WriteFile(filepath.Join(e.repository, "config.example.yaml"), []byte("unknown_future_field: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	commitFixture(t, e.repository)
	output.Reset()
	if err := e.execute(context.Background(), Options{Command: "update", Home: home}, nil, &output); err == nil {
		t.Fatal("invalid template accepted")
	}
	after, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
	newState, _ := os.ReadFile(filepath.Join(home, ".manager", "state.json"))
	if !bytes.Equal(before, after) || !bytes.Equal(oldState, newState) {
		t.Fatal("failed child changed installation")
	}
}

func TestHandoffRejectsUnsupportedManagerAndCancellation(t *testing.T) {
	for _, cancelRun := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsupported", true: "cancelled"}[cancelRun], func(t *testing.T) {
			e, home := installFixture(t)
			e.handoff = nil
			e.build = func(ctx context.Context, source, pkg, output string) error {
				script := "#!/bin/sh\nexit 0\n"
				if cancelRun {
					script = "#!/bin/sh\nsleep 30\n"
				}
				return os.WriteFile(output, []byte(script), 0700)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelRun {
				e.handoff = func(ctx context.Context, p preparedUpdate, locks []*os.File, in io.Reader, out io.Writer) error {
					timer := time.AfterFunc(100*time.Millisecond, cancel)
					defer timer.Stop()
					return launchPrepared(ctx, p, locks, in, out)
				}
			}
			err := e.execute(ctx, Options{Command: "update", Home: home}, nil, io.Discard)
			if cancelRun && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if !cancelRun && (err == nil || !strings.Contains(err.Error(), "acknowledge")) {
				t.Fatal(err)
			}
			if release, err := maintenance.Acquire(home, true); err != nil {
				t.Fatal(err)
			} else {
				release()
			}
		})
	}
}
