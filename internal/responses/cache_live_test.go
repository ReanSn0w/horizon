package responses

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ReanSn0w/horizon/internal/config"
)

type cacheProbeTransport struct {
	sessionID    string
	generationID string
}

func (p *cacheProbeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if p.sessionID != "" {
		request.Header.Set("x-session-id", p.sessionID)
	}
	response, err := http.DefaultTransport.RoundTrip(request)
	if err == nil {
		p.generationID = response.Header.Get("X-Generation-Id")
	}
	return response, err
}

// Opt-in: twelve small paid calls; no prompt text, key or generation ID is logged.
func TestConfiguredPromptCacheRouting(t *testing.T) {
	if os.Getenv("HORIZON_CACHE_CHECK") != "1" {
		t.Skip("set HORIZON_CACHE_CHECK=1 to probe configured provider caching")
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
	endpoint, _ := cfg.Endpoint("responses")
	metaEndpoint, _ := cfg.Endpoint("generation")
	runID := time.Now().UTC().Format("20060102T150405.000000000")
	for _, phase := range []string{"implicit", "session_header", "explicit_breakpoint"} {
		body := strings.Repeat("alpha ", 1500) + phase + runID
		first := UserMessage(body)
		prefix := sha256.Sum256(append([]byte("Reply briefly."), first...))
		t.Logf("phase=%s stable_prefix_sha256=%s", phase, hex.EncodeToString(prefix[:]))
		probe := &cacheProbeTransport{}
		if phase != "implicit" {
			probe.sessionID = "horizon-cache-probe-" + hex.EncodeToString(prefix[:8])
		}
		client := &Client{ResponsesURL: endpoint, APIKey: cfg.Provider.Key, HTTPClient: &http.Client{Transport: probe}, MaxAttempts: 1}
		firstItem := first
		if phase == "explicit_breakpoint" {
			firstItem, _ = json.Marshal(map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": body, "prompt_cache_breakpoint": map[string]any{"mode": "explicit"}}}})
		}
		for i := 0; i < 4; i++ {
			request := Request{Model: model.Model, Instructions: "Reply briefly.", Input: []json.RawMessage{firstItem, UserMessage(fmt.Sprintf("Round %d: reply OK.", i%3))}, Tools: []Tool{}, Store: false}
			response, err := client.Stream(context.Background(), request, nil, nil)
			if err != nil {
				t.Fatalf("%s round %d: %v", phase, i, err)
			}
			usage := ParseUsage(response.Usage)
			if usage.InputTokens == nil {
				t.Fatalf("%s round %d: missing input usage", phase, i)
			}
			generationID := probe.generationID
			if generationID == "" {
				generationID = response.ID
			}
			provider, nativeCached := generationProbe(t, metaEndpoint, cfg.Provider.Key, generationID)
			cached := "unknown"
			if usage.CachedTokens != nil {
				cached = fmt.Sprint(*usage.CachedTokens)
			}
			t.Logf("phase=%s round=%d input=%d cached=%s provider=%s native_cached=%s", phase, i, *usage.InputTokens, cached, provider, nativeCached)
		}
	}
}

func generationProbe(t *testing.T, endpoint, key, id string) (string, string) {
	t.Helper()
	if !strings.HasPrefix(id, "gen-") {
		return "unavailable", "unknown"
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "unavailable", "unknown"
	}
	query := parsed.Query()
	query.Set("id", id)
	parsed.RawQuery = query.Encode()
	request, err := http.NewRequest(http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "unavailable", "unknown"
	}
	request.Header.Set("Authorization", "Bearer "+key)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "unavailable", "unknown"
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "unavailable", "unknown"
	}
	var value struct {
		Data struct {
			Provider string  `json:"provider_name"`
			Cached   *uint64 `json:"native_tokens_cached"`
		} `json:"data"`
	}
	if json.NewDecoder(response.Body).Decode(&value) != nil {
		return "unavailable", "unknown"
	}
	cached := "unknown"
	if value.Data.Cached != nil {
		cached = fmt.Sprint(*value.Data.Cached)
	}
	return value.Data.Provider, cached
}
