package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const inputJSON = `{"state":{"value":9007199254740993},"questions":{"ready":{"type":"noul","instructions":"Ready?"}}}`
const responseJSON = `{"id":"d1","model":"jev","provider":"test","answers":{"ready":{"type":"noul","noul":0.9}},"usage":{"cost":0.01}}`

func TestArgumentsAndInput(t *testing.T) {
	for _, args := range [][]string{{"-"}, {"-", "-o", "result"}, {"-o", "result", "-"}, {"--", "-file"}} {
		if _, _, err := parseArgs(args); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{nil, {"a", "b"}, {"-o"}, {"--model", "jev", "-"}, {"-", "-o", "x", "-o", "y"}} {
		if _, _, err := parseArgs(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	for _, value := range []string{inputJSON, `{"state":null,"questions":{"a":{"type":"choice","instructions":"Choose","criteria":{"x":"one"}}}}`, `{"questions":{"a":{"type":"score","instructions":"Rate"}}}`} {
		if _, err := readRequest(strings.NewReader(value)); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []string{"null", "[]", "{", inputJSON + " {}", `{"model":"x","questions":{}}`, `{"questions":{}}`, `{"questions":{"a":{"type":"bad","instructions":"x"}}}`, strings.Repeat(" ", maxInputBytes+1), string([]byte{'{', 0xff, '}'})} {
		if _, err := readRequest(strings.NewReader(value)); err == nil {
			t.Fatalf("accepted invalid input")
		}
	}
}

func setupHome(t *testing.T, endpoint string) string {
	t.Helper()
	home := t.TempDir()
	data := fmt.Sprintf("mode: unit\nprovider:\n  url: https://example.test\n  key: chat-secret\ndecision:\n  provider:\n    url: %s\n    key: decision-secret\n  model: jev\nplugins:\n  decision: {}\n", endpoint)
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HORIZON_HOME", home)
	return home
}

func TestDecisionIO(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/alpha/decisions" || r.Header.Get("Authorization") != "Bearer decision-secret" {
			t.Error("wrong request configuration")
		}
		var body struct {
			Model string                 `json:"model"`
			State map[string]json.Number `json:"state"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "jev" || body.State["value"].String() != "9007199254740993" {
			t.Errorf("request changed: %+v %v", body, err)
		}
		fmt.Fprint(w, responseJSON)
	}))
	defer server.Close()
	home := setupHome(t, server.URL+"/api/alpha/decisions")
	inputPath := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(inputPath, []byte(inputJSON), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diag bytes.Buffer
	if code := run(context.Background(), []string{inputPath}, nil, &out, &diag); code != 0 || diag.Len() != 0 || out.String() != responseJSON+"\n" {
		t.Fatalf("code=%d out=%q diag=%q", code, out.String(), diag.String())
	}
	output := filepath.Join(t.TempDir(), "result.json")
	os.WriteFile(output, []byte("old"), 0644)
	out.Reset()
	if code := run(context.Background(), []string{"-", "-o", output}, strings.NewReader(inputJSON), &out, &diag); code != 0 || out.Len() != 0 {
		t.Fatalf("code=%d diag=%s", code, &diag)
	}
	data, _ := os.ReadFile(output)
	info, _ := os.Stat(output)
	if string(data) != responseJSON+"\n" || info.Mode().Perm() != 0600 {
		t.Fatal("bad output file")
	}
	if _, err := os.Stat(filepath.Join(home, "dialogs")); !os.IsNotExist(err) {
		t.Fatal("created sessions")
	}
}

func TestErrorsLeaveOutputUntouched(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500); fmt.Fprint(w, "decision-secret") }))
	defer server.Close()
	setupHome(t, server.URL)
	dir := t.TempDir()
	output := filepath.Join(dir, "result")
	os.WriteFile(output, []byte("old"), 0600)
	var out, diag bytes.Buffer
	if code := run(context.Background(), []string{"-", "-o", output}, strings.NewReader(inputJSON), &out, &diag); code != 1 || out.Len() != 0 || strings.Contains(diag.String(), "decision-secret") {
		t.Fatalf("code=%d out=%s diag=%s", code, &out, &diag)
	}
	data, _ := os.ReadFile(output)
	entries, _ := os.ReadDir(dir)
	if string(data) != "old" || len(entries) != 1 {
		t.Fatal("partial output")
	}
	if err := writeAtomic(dir, []byte("new")); err == nil {
		t.Fatal("accepted directory output")
	}
	entries, _ = os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("leaked temporary file")
	}
}

func TestLocalHelpAndMetadata(t *testing.T) {
	t.Setenv("HORIZON_HOME", filepath.Join(t.TempDir(), "missing"))
	for _, arg := range []string{"--help", "-h", "horizon-plugin-metadata"} {
		var out, diag bytes.Buffer
		if code := run(context.Background(), []string{arg}, nil, &out, &diag); code != 0 || out.Len() == 0 || diag.Len() != 0 {
			t.Fatalf("local command failed %d %s", code, &diag)
		}
	}
}

func TestHelpExampleIsAcceptedRequest(t *testing.T) {
	t.Setenv("HORIZON_HOME", filepath.Join(t.TempDir(), "missing"))
	var out, diag bytes.Buffer
	if code := run(context.Background(), []string{"--help"}, nil, &out, &diag); code != 0 || diag.Len() != 0 {
		t.Fatalf("help failed: code=%d diagnostics=%s", code, &diag)
	}
	help := out.String()
	start, end := strings.Index(help, "{\n"), strings.LastIndex(help, "\n}")
	if start < 0 || end < start {
		t.Fatal("help has no complete JSON example")
	}
	request, err := readRequest(strings.NewReader(help[start : end+2]))
	if err != nil {
		t.Fatalf("help example is not accepted: %v", err)
	}
	if q, ok := request.Questions["ready"]; !ok || q.Type != "noul" {
		t.Fatal("example lacks documented ready question")
	}
}
