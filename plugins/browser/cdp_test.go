package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

func TestRemoteCDPWithoutLocalBrowser(t *testing.T) {
	var methods []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, _, err := ws.UpgradeHTTP(r, w)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		for {
			data, err := wsutil.ReadClientText(conn)
			if err != nil {
				return
			}
			var call struct {
				ID        int             `json:"id"`
				Method    string          `json:"method"`
				SessionID string          `json:"sessionId"`
				Params    json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(data, &call); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			methods = append(methods, call.Method)
			mu.Unlock()
			result := map[string]any{}
			switch call.Method {
			case "Target.createTarget":
				result["targetId"] = "tab-1"
			case "Target.attachToTarget":
				result["sessionId"] = "cdp-session-1"
			case "Runtime.evaluate":
				var params struct {
					Expression string `json:"expression"`
				}
				_ = json.Unmarshal(call.Params, &params)
				if params.Expression == "self" {
					result["result"] = map[string]any{"type": "object", "className": "Window"}
				} else {
					result["result"] = map[string]any{"type": "object", "value": map[string]any{"url": "https://example.com", "title": "Example", "text": "Hello", "elements": []any{}, "truncated": false}}
				}
			case "Page.navigate":
				result["frameId"] = "frame-1"
				result["loaderId"] = "loader-2"
			case "Page.getFrameTree":
				result["frameTree"] = map[string]any{"frame": map[string]any{"id": "frame-1", "loaderId": "loader-1", "url": "about:blank", "securityOrigin": "://", "mimeType": "text/html"}}
			case "DOM.getDocument":
				result["root"] = map[string]any{"nodeId": 1, "backendNodeId": 1, "nodeType": 9, "nodeName": "#document", "localName": "", "nodeValue": "", "childNodeCount": 0}
			}
			response, _ := json.Marshal(map[string]any{"id": call.ID, "sessionId": call.SessionID, "result": result})
			if err := wsutil.WriteServerText(conn, response); err != nil {
				return
			}
			if call.Method == "Page.navigate" {
				for _, event := range []map[string]any{
					{"method": "Page.frameNavigated", "sessionId": call.SessionID, "params": map[string]any{"frame": map[string]any{"id": "frame-1", "loaderId": "loader-2", "url": "https://example.com", "securityOrigin": "https://example.com", "mimeType": "text/html"}}},
					{"method": "Page.lifecycleEvent", "sessionId": call.SessionID, "params": map[string]any{"frameId": "frame-1", "loaderId": "loader-2", "name": "init", "timestamp": 1}},
					{"method": "Page.loadEventFired", "sessionId": call.SessionID, "params": map[string]any{"timestamp": 2}},
				} {
					encoded, _ := json.Marshal(event)
					if err := wsutil.WriteServerText(conn, encoded); err != nil {
						return
					}
				}
			}
		}
	}))
	defer server.Close()
	var page pageData
	target, err := runCDP(context.Background(), browserRecord{CDPURL: strings.Replace(server.URL, "http://", "ws://", 1)}, 3*time.Second, chromedp.Navigate("https://example.com"), chromedp.Evaluate(snapshotScript, &page))
	if err != nil || target != "tab-1" || page.Title != "Example" {
		t.Fatalf("target=%q page=%+v err=%v methods=%v", target, page, err, methods)
	}
	var second pageData
	_, err = runCDP(context.Background(), browserRecord{CDPURL: strings.Replace(server.URL, "http://", "ws://", 1), TargetID: target}, 3*time.Second, chromedp.Evaluate(snapshotScript, &second))
	if err != nil || second.Title != "Example" {
		t.Fatalf("reattach page=%+v err=%v methods=%v", second, err, methods)
	}
	mu.Lock()
	defer mu.Unlock()
	created := 0
	for _, method := range methods {
		if method == "Target.createTarget" {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created %d tabs: %v", created, methods)
	}
}
