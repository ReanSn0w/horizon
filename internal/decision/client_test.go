package decision

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientSendsDecisionsRequestAndValidatesAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/alpha/decisions" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer decision-key" {
			t.Errorf("request: %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		var payload struct {
			Model     string              `json:"model"`
			State     map[string]any      `json:"state"`
			Questions map[string]Question `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Model != "typesafe/jev-1.13" || payload.State["command"] != "pwd" || payload.Questions["allowed"].Type != "noul" {
			t.Errorf("payload=%+v err=%v", payload, err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"decision-1","model":"typesafe/jev-1.13","provider":"TypeSafe","answers":{"allowed":{"type":"noul","noul":0.98}},"usage":{"cost":0.00001}}`))
	}))
	defer server.Close()
	client := &Client{Endpoint: server.URL + "/api/alpha/decisions", APIKey: "decision-key", Model: "typesafe/jev-1.13", HTTPClient: server.Client()}
	response, err := client.Decide(context.Background(), Request{State: map[string]any{"command": "pwd"}, Questions: map[string]Question{"allowed": {Type: "noul", Instructions: "Is this allowed?"}}})
	if err != nil || response.ID != "decision-1" || *response.Answers["allowed"].Noul != 0.98 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestClientFailsClosedOnProviderAndShapeErrors(t *testing.T) {
	for name, body := range map[string]string{
		"missing answer":  `{"id":"x","answers":{"other":{"type":"noul","noul":0.9}}}`,
		"wrong type":      `{"id":"x","answers":{"allowed":{"type":"choice","choice":"yes","confidence":0.9,"probabilities":{"yes":0.9}}}}`,
		"bad probability": `{"id":"x","answers":{"allowed":{"type":"noul","noul":1.2}}}`,
		"trailing":        `{"id":"x","answers":{"allowed":{"type":"noul","noul":0.9}}} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			defer server.Close()
			client := &Client{Endpoint: server.URL, APIKey: "key", Model: "model"}
			_, err := client.Decide(context.Background(), Request{State: "test", Questions: map[string]Question{"allowed": {Type: "noul", Instructions: "Allowed?"}}})
			if err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "sensitive provider error", http.StatusTooManyRequests)
	}))
	defer server.Close()
	client := &Client{Endpoint: server.URL, APIKey: "secret-key", Model: "model"}
	_, err := client.Decide(context.Background(), Request{State: "test", Questions: map[string]Question{"allowed": {Type: "noul", Instructions: "Allowed?"}}})
	if err == nil || !strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "sensitive") || strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("provider error=%v", err)
	}
}

func TestClientRejectsCancelledAndOversizedRequest(t *testing.T) {
	client := &Client{Endpoint: "https://example.test/api/alpha/decisions", APIKey: "secret", Model: "model"}
	questions := map[string]Question{"allowed": {Type: "noul", Instructions: "Allowed?"}}
	if _, err := client.Decide(context.Background(), Request{State: strings.Repeat("x", maxRequestBytes), Questions: questions}); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("large request error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Decide(ctx, Request{State: "short", Questions: questions}); err == nil {
		t.Fatal("cancelled request accepted")
	}
}

func BenchmarkClientDecideLocal(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"decision-1","answers":{"allowed":{"type":"noul","noul":0.98}}}`))
	}))
	defer server.Close()
	client := &Client{Endpoint: server.URL, APIKey: "key", Model: "model", HTTPClient: server.Client()}
	request := Request{State: map[string]string{"command": "pwd"}, Questions: map[string]Question{"allowed": {Type: "noul", Instructions: "Allowed?"}}}
	b.ResetTimer()
	for range b.N {
		if _, err := client.Decide(context.Background(), request); err != nil {
			b.Fatal(err)
		}
	}
}
