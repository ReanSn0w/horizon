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
)

const decisionInputJSON = `{"state":{"value":9007199254740993},"questions":{"ready":{"instructions":"Ready?"}}}`
const decisionResponseJSON = `{"id":"d1","model":"test-decision-model","answers":{"ready":true},"usage":{"cost":0.01}}`

type shortWriter struct{}

func (shortWriter) Write(data []byte) (int, error) { return len(data) - 1, nil }

func TestDecisionRequestValidation(t *testing.T) {
	for _, value := range []string{decisionInputJSON, `{"state":null,"questions":{"a":{"instructions":"Ready?"}}}`} {
		if _, err := readDecisionRequest(strings.NewReader(value)); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []string{"null", "[]", "{", decisionInputJSON + " {}", `{"model":"x","questions":{}}`, `{"questions":{}}`, `{"questions":{"a":{"type":"bad","instructions":"x"}}}`, strings.Repeat(" ", maxDecisionInputBytes+1), string([]byte{'{', 0xff, '}'})} {
		if _, err := readDecisionRequest(strings.NewReader(value)); err == nil {
			t.Fatalf("accepted invalid input %q", value[:min(len(value), 32)])
		}
	}
}

func TestDecisionCommandIO(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/responses" || r.Header.Get("Authorization") != "Bearer decision-secret" {
			t.Error("wrong request configuration")
		}
		var body struct {
			Model string `json:"model"`
			Input []struct {
				Content string `json:"content"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "test-decision-model" || len(body.Input) != 1 || body.Input[0].Content != `{"value":9007199254740993}` {
			t.Errorf("request changed: %+v %v", body, err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"id":"d1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{\"ready\":true}"}]}],"usage":{"cost":0.01}}}`+"\n\n")
	}))
	defer server.Close()
	home := t.TempDir()
	config := fmt.Sprintf("mode: unit\nproviders:\n  default:\n    url: %s/api\n    key: decision-secret\ndefault_model: decision\ndecision_model: decision\nmodels:\n  decision:\n    provider: default\n    model: test-decision-model\n    compact_threshold: 1000\n", server.URL)
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}

	inputPath := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(inputPath, []byte(decisionInputJSON), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diag bytes.Buffer
	if code := New(strings.NewReader(""), &out, &diag).Run([]string{"--home", home, "decision", inputPath}); code != 0 || diag.Len() != 0 || out.String() != decisionResponseJSON+"\n" {
		t.Fatalf("file: code=%d out=%q diag=%q", code, out.String(), diag.String())
	}

	output := filepath.Join(t.TempDir(), "result.json")
	if err := os.WriteFile(output, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	diag.Reset()
	if code := New(strings.NewReader(decisionInputJSON), &out, &diag).Run([]string{"--home", home, "decision", "-o", output, "-"}); code != 0 || out.Len() != 0 || diag.Len() != 0 {
		t.Fatalf("stdin: code=%d out=%q diag=%q", code, out.String(), diag.String())
	}
	data, _ := os.ReadFile(output)
	info, _ := os.Stat(output)
	if string(data) != decisionResponseJSON+"\n" || info.Mode().Perm() != 0600 {
		t.Fatal("bad output file")
	}
	secondOutput := filepath.Join(t.TempDir(), "result.json")
	out.Reset()
	diag.Reset()
	if code := New(strings.NewReader(decisionInputJSON), &out, &diag).Run([]string{"--home", home, "decision", "-", "-o", secondOutput}); code != 0 || out.Len() != 0 || diag.Len() != 0 {
		t.Fatalf("trailing -o: code=%d out=%q diag=%q", code, out.String(), diag.String())
	}
	if _, err := os.Stat(filepath.Join(home, "dialogs")); !os.IsNotExist(err) {
		t.Fatal("decision created sessions")
	}
	diag.Reset()
	if code := New(strings.NewReader(decisionInputJSON), shortWriter{}, &diag).Run([]string{"--home", home, "decision", "-"}); code != 1 || !strings.Contains(diag.String(), "short write") {
		t.Fatalf("short write: code=%d diag=%q", code, diag.String())
	}
}

func TestDecisionFailureLeavesOutputUntouchedAndRedactsKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "decision-secret")
	}))
	defer server.Close()
	home := t.TempDir()
	config := fmt.Sprintf("mode: unit\nproviders:\n  default:\n    url: %s\n    key: decision-secret\ndefault_model: decision\ndecision_model: decision\nmodels:\n  decision:\n    provider: default\n    model: test-decision-model\n    compact_threshold: 1000\n", server.URL)
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	output := filepath.Join(dir, "result.json")
	if err := os.WriteFile(output, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diag bytes.Buffer
	code := New(strings.NewReader(decisionInputJSON), &out, &diag).Run([]string{"--home", home, "decision", "-", "-o", output})
	data, _ := os.ReadFile(output)
	entries, _ := os.ReadDir(dir)
	if code != 1 || out.Len() != 0 || strings.Contains(diag.String(), "decision-secret") || string(data) != "old" || len(entries) != 1 {
		t.Fatalf("code=%d out=%q diag=%q data=%q entries=%d", code, out.String(), diag.String(), data, len(entries))
	}
	if err := writeDecisionAtomic(dir, []byte("new")); err == nil {
		t.Fatal("accepted directory output")
	}
	entries, _ = os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("leaked temporary file")
	}
}

func TestDecisionArguments(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("mode: unit\nproviders: {default: {url: https://example.test, key: x}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"decision"}, {"decision", "a", "b"}, {"decision", "-o"}, {"decision", "-", "-o", "x", "-o", "y"}} {
		var out, diag bytes.Buffer
		if code := New(strings.NewReader(decisionInputJSON), &out, &diag).Run(append([]string{"--home", home}, args...)); code != 2 {
			t.Fatalf("accepted %v: code=%d out=%q diag=%q", args, code, out.String(), diag.String())
		}
	}
}

func TestDecisionHelpDoesNotInitializeHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "missing")
	marker := filepath.Join(t.TempDir(), "marker")
	pluginDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(pluginDir, 0700); err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(pluginDir, "horizon-decision")
	if err := os.WriteFile(plugin, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var out, diag bytes.Buffer
	if code := New(strings.NewReader(""), &out, &diag).Run([]string{"--home", home, "decision", "--help"}); code != 0 || !strings.Contains(out.String(), "Evaluate structured JSON") || diag.Len() != 0 {
		t.Fatalf("help: code=%d out=%q diag=%q", code, out.String(), diag.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("legacy decision plugin was executed")
	}
}
