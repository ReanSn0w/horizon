package responses

import (
	"encoding/json"
	"testing"
)

func TestParseUsagePreservesMissingAndZeroCounters(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		raw                   string
		input, output, cached *uint64
	}{
		{name: "responses", raw: `{"input_tokens":1500,"output_tokens":12,"input_tokens_details":{"cached_tokens":0}}`, input: number(1500), output: number(12), cached: number(0)},
		{name: "chat style", raw: `{"prompt_tokens":2048,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":1024}}`, input: number(2048), output: number(4), cached: number(1024)},
		{name: "missing cached", raw: `{"input_tokens":50}`, input: number(50)},
		{name: "null", raw: `null`},
		{name: "malformed", raw: `{"input_tokens":"secret"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseUsage(json.RawMessage(tc.raw))
			if !equalNumber(got.InputTokens, tc.input) || !equalNumber(got.OutputTokens, tc.output) || !equalNumber(got.CachedTokens, tc.cached) {
				t.Fatalf("ParseUsage() = %+v", got)
			}
		})
	}
}

func number(value uint64) *uint64 { return &value }

func equalNumber(a, b *uint64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
