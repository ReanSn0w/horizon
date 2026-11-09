package plugins

import (
	"context"
	"encoding/json"
	"github.com/ReanSn0w/horizon/internal/session"
	"os"
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
			timeout := time.Second
			if tc.name == "hang" {
				timeout = 50 * time.Millisecond
			}
			_, _, err := Call(context.Background(), path, "context", request, timeout)
			if (err == nil) != tc.good {
				t.Fatalf("%v", err)
			}
			if time.Since(start) > 2*time.Second {
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

func TestPrepareMaintenanceAccessAndDescribeFailures(t *testing.T) {
	for _, access := range []string{"read", "write", "full"} {
		t.Run(access, func(t *testing.T) {
			home := t.TempDir()
			ws := session.Workspace{Dir: home, ID: session.WorkspaceID(home)}
			marker := home + "/maintained"
			script := `case "$1" in
horizon-plugin-metadata) echo '{"protocol_version":1,"agent_protocol_version":1,"version":"1","description":"test"}';;
horizon-plugin-agent-describe) echo '{"ok":true,"data":{"instructions":"Use this plugin","tools":[],"maintain_effect":"write_home"}}';;
horizon-plugin-agent-maintain) touch '` + marker + `'; echo '{"ok":false,"error":{"code":"offline","message":"offline"}}';;
horizon-plugin-agent-context) echo '{"ok":true,"data":[{"source":"test","text":"previous state"}]}';;
esac`
			candidate(t, home, "test", script)
			warnings := 0
			got, err := Prepare(context.Background(), home, ws, access, []string{"test"}, true, func(string) { warnings++ })
			if err != nil || len(got) != 1 || got[0].Context[0].Text != "previous state" {
				t.Fatal(got, err)
			}
			_, err = os.Stat(marker)
			if access == "read" {
				if err == nil || warnings != 0 {
					t.Fatal("read maintenance launched")
				}
			} else if err != nil || warnings != 1 {
				t.Fatal(err, warnings)
			}
			if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			_, err = Prepare(context.Background(), home, ws, access, []string{"test"}, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("context-only preparation wrote")
			}
		})
	}
	for _, description := range []string{
		`null`,
		`{"instructions":"test","tools":null}`,
		`{"instructions":"test","tools":[],"finalize_effect":"network"}`,
		`{"instructions":"test","tools":[{"name":"add","description":"test","effect":"read","parameters":{"type":"object","properties":{},"required":[],"additionalProperties":false}},{"name":"add","description":"test","effect":"read","parameters":{"type":"object","properties":{},"required":[],"additionalProperties":false}}]}`,
		`{"instructions":"test","tools":[{"name":"add","description":"test","effect":"read","parameters":{"type":"object","properties":{},"required":[],"additionalProperties":false,"oneOf":[]}}]}`,
	} {
		home := t.TempDir()
		ws := session.Workspace{Dir: home, ID: session.WorkspaceID(home)}
		script := `case "$1" in
horizon-plugin-metadata) echo '{"protocol_version":1,"agent_protocol_version":1,"version":"1","description":"test"}';;
*) echo '{"ok":true,"data":` + description + `}';;
esac`
		candidate(t, home, "invalid", script)
		if _, err := Prepare(context.Background(), home, ws, "write", []string{"invalid"}, false, nil); err == nil {
			t.Fatalf("accepted %s", description)
		}
	}
}
func TestEffectMatrix(t *testing.T) {
	for _, access := range []string{"read", "write", "full"} {
		for _, effect := range []string{"read", "write_home", "write_workspace", "unrestricted", "unknown"} {
			want := effect == "read" || access != "read" && (effect == "write_home" || effect == "write_workspace") || access == "full" && effect == "unrestricted"
			if got := Allowed(access, effect); got != want {
				t.Fatal(access, effect, got)
			}
		}
	}
}
func TestNumericSchemaWithoutExponentExpansion(t *testing.T) {
	var schema map[string]any
	if err := Decode([]byte(`{"type":"object","properties":{"value":{"type":"integer","enum":[1,1e100000000]}} ,"required":["value"],"additionalProperties":false}`), &schema); err != nil {
		t.Fatal(err)
	}
	if err := CheckSchema(schema); err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{`{"value":1.0}`, `{"value":10e99999999}`} {
		if err := Validate(schema, json.RawMessage(args)); err != nil {
			t.Fatal(args, err)
		}
	}
}
