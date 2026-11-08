package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltDecisionCommand(t *testing.T) {
	binary := buildBinary(t)
	home, workspace := t.TempDir(), t.TempDir()
	if out, diag, err := run(binary, workspace, "", "--home", home, "decision", "--help"); err != nil || diag != "" || !strings.Contains(out, "Usage:") {
		t.Fatalf("help: %v %q %q", err, out, diag)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" || r.Header.Get("Authorization") != "Bearer local-secret" {
			t.Error("incorrect endpoint or key")
		}
		writeDecisionResult(w, "local", map[string]any{"ready": true})
	}))
	defer server.Close()
	cfg := fmt.Sprintf("mode: unit\nproviders:\n  default:\n    url: %s\n    key: local-secret\ndefault_model: decision\ndecision_model: decision\nmodels:\n  decision:\n    provider: default\n    model: test-decision-model\n    compact_threshold: 1000\n", server.URL)
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	input := `{"state":{},"questions":{"ready":{"instructions":"Ready?"}}}`
	out, diag, err := run(binary, workspace, input, "--home", home, "decision", "-")
	if err != nil || diag != "" || !strings.Contains(out, `"id":"local"`) {
		t.Fatalf("decision: %v %q %q", err, out, diag)
	}
	if _, err := os.Stat(filepath.Join(home, "dialogs")); !os.IsNotExist(err) {
		t.Fatal("decision created session")
	}
}

func readDecisionState(r *http.Request, state any) (map[string]json.RawMessage, error) {
	var wire struct {
		Input []struct {
			Content string `json:"content"`
		} `json:"input"`
		Text struct {
			Format struct {
				Schema struct {
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"schema"`
			} `json:"format"`
		} `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
		return nil, err
	}
	if len(wire.Input) != 1 {
		return nil, fmt.Errorf("expected one state message")
	}
	if err := json.Unmarshal([]byte(wire.Input[0].Content), state); err != nil {
		return nil, err
	}
	return wire.Text.Format.Schema.Properties, nil
}

func writeDecisionResult(w http.ResponseWriter, id string, answers map[string]any) {
	data, _ := json.Marshal(answers)
	event, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"id": id, "status": "completed", "output": message(string(data))}})
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "data: %s\n\n", event)
}
