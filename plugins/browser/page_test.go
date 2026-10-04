package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ReanSn0w/horizon/internal/plugins"
)

func TestBrowserToolSchemasAndNoImplicitCreation(t *testing.T) {
	description := browserTools()
	if description.FinalizeEffect != "unrestricted" || len(description.Tools) != 4 {
		t.Fatalf("description=%+v", description)
	}
	for _, tool := range description.Tools {
		if tool.Effect != "unrestricted" || plugins.CheckSchema(tool.Parameters) != nil {
			t.Fatalf("tool=%+v", tool)
		}
	}
	b := newBrowserLifecycle(t.TempDir(), settings{APIKey: "test", APIURL: "http://127.0.0.1:1", TimeoutMinutes: 10})
	for _, name := range []string{"snapshot", "click", "type"} {
		request := plugins.Request{Access: "full", Tool: name, TurnID: "turn", SessionID: "session", WorkspaceID: "workspace", Arguments: json.RawMessage(`{}`)}
		_, err := b.tool(context.Background(), request)
		if err == nil || !strings.Contains(err.Error(), "navigate first") {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for _, raw := range []string{`{"url":"file:///etc/passwd"}`, `{"url":"javascript:alert(1)"}`, `{"url":"https://user:pass@example.com"}`} {
		request := plugins.Request{Access: "full", Tool: "navigate", TurnID: "turn", SessionID: "session", WorkspaceID: "workspace", Arguments: json.RawMessage(raw)}
		_, err := b.tool(context.Background(), request)
		if err == nil || !strings.Contains(err.Error(), "HTTP(S)") {
			t.Fatalf("%s: %v", raw, err)
		}
	}
}

func TestRenderPageOmitsSelectorsAndFingerprint(t *testing.T) {
	result := renderPage(pageData{URL: "https://example.com", Elements: []elementRef{{Ref: "e1", Selector: "body > button", Fingerprint: "sensitive", Role: "button", Name: "Submit"}}}, "snapshot")
	encoded, err := json.Marshal(result)
	if err != nil || strings.Contains(string(encoded), "sensitive") || strings.Contains(string(encoded), "body > button") || !strings.Contains(string(encoded), "Submit") {
		t.Fatalf("result=%s err=%v", encoded, err)
	}
}

func TestRenderPageBoundsEncodedBytes(t *testing.T) {
	page := pageData{Text: strings.Repeat("<😊", 6000)}
	for i := 0; i < 50; i++ {
		page.Elements = append(page.Elements, elementRef{Ref: "e", Role: strings.Repeat("<", 24), Name: strings.Repeat("<", 64)})
	}
	result := renderPage(page, "snapshot")
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > 48<<10 || !result.Truncated {
		t.Fatalf("encoded bytes=%d truncated=%t err=%v", len(encoded), result.Truncated, err)
	}
}
