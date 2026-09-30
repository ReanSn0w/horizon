package responses

import (
	"encoding/json"
	"fmt"
)

type Tool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Strict      bool           `json:"strict"`
	Parameters  map[string]any `json:"parameters"`
}

type Request struct {
	Model             string            `json:"model"`
	Instructions      string            `json:"instructions"`
	Input             []json.RawMessage `json:"input"`
	Tools             []Tool            `json:"tools"`
	ParallelToolCalls bool              `json:"parallel_tool_calls"`
	Stream            bool              `json:"stream"`
	Store             bool              `json:"store"`
	Include           []string          `json:"include,omitempty"`
	Reasoning         *Reasoning        `json:"reasoning,omitempty"`
	ContextManagement []ContextPolicy   `json:"context_management"`
}

type Reasoning struct {
	Effort string `json:"effort"`
}

type ContextPolicy struct {
	Type             string `json:"type"`
	CompactThreshold int    `json:"compact_threshold"`
}

type CompactRequest struct {
	Model        string            `json:"model"`
	Instructions string            `json:"instructions"`
	Input        []json.RawMessage `json:"input"`
}

type Response struct {
	ID                string            `json:"id"`
	Status            string            `json:"status"`
	Output            []json.RawMessage `json:"output"`
	Error             json.RawMessage   `json:"error,omitempty"`
	IncompleteDetails json.RawMessage   `json:"incomplete_details,omitempty"`
	Usage             json.RawMessage   `json:"usage,omitempty"`
}

type Event struct {
	Type  string
	Delta string
	Raw   json.RawMessage
}

type FunctionCall struct {
	CallID    string
	Name      string
	Arguments json.RawMessage
}

func FunctionCalls(items []json.RawMessage) ([]FunctionCall, error) {
	var calls []FunctionCall
	for _, item := range items {
		var header struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}
		if err := json.Unmarshal(item, &header); err != nil {
			return nil, fmt.Errorf("decode response output item: %w", err)
		}
		if header.Type != "function_call" {
			continue
		}
		if header.CallID == "" || header.Name == "" || !json.Valid([]byte(header.Arguments)) {
			return nil, fmt.Errorf("invalid completed function call %q", header.Name)
		}
		calls = append(calls, FunctionCall{CallID: header.CallID, Name: header.Name, Arguments: json.RawMessage(header.Arguments)})
	}
	return calls, nil
}

func OutputText(items []json.RawMessage) (string, error) {
	var result string
	for _, item := range items {
		var message struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(item, &message); err != nil {
			return "", fmt.Errorf("decode response output item: %w", err)
		}
		if message.Type != "message" {
			continue
		}
		for _, content := range message.Content {
			if content.Type == "output_text" {
				result += content.Text
			}
		}
	}
	return result, nil
}

func UserMessage(text string) json.RawMessage {
	data, _ := json.Marshal(map[string]any{"role": "user", "content": text})
	return data
}

func DeveloperMessage(text string) json.RawMessage {
	data, _ := json.Marshal(map[string]any{"role": "developer", "content": text})
	return data
}

func FunctionOutput(callID string, result json.RawMessage) (json.RawMessage, error) {
	if !json.Valid(result) {
		return nil, fmt.Errorf("tool result for %s is not valid JSON", callID)
	}
	return json.Marshal(map[string]any{"type": "function_call_output", "call_id": callID, "output": string(result)})
}
