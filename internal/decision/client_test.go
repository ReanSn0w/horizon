package decision

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ReanSn0w/horizon/internal/responses"
)

func decisionEvent(w http.ResponseWriter, status, text string) {
	output, _ := json.Marshal(map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": text}}})
	event, _ := json.Marshal(map[string]any{"type": "response." + status, "response": responses.Response{ID: "decision-1", Status: status, Output: []json.RawMessage{output}}})
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "data: %s\n\n", event)
}

func TestClientSendsStructuredResponsesRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer decision-key" {
			t.Error("wrong request configuration")
		}
		var request responses.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "ordinary-model" || request.Store || !request.Stream || request.ParallelToolCalls || len(request.Tools) != 0 || request.Reasoning == nil || request.Reasoning.Effort != "none" || request.MaxOutputTokens <= 0 {
			t.Errorf("request=%+v", request)
		}
		if request.Text == nil {
			t.Fatal("missing structured output")
		}
		format := request.Text.Format
		if format.Type != "json_schema" || !format.Strict || format.Schema["additionalProperties"] != false {
			t.Fatalf("format=%+v", format)
		}
		props := format.Schema["properties"].(map[string]any)
		if len(props) != 1 || props["allowed"].(map[string]any)["type"] != "boolean" {
			t.Fatal(props)
		}
		required := format.Schema["required"].([]any)
		if len(required) != 1 || required[0] != "allowed" {
			t.Fatal(required)
		}
		var input struct{ Role, Content string }
		if len(request.Input) != 1 || json.Unmarshal(request.Input[0], &input) != nil || input.Role != "user" || input.Content != `{"command":"pwd"}` {
			t.Fatal("state changed")
		}
		decisionEvent(w, "completed", `{"allowed":true}`)
	}))
	defer server.Close()
	client := Client{Endpoint: server.URL + "/v1/responses", APIKey: "decision-key", Model: "ordinary-model", HTTPClient: server.Client()}
	response, err := client.Decide(context.Background(), Request{State: map[string]string{"command": "pwd"}, Questions: map[string]Question{"allowed": {Instructions: "Allowed?"}}})
	if err != nil || response.ID != "decision-1" || response.Answers["allowed"] == nil || !*response.Answers["allowed"] {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestClientFailsClosedOnMalformedAnswers(t *testing.T) {
	for name, body := range map[string]string{
		"missing": `{}`, "extra": `{"allowed":false,"other":true}`, "null": `{"allowed":null}`, "string": `{"allowed":"false"}`, "number": `{"allowed":0}`, "probability": `{"allowed":{"noul":0.1}}`, "duplicate": `{"allowed":false,"allowed":true}`, "trailing": `{"allowed":false} {}`, "array": `[false]`, "invalid": "not json",
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { decisionEvent(w, "completed", body) }))
			defer server.Close()
			client := Client{Endpoint: server.URL, APIKey: "key", Model: "model"}
			if _, err := client.Decide(context.Background(), Request{Questions: map[string]Question{"allowed": {Instructions: "Allowed?"}}}); err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
}

func TestClientFailsClosedOnProviderErrors(t *testing.T) {
	for _, kind := range []string{"http", "incomplete", "refusal", "tool", "truncated", "empty id", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "text/event-stream")
				switch kind {
				case "http":
					http.Error(w, "sensitive secret-key", 429)
				case "incomplete":
					decisionEvent(w, "incomplete", `{"allowed":false}`)
				case "refusal":
					fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"x\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"refusal\",\"refusal\":\"no\"}]}]}}\n\n")
				case "tool":
					fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"x\",\"status\":\"completed\",\"output\":[{\"type\":\"function_call\"}]}}\n\n")
				case "truncated":
					fmt.Fprint(w, "data: {\"type\":\"response.created\"}\n\n")
				case "oversized":
					fmt.Fprint(w, "data: "+strings.Repeat(" ", 1024*1024)+"\n\n")
				case "empty id":
					fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
				}
			}))
			defer server.Close()
			client := Client{Endpoint: server.URL, APIKey: "secret-key", Model: "model"}
			_, err := client.Decide(context.Background(), Request{Questions: map[string]Question{"allowed": {Instructions: "Allowed?"}}})
			if err == nil || strings.Contains(err.Error(), "sensitive") || strings.Contains(err.Error(), "secret-key") || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestClientRejectsCancelledAndOversizedRequest(t *testing.T) {
	client := Client{Endpoint: "https://example.test/v1/responses", APIKey: "secret", Model: "model"}
	questions := map[string]Question{"allowed": {Instructions: "Allowed?"}}
	if _, err := client.Decide(context.Background(), Request{State: strings.Repeat("x", maxRequestBytes), Questions: questions}); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Decide(ctx, Request{State: "short", Questions: questions}); err != context.Canceled {
		t.Fatalf("err=%v", err)
	}
}
