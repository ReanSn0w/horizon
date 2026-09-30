package decision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

const maxRequestBytes = 32 * 1024
const maxResponseBytes = 1024 * 1024

type Question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

type Request struct {
	State     any                 `json:"state"`
	Questions map[string]Question `json:"questions"`
}

type Answer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

type Response struct {
	ID       string            `json:"id"`
	Model    string            `json:"model"`
	Provider string            `json:"provider"`
	Answers  map[string]Answer `json:"answers"`
	Usage    struct {
		Cost float64 `json:"cost"`
	} `json:"usage"`
}

type Client struct {
	Endpoint   string
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

func (c *Client) Decide(ctx context.Context, value Request) (Response, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if c == nil || c.Endpoint == "" || c.APIKey == "" || c.Model == "" {
		return Response{}, errors.New("decision client is not configured")
	}
	if len(value.Questions) == 0 {
		return Response{}, errors.New("decision questions are required")
	}
	for id, question := range value.Questions {
		if id == "" || question.Instructions == "" || (question.Type != "noul" && question.Type != "choice" && question.Type != "score") {
			return Response{}, errors.New("invalid decision question")
		}
	}
	payload, err := json.Marshal(struct {
		Model string `json:"model"`
		Request
	}{Model: c.Model, Request: value})
	if err != nil {
		return Response{}, fmt.Errorf("encode decision request: %w", err)
	}
	if len(payload) > maxRequestBytes {
		return Response{}, errors.New("decision request is too large")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return Response{}, fmt.Errorf("prepare decision request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	transport := c.HTTPClient
	if transport == nil {
		transport = http.DefaultClient
	}
	resp, err := transport.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("request decision provider: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Response{}, fmt.Errorf("decision provider returned HTTP %d", resp.StatusCode)
	}
	var answer Response
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err := decoder.Decode(&answer); err != nil {
		return Response{}, fmt.Errorf("decode decision response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Response{}, errors.New("decision response contains trailing data")
	}
	if answer.ID == "" || len(answer.Answers) == 0 {
		return Response{}, errors.New("decision response is incomplete")
	}
	for id, question := range value.Questions {
		item, ok := answer.Answers[id]
		if !ok || item.Type != question.Type || !validAnswer(item) {
			return Response{}, fmt.Errorf("decision response has invalid answer for %q", id)
		}
	}
	return answer, nil
}

func validAnswer(value Answer) bool {
	switch value.Type {
	case "noul":
		return value.Noul != nil && validProbability(*value.Noul)
	case "choice":
		if value.Choice == "" || value.Confidence == nil || !validProbability(*value.Confidence) || !validProbabilities(value.Probabilities) {
			return false
		}
		_, ok := value.Probabilities[value.Choice]
		return ok
	case "score":
		return value.Score != nil && !math.IsNaN(*value.Score) && !math.IsInf(*value.Score, 0) && value.Confidence != nil && validProbability(*value.Confidence) && validProbabilities(value.Probabilities)
	default:
		return false
	}
}

func validProbabilities(values map[string]float64) bool {
	if len(values) == 0 {
		return false
	}
	for _, value := range values {
		if !validProbability(value) {
			return false
		}
	}
	return true
}

func validProbability(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}
