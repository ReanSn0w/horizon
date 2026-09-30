package responses

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const maxErrorBody = 64 << 10

type Error struct {
	Code       string
	Message    string
	StatusCode int
	Retryable  bool
	Started    bool
	Cause      error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }

type Client struct {
	ResponsesURL string
	CompactURL   string
	APIKey       string
	HTTPClient   *http.Client
	MaxAttempts  int
	RetryDelay   func(int) time.Duration
}

func (c *Client) Stream(ctx context.Context, request Request, beforeAttempt func() error, publish func(Event)) (*Response, error) {
	request.Stream = true
	return c.retry(ctx, beforeAttempt, func() (*Response, *Error) {
		return c.streamOnce(ctx, request, publish)
	})
}

func (c *Client) Compact(ctx context.Context, request CompactRequest, beforeAttempt func() error) (*Response, error) {
	return c.retry(ctx, beforeAttempt, func() (*Response, *Error) {
		return c.compactOnce(ctx, request)
	})
}

func (c *Client) retry(ctx context.Context, beforeAttempt func() error, invoke func() (*Response, *Error)) (*Response, error) {
	maxAttempts := c.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if beforeAttempt != nil {
			if err := beforeAttempt(); err != nil {
				return nil, err
			}
		}
		response, callErr := invoke()
		if callErr == nil {
			return response, nil
		}
		if !callErr.Retryable || callErr.Started || attempt == maxAttempts {
			return nil, callErr
		}
		delay := time.Duration(attempt) * 250 * time.Millisecond
		if c.RetryDelay != nil {
			delay = c.RetryDelay(attempt)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	panic("unreachable")
}

func (c *Client) streamOnce(ctx context.Context, request Request, publish func(Event)) (*Response, *Error) {
	response, callErr := c.post(ctx, c.ResponsesURL, request, "text/event-stream")
	if callErr != nil {
		return nil, callErr
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, c.httpError(response)
	}
	if !strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		return nil, &Error{Code: "incompatible_endpoint", Message: "provider did not return a Responses SSE stream", StatusCode: response.StatusCode}
	}

	reader := bufio.NewReader(response.Body)
	started := false
	for {
		data, err := readSSEData(reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, &Error{Code: "incomplete_response", Message: "response stream ended before a terminal event", Started: started}
			}
			return nil, &Error{Code: "stream_error", Message: fmt.Sprintf("read response stream: %v", err), Retryable: !started, Started: started}
		}
		if string(data) == "[DONE]" {
			return nil, &Error{Code: "incomplete_response", Message: "response stream ended without response.completed", Started: started}
		}
		var envelope struct {
			Type     string    `json:"type"`
			Delta    string    `json:"delta"`
			Message  string    `json:"message"`
			Response *Response `json:"response"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			return nil, &Error{Code: "stream_error", Message: fmt.Sprintf("decode response event: %v", err), Started: started}
		}
		started = true
		if publish != nil {
			publish(Event{Type: envelope.Type, Delta: envelope.Delta, Raw: append(json.RawMessage(nil), data...)})
		}
		switch envelope.Type {
		case "response.completed":
			if envelope.Response == nil || envelope.Response.Status != "completed" {
				return nil, &Error{Code: "incomplete_response", Message: "response.completed did not contain a completed response", Started: true}
			}
			return envelope.Response, nil
		case "response.failed":
			return nil, terminalError("response_failed", "model response failed", envelope.Response)
		case "response.incomplete":
			return nil, terminalError("incomplete_response", "model response was incomplete", envelope.Response)
		case "error":
			if envelope.Message == "" {
				envelope.Message = "provider returned a streaming error"
			}
			return nil, &Error{Code: "api_error", Message: envelope.Message, Started: true}
		}
	}
}

func (c *Client) compactOnce(ctx context.Context, request CompactRequest) (*Response, *Error) {
	response, callErr := c.post(ctx, c.CompactURL, request, "application/json")
	if callErr != nil {
		return nil, callErr
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, c.httpError(response)
	}
	var result Response
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<20))
	if err := decoder.Decode(&result); err != nil {
		return nil, &Error{Code: "invalid_response", Message: fmt.Sprintf("decode compact response: %v", err)}
	}
	if len(result.Output) == 0 {
		return nil, &Error{Code: "incompatible_endpoint", Message: "compact response did not contain an output window"}
	}
	return &result, nil
}

func (c *Client) post(ctx context.Context, endpoint string, value any, accept string) (*http.Response, *Error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, &Error{Code: "request_encoding", Message: fmt.Sprintf("encode API request: %v", err)}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, &Error{Code: "invalid_endpoint", Message: fmt.Sprintf("create API request: %v", err)}
	}
	request.Header.Set("Authorization", "Bearer "+c.APIKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", accept)
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &Error{Code: "cancelled", Message: ctx.Err().Error(), Cause: ctx.Err()}
		}
		return nil, &Error{Code: "network_error", Message: fmt.Sprintf("request provider: %v", err), Retryable: true}
	}
	return response, nil
}

func (c *Client) httpError(response *http.Response) *Error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = response.Status
	}
	code := "api_error"
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		code, message = "authentication_error", "provider rejected the configured API credentials"
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooManyRequests:
		code = "temporary_api_error"
	case http.StatusNotFound:
		code = "incompatible_endpoint"
	case http.StatusBadRequest:
		lower := strings.ToLower(message)
		switch {
		case strings.Contains(lower, "context") && (strings.Contains(lower, "length") || strings.Contains(lower, "token")):
			code = "context_length_exceeded"
		case strings.Contains(lower, "context_management"), strings.Contains(lower, "compact_threshold"), strings.Contains(lower, "stream"):
			code = "incompatible_endpoint"
		}
	}
	retryable := response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusConflict || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
	return &Error{Code: code, Message: fmt.Sprintf("provider returned HTTP %d: %s", response.StatusCode, message), StatusCode: response.StatusCode, Retryable: retryable}
}

func readSSEData(reader *bufio.Reader) ([]byte, error) {
	var data []byte
	for {
		line, err := reader.ReadString('\n')
		if err != nil && len(line) == 0 {
			return nil, err
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			if len(data) != 0 {
				return data, nil
			}
		} else if strings.HasPrefix(line, "data:") {
			part := strings.TrimPrefix(line, "data:")
			part = strings.TrimPrefix(part, " ")
			if len(data) != 0 {
				data = append(data, '\n')
			}
			data = append(data, part...)
		}
		if err != nil {
			if len(data) != 0 {
				return data, nil
			}
			return nil, err
		}
	}
}

func terminalError(code, fallback string, response *Response) *Error {
	message := fallback
	if response != nil {
		if len(response.Error) != 0 && string(response.Error) != "null" {
			message += ": " + string(response.Error)
		} else if len(response.IncompleteDetails) != 0 && string(response.IncompleteDetails) != "null" {
			message += ": " + string(response.IncompleteDetails)
		}
	}
	return &Error{Code: code, Message: message, Started: true}
}
