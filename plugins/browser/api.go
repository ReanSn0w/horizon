package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type browserSession struct {
	ID        string            `json:"id"`
	Status    string            `json:"status"`
	CDPURL    string            `json:"cdpUrl"`
	StartedAt string            `json:"startedAt"`
	TimeoutAt string            `json:"timeoutAt"`
	Metadata  map[string]string `json:"metadata"`
}

type browserAPI struct {
	baseURL string
	key     string
	client  *http.Client
}

func newBrowserAPI(s settings) browserAPI {
	return browserAPI{baseURL: strings.TrimRight(s.APIURL, "/"), key: s.APIKey, client: &http.Client{Timeout: 30 * time.Second}}
}

func (a browserAPI) do(ctx context.Context, method, path string, body any, target any) error {
	var input io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode browser request: %w", err)
		}
		input = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, input)
	if err != nil {
		return fmt.Errorf("prepare Browser Use request")
	}
	req.Header.Set("X-Browser-Use-API-Key", a.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("Browser Use request outcome unknown")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Browser Use HTTP %d", response.StatusCode)
	}
	if target == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target); err != nil {
		return fmt.Errorf("invalid Browser Use response")
	}
	return nil
}

func (a browserAPI) create(ctx context.Context, homeID, turnID, sessionID, workspaceID string, timeout int) (browserSession, error) {
	var result browserSession
	err := a.do(ctx, http.MethodPost, "/browsers", map[string]any{
		"metadata": map[string]string{"horizon_home": homeID, "horizon_turn": turnID, "horizon_session": sessionID, "horizon_workspace": workspaceID},
		"timeout":  timeout,
	}, &result)
	if err != nil {
		return result, err
	}
	if result.ID == "" || result.CDPURL == "" {
		return result, fmt.Errorf("Browser Use creation returned no browser ID or CDP URL")
	}
	return result, nil
}

func (a browserAPI) list(ctx context.Context, homeID string) ([]browserSession, error) {
	var all []browserSession
	for page := 1; ; page++ {
		query := url.Values{}
		query.Set("metadata", "horizon_home="+homeID)
		query.Set("filterBy", "active")
		query.Set("pageSize", "100")
		query.Set("pageNumber", strconv.Itoa(page))
		var result struct {
			Items      []browserSession `json:"items"`
			TotalItems int              `json:"totalItems"`
			PageNumber int              `json:"pageNumber"`
			PageSize   int              `json:"pageSize"`
		}
		if err := a.do(ctx, http.MethodGet, "/browsers?"+query.Encode(), nil, &result); err != nil {
			return nil, err
		}
		for _, item := range result.Items {
			if item.Status == "active" && item.Metadata["horizon_home"] == homeID {
				all = append(all, item)
			}
		}
		if result.PageSize <= 0 || result.PageNumber != page {
			return nil, fmt.Errorf("invalid Browser Use pagination response")
		}
		if len(result.Items) == 0 || page*result.PageSize >= result.TotalItems {
			return all, nil
		}
		if page >= 1000 {
			return nil, fmt.Errorf("Browser Use list exceeds pagination limit")
		}
	}
}

func (a browserAPI) get(ctx context.Context, id string) (browserSession, error) {
	var result browserSession
	if err := a.do(ctx, http.MethodGet, "/browsers/"+url.PathEscape(id), nil, &result); err != nil {
		return result, err
	}
	return result, nil
}

func (a browserAPI) stop(ctx context.Context, id string) error {
	return a.do(ctx, http.MethodPatch, "/browsers/"+url.PathEscape(id), map[string]string{"action": "stop"}, nil)
}
