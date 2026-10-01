package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIngestFilteringAndMigration(t *testing.T) {
	home := configHome(t)
	cfg := defaults(home)
	cfg.Telegram.Owner = 10
	s := newStore(home, 99)
	bot := tgUser{ID: 99, Username: "our_bot"}
	denied := update{ID: 1, Message: &tgMessage{Chat: tgChat{ID: 11, Type: "private"}, From: &tgUser{ID: 11}, Text: "secret"}}
	if err := ingest(home, s, cfg, bot, denied); err != nil {
		t.Fatal(err)
	}
	v, _ := s.snapshot()
	if len(v.Chats) != 0 || v.Offset != 2 {
		t.Fatal("foreign private chat recorded")
	}
	u := update{ID: 2, Message: &tgMessage{ID: 1, Chat: tgChat{ID: -4, Type: "group", Title: "Имя"}, From: &tgUser{ID: 12}, Text: "😀 @our_bot", Entities: []entity{{Type: "mention", Offset: 3, Length: 8}}}}
	if err := ingest(home, s, cfg, bot, u); err != nil {
		t.Fatal(err)
	}
	ingest(home, s, cfg, bot, u)
	v, _ = s.snapshot()
	c := v.Chats["-4"]
	if len(c.Jobs) != 1 || !c.Jobs[0].Input.Mention || chatName(c) != "Имя" {
		t.Fatal("incorrect ingest")
	}
	c.Workspace = "unused"
	s.update(func(v *state) error { v.Chats["-4"].Workspace = "original"; return nil })
	if err := ingest(home, s, cfg, bot, update{ID: 3, Message: &tgMessage{Chat: tgChat{ID: -4, Type: "group"}, MigrateTo: -8}}); err != nil {
		t.Fatal(err)
	}
	v, _ = s.snapshot()
	if v.Chats["-8"].Workspace != "original" || v.Aliases["-4"] != "-8" {
		t.Fatal("migration lost workspace")
	}
	data, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
	if !strings.Contains(string(data), "-4") {
		t.Fatal("missing group config")
	}
}
