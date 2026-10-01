package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDeliverySavedAndRecovered(t *testing.T) {
	home := t.TempDir()
	s := newStore(home, 99)
	j := &job{ID: "j", ChatID: 1, Status: "generated", Response: strings.Repeat("😀", 2300)}
	c := &chat{ID: 1, Origin: 1, Available: true, Jobs: []*job{j}}
	s.update(func(v *state) error { v.Chats["1"] = c; return nil })
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 2 {
			fmt.Fprint(w, `{"ok":false,"error_code":500,"description":"uncertain"}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":42}}`)
	}))
	defer server.Close()
	g := &gateway{store: s, bot: tgUser{ID: 99}, tg: &telegram{server.URL, "token", &http.Client{Timeout: time.Second}}}
	if err := g.delivery(context.Background(), c, j); err != nil {
		t.Fatal(err)
	}
	v, _ := s.snapshot()
	saved := v.Chats["1"].Jobs[0]
	if saved.Status != "unknown" || len(saved.Sent) != 1 || len(v.Chats["1"].History) != 1 {
		t.Fatal("lost partial delivery")
	}
	if err := s.recover(); err != nil {
		t.Fatal(err)
	}
	v, _ = s.snapshot()
	if v.Chats["1"].Jobs[0].Status != "unknown" {
		t.Fatal("unknown delivery replayed")
	}
	for _, part := range splitMessage(j.Response) {
		if len([]rune(part)) > 4096 {
			t.Fatal("oversized fragment")
		}
	}
}
