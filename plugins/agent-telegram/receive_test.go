package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ReanSn0w/horizon/internal/daemon"
	"github.com/ReanSn0w/horizon/internal/telegram"
)

func fixture(t *testing.T) *adapter {
	t.Helper()
	home := t.TempDir()
	return &adapter{home: home, cfg: telegram.DefaultAdapter(home), bot: telegram.BotSettings{Owner: 1, Token: "99:fake"}, username: "test_bot", store: telegram.NewStore(home, 99)}
}

func message(chat, author int64, kind, text string, mention bool) *telegram.Message {
	m := &telegram.Message{ID: 1, Chat: telegram.Chat{ID: chat, Type: kind}, From: &telegram.User{ID: author, First: "Participant"}, Text: text}
	if mention {
		m.Entities = []telegram.Entity{{Type: "mention", Offset: 3, Length: 9}}
	}
	return m
}

func TestGroupModesOwnerFilterAndOwnerPrivate(t *testing.T) {
	a := fixture(t)
	a.cfg.Groups = map[string]telegram.GroupSettings{
		"-2": {Mode: "mention"},
		"-3": {Mode: "conversation"},
		"-4": {Mode: "conversation", OwnerOnly: true},
	}
	value := telegram.Registry{Chats: map[int64]*telegram.Conversation{}}
	rows := []struct {
		msg     *telegram.Message
		trigger bool
		access  string
	}{
		{message(9, 9, "private", "FOREIGN PRIVATE", false), false, ""},
		{message(1, 9, "private", "FOREIGN AUTHOR", false), false, ""},
		{message(1, 1, "private", "owner text", false), true, "write"},
		{message(-2, 2, "group", "group history", false), false, ""},
		{message(-2, 2, "group", "😀 @test_bot", true), true, "read"},
		{message(-3, 2, "supergroup", "everyone", false), true, "read"},
		{message(-4, 2, "supergroup", "read-only participant", false), false, ""},
		{message(-4, 1, "supergroup", "owner reply", false), true, "read"},
	}
	for i, row := range rows {
		p, err := a.prepare(&value, telegram.Update{ID: int64(i + 1), Message: row.msg})
		if err != nil || (p != nil) != row.trigger || p != nil && p.Access != row.access {
			t.Fatalf("row %d pending = %#v, error = %v", i, p, err)
		}
		if p != nil {
			if strings.Contains(p.Payload, "FOREIGN") || i == 4 && !strings.Contains(p.Payload, "group history") ||
				i == 7 && !strings.Contains(p.Payload, "read-only participant") {
				t.Fatalf("invalid chat payload %s", p.Payload)
			}
		}
	}
	if value.Find(9) != nil || len(value.Find(1).History) != 1 {
		t.Fatal("foreign private chat was registered")
	}
	if err := migrateGroup(&value, telegram.Chat{ID: -2}, -2, -22); err != nil {
		t.Fatal(err)
	}
	migrated := value.Find(-22)
	if migrated.Route() != "-2" || migrated.ID != -22 || len(migrated.History) != 2 {
		t.Fatalf("migrated chat = %#v", migrated)
	}
}

func TestOffsetAdvancesOnlyAfterDurableDaemonReceipt(t *testing.T) {
	a := fixture(t)
	ctx := context.Background()
	var accepted daemon.Request
	a.call = func(_ context.Context, _ string, r daemon.Request) (daemon.Response, error) {
		if r.Method == "submit" {
			accepted = r
			return daemon.Response{}, &daemon.Failure{Code: "outcome_unknown", Message: "lost reply"}
		}
		return daemon.Response{}, &daemon.Failure{Code: "daemon_unavailable", Message: "offline"}
	}
	u := telegram.Update{ID: 12, Message: message(1, 1, "private", "test", false)}
	if err := a.receive(ctx, u); err == nil {
		t.Fatal("accepted unconfirmed delivery")
	}
	value, err := a.store.Snapshot()
	if err != nil || value.Offset != 0 || value.Pending == nil || value.Pending.UpdateID != 12 {
		t.Fatalf("pending state = %#v, error = %v", value, err)
	}
	calls := 0
	a.call = func(_ context.Context, _ string, r daemon.Request) (daemon.Response, error) {
		calls++
		if r.OperationID != accepted.OperationID || r.Message != accepted.Message || r.Route != "1" || !r.Channel || r.Delivery != daemon.ActiveDelivery {
			t.Fatalf("different replay request %#v", r)
		}
		data, _ := json.Marshal(daemon.Task{ID: "task", Input: &daemon.InputReceipt{Source: "telegram", Route: "1", Status: "stored"}})
		return daemon.Response{Data: data}, nil
	}
	if err := a.flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.receive(ctx, u); err != nil {
		t.Fatal(err)
	}
	value, err = a.store.Snapshot()
	if err != nil || value.Offset != 13 || value.Pending != nil || calls != 1 || len(value.Find(1).History) != 1 {
		t.Fatalf("recovered state = %#v, error = %v, calls = %d", value, err, calls)
	}
}

func TestTypingStopsForEveryTerminalState(t *testing.T) {
	a := fixture(t)
	registry := telegram.Registry{Chats: map[int64]*telegram.Conversation{1: {ID: 1, Origin: 1, Available: true}}}
	for _, state := range []string{"succeeded", "failed", "cancelled"} {
		ctx, stop := context.WithCancel(context.Background())
		active := map[string]typingAction{}
		var wg sync.WaitGroup
		started, cancelled := make(chan struct{}), make(chan struct{})
		send := func(ctx context.Context, chat, thread int64) error {
			if chat != 1 || thread != 0 {
				t.Errorf("typing route = %d/%d", chat, thread)
			}
			close(started)
			<-ctx.Done()
			close(cancelled)
			return ctx.Err()
		}
		task := daemon.Task{ID: "active", State: "running", Channel: true, Route: "1"}
		a.refreshTyping(ctx, []daemon.Task{task}, registry, active, &wg, send)
		select {
		case <-started:
		case <-time.After(time.Second):
			stop()
			wg.Wait()
			t.Fatal("typing was not started")
		}
		task.State = state
		a.refreshTyping(ctx, []daemon.Task{task}, registry, active, &wg, send)
		select {
		case <-cancelled:
		case <-time.After(time.Second):
			stop()
			wg.Wait()
			t.Fatalf("typing was not stopped after %s", state)
		}
		stop()
		wg.Wait()
	}
}
