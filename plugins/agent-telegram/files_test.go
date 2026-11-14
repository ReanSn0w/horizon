package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ReanSn0w/horizon/internal/daemon"
	"github.com/ReanSn0w/horizon/internal/telegram"
)

func TestFileInputPreparedBeforeSubmissionAndStableOnRetry(t *testing.T) {
	for _, kind := range []string{"document", "photo", "failed"} {
		t.Run(kind, func(t *testing.T) {
			a := fixture(t)
			downloads := 0
			submissions := 0
			a.fetch = func(ctx context.Context, id string, w io.Writer) (int64, error) {
				downloads++
				if kind == "failed" {
					return 0, telegram.ErrFileLimit
				}
				return io.Copy(w, strings.NewReader("file content"))
			}
			m := message(1, 1, "private", "", false)
			m.Caption = "inspect"
			m.MediaGroup = "album"
			if kind == "photo" {
				m.Photos = []telegram.PhotoSize{{File: telegram.File{ID: "image"}, Width: 2, Height: 2}}
			} else {
				m.Document = &telegram.Document{File: telegram.File{ID: "file"}, Name: "report.txt", MIME: "text/plain"}
			}
			var first daemon.Request
			a.call = func(ctx context.Context, home string, r daemon.Request) (daemon.Response, error) {
				submissions++
				var payload struct {
					Message telegram.Entry `json:"message"`
				}
				if err := json.Unmarshal([]byte(r.Message), &payload); err != nil {
					t.Fatal(err)
				}
				if len(payload.Message.Attachments) != 1 || payload.Message.MediaGroup != "album" || r.Delivery != daemon.ActiveDelivery || r.Route != "1" {
					t.Fatal(r)
				}
				att := payload.Message.Attachments[0]
				if att.Local != "" || att.Temp != "" {
					t.Fatal("private state leaked", att)
				}
				if kind == "failed" {
					if att.State != "failed" || att.Path != "" || payload.Message.Text != "inspect" {
						t.Fatal(att)
					}
				} else {
					b, err := os.ReadFile(att.Path)
					if err != nil || string(b) != "file content" {
						t.Fatal(string(b), err)
					}
				}
				if submissions == 1 {
					first = r
					return daemon.Response{}, errors.New("lost daemon response")
				}
				if first.Message != r.Message || first.OperationID != r.OperationID {
					t.Fatal("pending input changed on retry")
				}
				b, _ := json.Marshal(daemon.Task{ID: "task", Input: &daemon.InputReceipt{Source: "telegram", Route: r.Route, Status: "included"}})
				return daemon.Response{Data: b}, nil
			}
			update := telegram.Update{ID: 50, Message: m}
			if err := a.receive(context.Background(), update); err == nil {
				t.Fatal("expected lost response")
			}
			// A new adapter instance uses durable pending state after restart.
			restarted := *a
			if err := restarted.flush(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := restarted.receive(context.Background(), update); err != nil {
				t.Fatal(err)
			}
			if downloads != 1 || submissions != 2 {
				t.Fatal(downloads, submissions)
			}
			state, _ := a.store.Snapshot()
			if state.Offset != 51 || state.Pending != nil {
				t.Fatal(state)
			}
		})
	}
}
func TestMediaTriggersAndQuotes(t *testing.T) {
	a := fixture(t)
	a.cfg.Groups = map[string]telegram.GroupSettings{"-2": {Mode: "mention"}}
	state := telegram.Registry{Chats: map[int64]*telegram.Conversation{}}
	m := message(-2, 2, "group", "", false)
	m.Document = &telegram.Document{File: telegram.File{ID: "f"}, Name: "x"}
	p, err := a.prepare(&state, telegram.Update{ID: 1, Message: m})
	if err != nil || p != nil {
		t.Fatal(p, err)
	}
	if state.Find(-2).History[0].Attachments[0].State != "not_downloaded" {
		t.Fatal(state)
	}
	reply := *m
	m.ID = 2
	m.Caption = "😀 @test_bot"
	m.CaptionEntities = []telegram.Entity{{Type: "mention", Offset: 3, Length: 9}}
	m.Reply = &reply
	p, err = a.prepare(&state, telegram.Update{ID: 2, Message: m})
	if err != nil || p == nil || p.Quote == nil || p.Quote.Attachments[0].Path != "" {
		t.Fatal(p, err)
	}
	workspace := p.Workspace
	if err := migrateGroup(&state, telegram.Chat{ID: -22}, -2, -22); err != nil {
		t.Fatal(err)
	}
	m.Chat.ID = -22
	m.ID = 3
	p, err = a.prepare(&state, telegram.Update{ID: 3, Message: m})
	if err != nil || p == nil || p.Origin != -2 || p.Workspace != workspace {
		t.Fatal(p, err)
	}
	// Owner's uncaptioned photograph is still input.
	photo := message(1, 1, "private", "", false)
	photo.Photos = []telegram.PhotoSize{{File: telegram.File{ID: "p"}, Width: 2, Height: 2}}
	p, err = a.prepare(&state, telegram.Update{ID: 4, Message: photo})
	if err != nil || p == nil {
		t.Fatal(p, err)
	}
}
