package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestResponseMatrixAndContext(t *testing.T) {
	cfg := defaults(t.TempDir())
	cfg.Telegram.Owner = 1
	called := 0
	run := func(ctx context.Context, dir string, args []string, input string) (string, error) {
		called++
		var r struct {
			State struct {
				Messages []record `json:"messages"`
			} `json:"state"`
		}
		if err := json.Unmarshal([]byte(input), &r); err != nil {
			t.Fatal(err)
		}
		if len(input) > 32*1024 || len(r.State.Messages) == 0 || r.State.Messages[len(r.State.Messages)-1].Text == "" {
			t.Fatal("invalid decision context")
		}
		return `{"id":"decision","answers":{"should_reply":{"type":"noul","noul":0.9}}}`, nil
	}
	for _, only := range []bool{true, false} {
		for _, mode := range []string{"mention", "conversation"} {
			for _, author := range []int64{1, 2} {
				for _, mention := range []bool{true, false} {
					cfg.Groups["-1"] = groupSettings{only, mode}
					c := &chat{ID: -1, Type: "group", History: []record{{Seq: 1, Author: 2, Text: strings.Repeat("x", 40000)}, {Seq: 2, Author: author, Text: "current"}}}
					j := &job{Input: record{Seq: 2, Author: author, Mention: mention, ID: 2}}
					got, err := shouldReply(context.Background(), "home", cfg, c, j, "model", run)
					want := (!only || author == 1) && (mention || mode == "conversation")
					if err != nil || got != want {
						t.Fatalf("only=%t mode=%s author=%d mention=%t got=%t err=%v", only, mode, author, mention, got, err)
					}
				}
			}
		}
	}
	if called == 0 {
		t.Fatal("Jev was never called")
	}
	if hasMention("😀 @other", []entity{{Type: "mention", Offset: 3, Length: 6}}, "our_bot") {
		t.Fatal("wrong bot matched")
	}
	if !hasMention("😀 @OUR_BOT", []entity{{Type: "mention", Offset: 3, Length: 8}}, "our_bot") {
		t.Fatal("UTF16 mention missed")
	}
}

func TestPendingInputSurvivesHistoryTrimming(t *testing.T) {
	j := &job{Input: record{Seq: 1, ID: 42, Text: "pending-input"}}
	c := &chat{History: []record{{Seq: 102, ID: 102, Text: "later participant"}, {Seq: 103, ID: 103, Bot: true, Text: "previous reply"}}}
	rows := historyFor(c, j, 1, false)
	if len(rows) != 1 || rows[0].ID != 42 || rows[0].Text != "pending-input" {
		t.Fatalf("lost pending input: %+v", rows)
	}
}
