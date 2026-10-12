package responses

import "encoding/json"

// UsageMetrics contains only numeric counters safe to put in diagnostics.
// A nil field means the provider did not supply that counter.
type UsageMetrics struct {
	InputTokens  *uint64
	OutputTokens *uint64
	CachedTokens *uint64
}

func ParseUsage(raw json.RawMessage) UsageMetrics {
	var value struct {
		InputTokens       *uint64 `json:"input_tokens"`
		OutputTokens      *uint64 `json:"output_tokens"`
		CachedTokens      *uint64 `json:"cached_tokens"`
		InputTokenDetails *struct {
			CachedTokens *uint64 `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return UsageMetrics{}
	}
	result := UsageMetrics{InputTokens: value.InputTokens, OutputTokens: value.OutputTokens, CachedTokens: value.CachedTokens}
	if value.InputTokenDetails != nil && value.InputTokenDetails.CachedTokens != nil {
		result.CachedTokens = value.InputTokenDetails.CachedTokens
	}
	return result
}
