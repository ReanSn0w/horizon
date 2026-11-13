package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSendJournalRetainsPartialUnknownAcrossRestart(t *testing.T) {
	home := t.TempDir()
	store := NewStore(home, 99)
	chat := Conversation{ID: -50, Origin: -50, Available: true}
	calls := 0
	sender := func(_ context.Context, id int64, part string) (int64, error) {
		calls++
		if id != chat.ID {
			t.Fatalf("sent to %d", id)
		}
		if calls == 1 {
			if len([]rune(part)) != 2000 {
				t.Fatal("first chunk exceeds the UTF-16 budget")
			}
			return 100, nil
		}
		return 0, errors.New("Telegram connection lost")
	}
	text := strings.Repeat("😀", 2600)
	first, err := store.Send(context.Background(), sender, chat, "session", "send-1", text)
	if err != nil || first.State != "unknown" || len(first.MessageIDs) != 1 || first.MessageIDs[0] != 100 || calls != 2 {
		t.Fatalf("send = %#v, error = %v, calls = %d", first, err, calls)
	}
	restarted := NewStore(home, 99)
	read, err := restarted.ReadSend("send-1", chat.Origin)
	if err != nil || read.State != "unknown" || read.Parts[0].State != "confirmed" || read.Parts[1].State != "unknown" {
		t.Fatalf("lookup = %#v, error = %v", read, err)
	}
	repeated, err := restarted.Send(context.Background(), sender, chat, "session", "send-1", text)
	if err != nil || repeated.State != "unknown" || calls != 2 {
		t.Fatalf("repeat = %#v, error = %v, calls = %d", repeated, err, calls)
	}
	if _, err = restarted.ReadSend("send-1", -55); err == nil {
		t.Fatal("read crossed chat route")
	}
	unrecorded, err := restarted.Confirmed(chat, nil, 20)
	if err != nil || len(unrecorded) != 1 || unrecorded[0].OperationID != "send-1" {
		t.Fatalf("missing unrecorded delivery from same session: %#v %v", unrecorded, err)
	}
	recorded, err := restarted.Confirmed(chat, map[string]map[int64]bool{"send-1": {100: true}}, 20)
	if err != nil || len(recorded) != 0 {
		t.Fatalf("duplicated recorded delivery: %#v %v", recorded, err)
	}
}

func TestExplicitRouteNeverFallsBackToOwner(t *testing.T) {
	store := NewStore(t.TempDir(), 99)
	chat, err := store.Resolve(9, "")
	if err != nil || chat.ID != 9 {
		t.Fatalf("owner route = %#v, error = %v", chat, err)
	}
	for _, route := range []string{"bad", "-404", "10", "9"} {
		if chat, err := store.Resolve(9, route); err == nil || chat.ID != 0 {
			t.Fatalf("explicit route %q resolved to %#v, error = %v", route, chat, err)
		}
	}
}

func TestClientLostResponseLeavesPersistedPartialDelivery(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Chat int64  `json:"chat_id"`
			Text string `json:"text"`
		}
		if r.URL.Path != "/bot99:fixture/sendMessage" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Chat != -4 {
			t.Error("unexpected local Telegram request")
		}
		if calls.Add(1) == 1 {
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":24,"chat":{"id":-4}}}`)
			return
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	}))
	defer server.Close()
	client := &Client{base: server.URL, token: "99:fixture", http: server.Client()}
	home := t.TempDir()
	chat := Conversation{ID: -4, Origin: -4, Available: true}
	text := strings.Repeat("a", 5000)
	result, err := NewStore(home, 99).Send(context.Background(), client.Send, chat, "session", "lost", text)
	if err != nil || result.State != "unknown" || len(result.MessageIDs) != 1 || result.MessageIDs[0] != 24 {
		t.Fatalf("lost response status = %#v, error = %v", result, err)
	}
	result, err = NewStore(home, 99).Send(context.Background(), client.Send, chat, "session", "lost", text)
	if err != nil || result.State != "unknown" || calls.Load() != 2 {
		t.Fatalf("retry started a Telegram request: %#v %v calls=%d", result, err, calls.Load())
	}
}
