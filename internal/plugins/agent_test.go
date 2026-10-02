package plugins

import (
	"context"
	"encoding/json"
	"github.com/ReanSn0w/horizon/internal/session"
	"strings"
	"testing"
	"time"
)

func TestAgentTransport(t *testing.T) {
	home := t.TempDir()
	request := Request{Home: home, Workspace: home, Access: "write"}
	for _, tc := range []struct {
		name, script string
		good         bool
	}{
		{"good", `echo '{"ok":true,"data":[]}'`, true},
		{"extra", `echo '{"ok":true,"data":[]} {}'`, false},
		{"unknown", `echo '{"ok":true,"data":[],"extra":1}'`, false},
		{"missing", `echo '{"ok":false}'`, false},
		{"huge", `printf '%70000s' x`, false},
		{"hang", `sleep 5`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := candidate(t, home, tc.name, tc.script)
			start := time.Now()
			_, _, err := Call(context.Background(), path, "context", request, 50*time.Millisecond)
			if (err == nil) != tc.good {
				t.Fatalf("%v", err)
			}
			if time.Since(start) > time.Second {
				t.Fatal("unbounded call")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := Call(ctx, "missing", "tool", request, time.Second)
	if f, ok := err.(*Failure); !ok || f.Code != "plugin_start_failed" {
		t.Fatal(err)
	}
}
func TestSchemaSubset(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(`{"type":"object","properties":{"scope":{"type":"string","enum":["agent","user"]},"nullable":{"type":["array","null"],"items":{"type":"integer"}}},"required":["scope","nullable"],"additionalProperties":false}`), &schema); err != nil {
		t.Fatal(err)
	}
	if err := CheckSchema(schema); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		json string
		good bool
	}{{`{"scope":"agent","nullable":null}`, true}, {`{"scope":"user","nullable":[1,2]}`, true}, {`{"scope":"wrong","nullable":null}`, false}, {`{"scope":"user","nullable":[1.5]}`, false}, {`{"scope":"user"}`, false}, {`{"scope":"user","nullable":null,"extra":true}`, false}} {
		if err := Validate(schema, json.RawMessage(tc.json)); (err == nil) != tc.good {
			t.Fatalf("%s: %v", tc.json, err)
		}
	}
	schema["oneOf"] = []any{}
	if err := CheckSchema(schema); err == nil {
		t.Fatal("unsupported keyword accepted")
	}
}
func TestAgentOptIn(t *testing.T) {
	home := t.TempDir()
	ws := session.Workspace{Dir: home, ID: session.WorkspaceID(home)}
	candidate(t, home, "old", `echo '{"protocol_version":1,"version":"1","description":"CLI only"}'`)
	candidate(t, home, "broken", `exit 1`)
	got, err := Prepare(context.Background(), home, ws, "read", nil, true, nil)
	if err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	_, err = Prepare(context.Background(), home, ws, "read", []string{"old"}, true, nil)
	if err == nil || !strings.Contains(err.Error(), "agent protocol") {
		t.Fatal(err)
	}
	_, err = Prepare(context.Background(), home, ws, "read", []string{"broken"}, true, nil)
	if err == nil {
		t.Fatal("broken explicitly enabled plugin accepted")
	}
}
