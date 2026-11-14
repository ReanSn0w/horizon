package timer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ReanSn0w/horizon/internal/daemon"
)

func TestRunNowRecoversUnknownSubmission(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	config := "default_model: coding\nmodels:\n  coding:\n    provider: default\n    model: example\n    compact_threshold: 100000\nproviders:\n  default:\n    url: https://example.test/v1\n    key: test\n"
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	putFile(t, home, "reports", markdown(workspace, minutely, "Inspect reports."))
	var submitted, lookups int
	var saved daemon.Request
	call := func(_ context.Context, _ string, request daemon.Request) (daemon.Response, error) {
		if request.Method == "timer_lookup" {
			lookups++
			if saved.OperationID == request.OperationID {
				data, _ := json.Marshal(daemon.Task{ID: "task-1", Mode: daemon.FreshTask, SourceKey: "reports"})
				return daemon.Response{Data: data}, nil
			}
			return daemon.Response{}, &daemon.Failure{Code: "not_found"}
		}
		submitted++
		saved = request
		return daemon.Response{}, errors.New("connection closed after submission")
	}
	if _, err := runNow(context.Background(), home, "reports", call); err == nil {
		t.Fatal("lost reply was reported successful")
	} else if submitted == 0 {
		t.Fatalf("run did not submit: %v", err)
	}
	if submitted != 1 || saved.Method != "timer_run" || saved.Access != "write" || saved.Caller.Workspace == "" {
		t.Fatalf("submission: %+v", saved)
	}
	id, err := runNow(context.Background(), home, "reports", call)
	if err != nil || id != "task-1" || submitted != 1 || lookups != 1 {
		t.Fatalf("recovery: %q %v submits=%d lookups=%d", id, err, submitted, lookups)
	}
	if _, err := os.Stat(filepath.Join(home, "timer", "manual-reports.json")); !os.IsNotExist(err) {
		t.Fatalf("intent remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "timer", "state.json")); !os.IsNotExist(err) {
		t.Fatalf("manual run changed scheduled slot state: %v", err)
	}
}

func TestRunNowRejectsDisabledAndUnavailable(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	config := "default_model: coding\nmodels:\n  coding:\n    provider: default\n    model: example\n    compact_threshold: 100000\nproviders:\n  default:\n    url: https://example.test/v1\n    key: test\n"
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	putFile(t, home, "reports", "---\nenabled: false\n"+markdown(workspace, minutely, "Task")[4:])
	called := 0
	call := func(_ context.Context, _ string, _ daemon.Request) (daemon.Response, error) {
		called++
		return daemon.Response{}, &daemon.Failure{Code: "daemon_unavailable"}
	}
	if _, err := runNow(context.Background(), home, "reports", call); err == nil || called != 0 {
		t.Fatalf("disabled: %v calls=%d", err, called)
	}
	if _, err := SetEnabled(home, "reports", true); err != nil {
		t.Fatal(err)
	}
	if _, err := runNow(context.Background(), home, "reports", call); err == nil || called != 1 {
		t.Fatalf("unavailable: %v calls=%d", err, called)
	}
}

func TestRunNowPreparesAndReusesDefaultWorkspace(t *testing.T) {
	home := t.TempDir()
	configuration := "default_model: coding\nmodels:\n  coding:\n    provider: default\n    model: example\n    compact_threshold: 100000\nproviders:\n  default:\n    url: https://example.test/v1\n    key: test\n"
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	putFile(t, home, "nap", "---\nname: Nap\ndescription: Job\nenabled: false\nschedule:\n"+minutely+"\n---\nTask.\n")
	calls := 0
	call := func(_ context.Context, _ string, req daemon.Request) (daemon.Response, error) {
		calls++
		marker := filepath.Join(req.Caller.Workspace, "notes.md")
		if calls == 1 {
			if err := os.WriteFile(marker, []byte("observation"), 0600); err != nil {
				t.Fatal(err)
			}
		} else {
			data, err := os.ReadFile(marker)
			if err != nil || string(data) != "observation" {
				t.Fatal(string(data), err)
			}
		}
		data, _ := json.Marshal(daemon.Task{ID: "task-1", SourceKey: "nap"})
		return daemon.Response{Data: data}, nil
	}
	if _, err := runNow(context.Background(), home, "nap", call); err == nil || calls != 0 {
		t.Fatal("disabled timer submitted")
	}
	if _, err := os.Stat(filepath.Join(home, "workspace")); !os.IsNotExist(err) {
		t.Fatal("disabled timer created workspace")
	}
	if _, err := SetEnabled(home, "nap", true); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := runNow(context.Background(), home, "nap", call); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}
