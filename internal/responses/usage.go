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
		PromptTokens      *uint64 `json:"prompt_tokens"`
		CompletionTokens  *uint64 `json:"completion_tokens"`
		CachedTokens      *uint64 `json:"cached_tokens"`
		InputTokenDetails *struct {
			CachedTokens *uint64 `json:"cached_tokens"`
		} `json:"input_tokens_details"`
		PromptTokenDetails *struct {
			CachedTokens *uint64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return UsageMetrics{}
	}
	result := UsageMetrics{InputTokens: value.InputTokens, OutputTokens: value.OutputTokens, CachedTokens: value.CachedTokens}
	if result.InputTokens == nil {
		result.InputTokens = value.PromptTokens
	}
	if result.OutputTokens == nil {
		result.OutputTokens = value.CompletionTokens
	}
	if value.InputTokenDetails != nil && value.InputTokenDetails.CachedTokens != nil {
		result.CachedTokens = value.InputTokenDetails.CachedTokens
	} else if value.PromptTokenDetails != nil && value.PromptTokenDetails.CachedTokens != nil {
		result.CachedTokens = value.PromptTokenDetails.CachedTokens
	}
	return result
}
