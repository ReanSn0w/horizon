package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReanSn0w/horizon/internal/session"
)

func TestModelsOutputIsSorted(t *testing.T) {
	home := testHome(t, `
mode: unit
default_model: zebra
models:
  zebra:
    model: model-z
    compact_threshold: 1000
  alpha:
    model: model-a
    reasoning: high
    score: 42
    compact_threshold: 2000
provider:
  url: https://example.test/v1
  key: secret
`)
	code, stdout, stderr := runApp(t, "", "--home", home, "models")
	if code != ExitOK || stderr != "" {
		t.Fatalf("models code=%d stderr=%q", code, stderr)
	}
	alpha := strings.Index(stdout, "alpha")
	zebra := strings.Index(stdout, "zebra")
	if alpha < 0 || zebra < 0 || alpha > zebra {
		t.Fatalf("models output is not sorted:\n%s", stdout)
	}
	if !strings.Contains(stdout, "REASONING") || !strings.Contains(stdout, "42") || !strings.Contains(stdout, "*") {
		t.Fatalf("models output misses columns:\n%s", stdout)
	}
}

func TestCLIExitCodesAndContract(t *testing.T) {
	home := testHome(t, `
mode: unit
default_model: coding
models:
  coding:
    model: model-code
    compact_threshold: 1000
provider:
  url: https://example.test/v1
  key: secret
`)
	tests := []struct {
		name  string
		stdin string
		args  []string
		code  int
		want  string
	}{
		{"help", "", []string{"--help"}, ExitOK, "Available commands"},
		{"unknown command", "", []string{"unknown"}, ExitUsage, "Unknown command"},
		{"invalid access mode", "", []string{"--home", home, "resume", "-m", "x", "--access", "unsafe"}, ExitUsage, "Allowed values"},
		{"removed fork command", "", []string{"sessions", "fork"}, ExitUsage, "Unknown command"},
		{"required read ID", "", []string{"sessions", "read"}, ExitUsage, "id"},
		{"bad compact mode", "", []string{"sessions", "compact", "--mode", "xml"}, ExitUsage, "Allowed values"},
		{"unknown model", "", []string{"--home", home, "resume", "-m", "x", "--model", "absent"}, ExitUsage, "horizon models"},
		{"explicit empty wins over stdin", "stdin", []string{"--home", home, "resume", "--message="}, ExitUsage, "must not be empty"},
		{"nonempty stdin reaches provider", "from stdin\n", []string{"--home", home, "resume"}, ExitFailure, "request provider"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, stdout, stderr := runApp(t, test.stdin, test.args...)
			combined := stdout + stderr
			if code != test.code || !strings.Contains(combined, test.want) {
				t.Fatalf("Run() code=%d, output=%q; want code=%d containing %q", code, combined, test.code, test.want)
			}
		})
	}
}

func TestEmptyMessageDoesNotCreateState(t *testing.T) {
	home := testHome(t, "mode: unit\ndefault_model: coding\nmodels:\n  coding:\n    model: model-code\n    compact_threshold: 1000\nprovider:\n  url: https://example.test/v1\n  key: secret\n")
	code, _, _ := runApp(t, "", "--home", home, "resume", "--message=   ")
	if code != ExitUsage {
		t.Fatalf("empty message code = %d", code)
	}
	if _, err := os.Stat(filepath.Join(home, "dialogs")); !os.IsNotExist(err) {
		t.Fatalf("empty message created state: %v", err)
	}
}

func TestSessionCommands(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	if code, _, stderr := runApp(t, "", "--home", home, "init"); code != ExitOK || stderr != "" {
		t.Fatalf("init code=%d stderr=%q", code, stderr)
	}
	code, stdout, stderr := runAppAt(t, workspace, "", "--home", home, "sessions", "create")
	if code != ExitOK || stderr != "" {
		t.Fatalf("sessions create code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	id := strings.TrimSpace(stdout)
	if len(id) != 32 {
		t.Fatalf("created session ID = %q", id)
	}

	code, stdout, stderr = runAppAt(t, workspace, "", "--home", home, "sessions", "list")
	if code != ExitOK || stderr != "" || !strings.Contains(stdout, id) || !strings.Contains(stdout, "Новый диалог") {
		t.Fatalf("sessions list code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	store := session.NewStore(home)
	resolved, err := store.ResolveWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.ReadSnapshot(resolved, id)
	if err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr = runAppAt(t, workspace, "", "--home", home, "sessions", "read", "--id", id, "--turns", "1")
	if code != ExitOK || stderr != "" || !strings.Contains(stdout, "Session "+id) {
		t.Fatalf("sessions read code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	after, err := store.ReadSnapshot(resolved, id)
	if err != nil {
		t.Fatal(err)
	}
	if !before.LastAccessedAt.Equal(after.LastAccessedAt) {
		t.Fatal("list or read changed last_accessed_at")
	}

	code, forkOutput, stderr := runAppAt(t, workspace, "", "--home", home, "sessions", "create", "--fork", id)
	if code != ExitOK || stderr != "" || len(strings.TrimSpace(forkOutput)) != 32 {
		t.Fatalf("sessions fork code=%d stdout=%q stderr=%q", code, forkOutput, stderr)
	}

	code, _, stderr = runAppAt(t, workspace, "", "--home", home, "sessions", "delete", "--id", id)
	if code != ExitOK || stderr != "" {
		t.Fatalf("sessions delete code=%d stderr=%q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(store.WorkspaceDir(resolved.ID), id+".lock")); err != nil {
		t.Fatalf("delete removed lock file: %v", err)
	}
}

func TestResumeCreatesNamesAndFailsSessionWhenProviderIsUnavailable(t *testing.T) {
	home := testHome(t, `
mode: unit
default_model: coding
models:
  coding:
    model: model-code
    compact_threshold: 1000
provider:
  url: https://example.test/v1
  key: secret
`)
	workspace := t.TempDir()
	code, _, stderr := runAppAt(t, workspace, "", "--home", home, "resume", "-m", "\n  First line 🚀  \nsecond")
	if code != ExitFailure || !strings.Contains(stderr, "request provider") {
		t.Fatalf("resume code=%d stderr=%q", code, stderr)
	}
	store := session.NewStore(home)
	resolved, err := store.ResolveWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.List(resolved, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Session.Title != "First line 🚀" {
		t.Fatalf("accepted resume sessions = %+v", items)
	}
	if len(items[0].Session.Turns) != 1 || items[0].Session.Turns[0].Status != session.StatusFailed {
		t.Fatalf("failed provider turn was not persisted: %+v", items[0].Session.Turns)
	}
}

func TestResumeJSONLContainsOnlyStructuredEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}]}}\n\n")
	}))
	defer server.Close()
	home := testHome(t, fmt.Sprintf(`
mode: unit
default_model: coding
models:
  coding:
    model: model-code
    compact_threshold: 1000
provider:
  url: %s
  key: secret
`, server.URL))
	workspace := t.TempDir()
	code, stdout, stderr := runAppAt(t, workspace, "", "--home", home, "resume", "--mode", "jsonl", "-m", "say hello")
	if code != ExitOK || stderr != "" {
		t.Fatalf("jsonl resume code=%d stderr=%q", code, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 5 {
		t.Fatalf("JSONL lines=%d: %s", len(lines), stdout)
	}
	want := []string{"turn_started", "model_request_started", "progress", "model_request_completed", "turn_completed"}
	for index, line := range lines {
		var event struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("line %d is not JSON: %v: %q", index, err, line)
		}
		if event.Type != want[index] {
			t.Fatalf("event %d type=%q want=%q", index, event.Type, want[index])
		}
	}
}

func TestResumeJSONLFailureEndsWithStructuredFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\"}}\n\n")
	}))
	defer server.Close()
	home := testHome(t, fmt.Sprintf(`
mode: unit
default_model: coding
models:
  coding:
    model: model-code
    compact_threshold: 1000
provider:
  url: %s
  key: secret
`, server.URL))
	code, stdout, stderr := runAppAt(t, t.TempDir(), "", "--home", home, "resume", "--mode", "jsonl", "-m", "fail")
	if code != ExitFailure || !strings.Contains(stderr, "terminal event") {
		t.Fatalf("jsonl failure code=%d stderr=%q", code, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	var terminal struct {
		Type string `json:"type"`
		Data struct {
			Code string `json:"code"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &terminal); err != nil {
		t.Fatal(err)
	}
	if terminal.Type != "turn_failed" || terminal.Data.Code != "incomplete_response" {
		t.Fatalf("terminal JSONL event = %+v", terminal)
	}
}

func runApp(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	app := New(strings.NewReader(stdin), &stdout, &stderr)
	return app.Run(args), stdout.String(), stderr.String()
}

func runAppAt(t *testing.T, workspace, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	app := New(strings.NewReader(stdin), &stdout, &stderr)
	app.workingDir = func() (string, error) { return workspace, nil }
	return app.Run(args), stdout.String(), stderr.String()
}

func testHome(t *testing.T, config string) string {
	t.Helper()
	home := t.TempDir()
	if !strings.Contains(config, "\ndecision:") {
		config += "\ndecision:\n  provider:\n    url: https://example.test/api\n    key: decision-secret\n  model: typesafe/jev-1.13\n"
	}
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestImplicitResume(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != "model-code" {
			t.Errorf("request=%+v err=%v", request, err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer\"}]}]}}\n\n")
	}))
	defer server.Close()
	home := testHome(t, fmt.Sprintf("mode: unit\ndefault_model: coding\nmodels:\n  coding:\n    model: model-code\n    compact_threshold: 1000\nprovider:\n  url: %s\n  key: secret\n", server.URL))
	workspace := t.TempDir()
	for _, explicit := range []bool{false, true} {
		for _, tail := range [][]string{nil, {"-m", "models"}, {"--message=resume"}, {"--model", "coding", "--mode", "plain"}, {"-v", "-m", "x"}} {
			args := []string{"--home", home}
			if explicit {
				args = append(args, "resume")
			}
			args = append(args, tail...)
			code, out, errOut := runAppAt(t, workspace, "stdin", args...)
			if code != ExitOK || out != "answer\n" {
				t.Fatalf("%q: %d %q %q", args, code, out, errOut)
			}
		}
	}
	store := session.NewStore(home)
	ws, err := store.ResolveWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.List(ws, false)
	if err != nil || len(items) != 1 {
		t.Fatalf("sessions=%v err=%v", items, err)
	}
	code, out, errOut := runAppAt(t, workspace, "", "--home", home, "--session", items[0].Session.SessionID, "-m", "continue")
	if code != ExitOK || out != "answer\n" {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	for _, tail := range [][]string{{"--bogus"}, {"--", "resume"}, {"-m", "x", "extra"}} {
		code, _, _ := runAppAt(t, workspace, "", append([]string{"--home", home}, tail...)...)
		if code != ExitUsage {
			t.Fatalf("%q: code=%d", tail, code)
		}
	}
}
