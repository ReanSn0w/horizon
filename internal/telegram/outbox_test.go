package telegram

import (
	"context"
	"errors"
	"strings"
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
	for _, id := range []string{"session", "new-session"} {
		entries, err := restarted.Confirmed(chat, id, 20)
		if err != nil || len(entries) != map[string]int{"session": 0, "new-session": 1}[id] {
			t.Fatalf("context %s = %#v, error = %v", id, entries, err)
		}
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
