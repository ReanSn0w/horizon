package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReanSn0w/horizon/internal/plugins"
	"github.com/ReanSn0w/horizon/internal/session"
	"github.com/ReanSn0w/horizon/internal/telegram"
)

func fileRequest(t *testing.T) plugins.Request {
	t.Helper()
	home, _ := session.NormalizeWorkspace(t.TempDir())
	workspace, _ := session.NormalizeWorkspace(t.TempDir())
	cfg := `default_model: m
providers:
  p: {url: "http://example.invalid", key: fixture}
models:
  m: {provider: p, model: fixture, compact_threshold: 1000}
plugins:
  telegram: {bot_token: "99:fixture", owner_user_id: 1}
`
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	return plugins.Request{Home: home, Workspace: workspace, WorkspaceID: session.WorkspaceID(workspace), SessionID: "s", OperationID: "op", Access: "read", Communication: true, Tool: "send_file", Arguments: json.RawMessage(`{"path":"report.txt","caption":"","as_photo":false}`)}
}
func TestFileExtensionSchemaAndPermission(t *testing.T) {
	desc := description(plugins.Request{Communication: true})
	found := false
	for _, tool := range desc.Tools {
		if tool.Name == "send_file" {
			found = true
			if tool.Effect != plugins.CommunicationEffect {
				t.Fatal(tool)
			}
			data, _ := json.Marshal(tool.Parameters)
			for _, key := range []string{"path", "caption", "as_photo", "additionalProperties"} {
				if !strings.Contains(string(data), key) {
					t.Fatal(string(data))
				}
			}
		}
	}
	if !found || !strings.Contains(desc.Instructions, "telegram__send_file") {
		t.Fatal(desc)
	}
	r := plugins.Request{Home: filepath.Join(t.TempDir(), "absent"), Tool: "send_file", Access: "full"}
	if _, err := operate(context.Background(), "tool", r); err == nil || !strings.Contains(err.Error(), "permission") {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.Home); !os.IsNotExist(err) {
		t.Fatal("denial created home")
	}
}
func TestFileExtensionValidationAndReadCommunication(t *testing.T) {
	r := fileRequest(t)
	for _, arguments := range []string{`{}`, `{"path":"x","caption":""}`, `{"path":"x","caption":"","as_photo":false,"chat_id":2}`, `{"path":null,"caption":"","as_photo":false}`} {
		r.Arguments = json.RawMessage(arguments)
		if _, err := operate(context.Background(), "tool", r); err == nil || !strings.Contains(err.Error(), "requires only") {
			t.Fatal(arguments, err)
		}
	}
	r.Arguments = json.RawMessage(`{"path":"report.txt","caption":"","as_photo":false}`)
	if err := os.WriteFile(filepath.Join(r.Workspace, "report.txt"), []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	// Preconfirm with a local sender. operate must return the journal result
	// without contacting Telegram, even with read filesystem access.
	store := telegram.NewStore(r.Home, 99)
	_, err := store.SendFile(context.Background(), func(context.Context, int64, *os.File, string, string, bool) (int64, error) { return 123, nil }, telegram.Conversation{ID: 1, Origin: 1, Available: true}, r.SessionID, r.OperationID, r.Workspace, "report.txt", "", false)
	if err != nil {
		t.Fatal(err)
	}
	result, err := operate(context.Background(), "tool", r)
	if err != nil {
		t.Fatal(err)
	}
	sent := result.(telegram.SendResult)
	if sent.State != "confirmed" || sent.ChatID != 1 || len(sent.MessageIDs) != 1 {
		t.Fatal(sent)
	}
	r.Communication = false
	if _, err := operate(context.Background(), "tool", r); err == nil {
		t.Fatal("read communication bypassed permission")
	}
	r.Communication = true
	r.Route = "-99"
	if _, err := operate(context.Background(), "tool", r); err == nil {
		t.Fatal("unknown route accepted")
	}
	r.Route = ""
	r.WorkspaceID = "wrong"
	if _, err := operate(context.Background(), "tool", r); err == nil {
		t.Fatal("host workspace identity ignored")
	}
}
