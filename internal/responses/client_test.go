package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ReanSn0w/horizon/internal/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestSerializedContinuationKeepsPrefixAndUsageCounters(t *testing.T) {
	user := UserMessage("inspect")
	opaque := json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque","future":{"x":1}}`)
	tools := []Tool{{Type: "function", Name: "skill_read", Strict: true, Parameters: map[string]any{"type": "object"}}}
	var sent []Request
	client := &Client{ResponsesURL: "https://example.test/responses", APIKey: "secret", MaxAttempts: 1,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(request.Body)
			if err != nil {
				return nil, err
			}
			var decoded Request
			if err := json.Unmarshal(body, &decoded); err != nil {
				return nil, err
			}
			sent = append(sent, decoded)
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":1539,\"output_tokens\":5,\"input_tokens_details\":{\"cached_tokens\":1523}},\"output\":[]}}\n\n"))}, nil
		})}}
	for _, input := range [][]json.RawMessage{{user}, {user, opaque}} {
		response, err := client.Stream(context.Background(), Request{Model: "m", Instructions: "stable", Input: input, Tools: tools, Store: false}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		usage := ParseUsage(response.Usage)
		if usage.InputTokens == nil || *usage.InputTokens != 1539 || usage.CachedTokens == nil || *usage.CachedTokens != 1523 {
			t.Fatalf("usage = %+v", usage)
		}
	}
	if len(sent) != 2 || sent[0].Instructions != sent[1].Instructions || !bytes.Equal(sent[0].Input[0], sent[1].Input[0]) || !bytes.Equal(sent[1].Input[1], opaque) {
		t.Fatalf("serialized prefix changed: %+v", sent)
	}
	firstTools, _ := json.Marshal(sent[0].Tools)
	nextTools, _ := json.Marshal(sent[1].Tools)
	if !bytes.Equal(firstTools, nextTools) || sent[0].Store || sent[1].Store {
		t.Fatalf("tool definitions or store changed: %+v", sent)
	}
}

func TestStreamRetriesBeforeEventsAndPreservesOpaqueOutput(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempts++
		if request.Header.Get("HTTP-Referer") != "" || request.Header.Get("X-OpenRouter-Title") != "" {
			t.Fatal("OpenRouter attribution headers were sent to another provider")
		}
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("authorization header = %q", request.Header.Get("Authorization"))
		}
		if attempts == 1 {
			http.Error(writer, "temporary", http.StatusInternalServerError)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"future\":{\"x\":1},\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n")
	}))
	defer server.Close()
	client := &Client{ResponsesURL: server.URL, APIKey: "secret", MaxAttempts: 3, RetryDelay: func(int) time.Duration { return 0 }}
	counted := 0
	var deltas string
	response, err := client.Stream(context.Background(), Request{Model: "m", Input: []json.RawMessage{UserMessage("hi")}}, func() error {
		counted++
		return nil
	}, func(event Event) { deltas += event.Delta })
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || counted != 2 || deltas != "ok" {
		t.Fatalf("attempts=%d counted=%d deltas=%q", attempts, counted, deltas)
	}
	if len(response.Output) != 1 || !bytes.Contains(response.Output[0], []byte(`"future":{"x":1}`)) {
		t.Fatalf("opaque output was lost: %s", response.Output)
	}
}

func TestAppAttributionHeadersOnResponsesAndCompact(t *testing.T) {
	var paths []string
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		paths = append(paths, request.URL.Path)
		if request.URL.Hostname() != "openrouter.ai" || request.Header.Get("HTTP-Referer") != openRouterAppURL || request.Header.Get("X-OpenRouter-Title") != openRouterAppTitle {
			t.Errorf("attribution headers: referer=%q title=%q", request.Header.Get("HTTP-Referer"), request.Header.Get("X-OpenRouter-Title"))
		}
		if request.URL.Path == "/api/v1/responses/compact" {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"status":"completed","output":[{"type":"compaction","id":"compact-1"}]}`))}, nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n"))}, nil
	})
	client := &Client{ResponsesURL: "https://openrouter.ai/api/v1/responses", CompactURL: "https://openrouter.ai/api/v1/responses/compact", APIKey: "secret", HTTPClient: &http.Client{Transport: transport}}
	if _, err := client.Stream(context.Background(), Request{Model: "m"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Compact(context.Background(), CompactRequest{Model: "m"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "/api/v1/responses" || paths[1] != "/api/v1/responses/compact" {
		t.Fatalf("paths = %v", paths)
	}
}

func TestStreamDoesNotRetryPartialResponse(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts++
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\"}}\n\n")
	}))
	defer server.Close()
	client := &Client{ResponsesURL: server.URL, APIKey: "secret", MaxAttempts: 3, RetryDelay: func(int) time.Duration { return 0 }}
	_, err := client.Stream(context.Background(), Request{Model: "m"}, nil, nil)
	var apiError *Error
	if !errors.As(err, &apiError) || apiError.Code != "incomplete_response" || attempts != 1 {
		t.Fatalf("partial stream err=%v attempts=%d", err, attempts)
	}
}

func TestProviderDiagnostics(t *testing.T) {
	tests := []struct {
		status int
		body   string
		code   string
	}{
		{http.StatusUnauthorized, "bad key", "authentication_error"},
		{http.StatusBadRequest, `unknown parameter context_management`, "incompatible_endpoint"},
		{http.StatusBadRequest, `maximum context length exceeded`, "context_length_exceeded"},
	}
	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				http.Error(writer, test.body, test.status)
			}))
			defer server.Close()
			client := &Client{ResponsesURL: server.URL, APIKey: "never-print-this", MaxAttempts: 1}
			_, err := client.Stream(context.Background(), Request{Model: "m"}, nil, nil)
			var apiError *Error
			if !errors.As(err, &apiError) || apiError.Code != test.code || strings.Contains(err.Error(), "never-print-this") {
				t.Fatalf("diagnostic = %v", err)
			}
		})
	}
}

func TestRetryWaitIsCancelled(t *testing.T) {
	hit := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		hit <- struct{}{}
		http.Error(writer, "temporary", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client := &Client{ResponsesURL: server.URL, APIKey: "secret", MaxAttempts: 3, RetryDelay: func(int) time.Duration { return time.Hour }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := client.Stream(ctx, Request{Model: "m"}, nil, nil)
		done <- err
	}()
	<-hit
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled retry error = %v", err)
	}
}

func TestCompactReturnsCanonicalWholeWindow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/responses/compact" {
			t.Errorf("compact path = %q", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"id":"cmp-response","object":"response.compaction","output":[{"role":"user","content":"retained"},{"type":"compaction","id":"cmp-1","encrypted_content":"opaque"}]}`)
	}))
	defer server.Close()
	client := &Client{CompactURL: server.URL + "/responses/compact", APIKey: "secret", MaxAttempts: 1}
	response, err := client.Compact(context.Background(), CompactRequest{Model: "m", Input: []json.RawMessage{UserMessage("long")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Output) != 2 || !bytes.Contains(response.Output[0], []byte("retained")) || !bytes.Contains(response.Output[1], []byte("opaque")) {
		t.Fatalf("compact output = %s", response.Output)
	}
}

func TestConfiguredEndpointCompatibility(t *testing.T) {
	if os.Getenv("HORIZON_API_CHECK") != "1" {
		t.Skip("set HORIZON_API_CHECK=1 to verify the configured provider")
	}
	home, err := config.ResolveHome("")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	_, model, err := cfg.SelectModel("")
	if err != nil {
		t.Fatal(err)
	}
	responsesURL, _ := cfg.Endpoint("responses")
	compactURL, _ := cfg.Endpoint("responses/compact")
	client := &Client{ResponsesURL: responsesURL, CompactURL: compactURL, APIKey: cfg.Provider.Key, MaxAttempts: 1}
	request := Request{
		Model: model.Model, Instructions: "Reply briefly.", Input: []json.RawMessage{UserMessage(strings.Repeat("alpha ", 1500) + "Reply with OK.")},
		Tools: []Tool{}, Store: false, Include: []string{"reasoning.encrypted_content"},
		ContextManagement: []ContextPolicy{{Type: "compaction", CompactThreshold: 1000}},
	}
	response, err := client.Stream(context.Background(), request, nil, nil)
	if err != nil {
		t.Fatalf("configured /responses endpoint is incompatible: %v", err)
	}
	automatic := false
	for _, item := range response.Output {
		var header struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(item, &header) == nil && header.Type == "compaction" {
			automatic = true
		}
	}
	var usage struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal(response.Usage, &usage); err != nil {
		t.Fatalf("decode usage: %v", err)
	}
	t.Logf("automatic_compaction_item=%t input_tokens=%d threshold=%d", automatic, usage.InputTokens, request.ContextManagement[0].CompactThreshold)
	window := append(request.Input, response.Output...)
	compacted, err := client.Compact(context.Background(), CompactRequest{Model: model.Model, Instructions: request.Instructions, Input: window}, nil)
	if err != nil {
		t.Fatalf("configured /responses/compact endpoint is incompatible: %v", err)
	}
	found := false
	for _, item := range compacted.Output {
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(item, &header); err != nil {
			t.Fatal(err)
		}
		found = found || header.Type == "compaction"
	}
	if !found {
		t.Fatal("compact response contains no compaction item")
	}
}
