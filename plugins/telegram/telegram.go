package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type tgUser struct {
	ID       int64  `json:"id"`
	Bot      bool   `json:"is_bot"`
	Username string `json:"username"`
	First    string `json:"first_name"`
	Last     string `json:"last_name"`
}
type tgChat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Username string `json:"username"`
	First    string `json:"first_name"`
	Last     string `json:"last_name"`
	Forum    bool   `json:"is_forum"`
}
type entity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
}
type tgMessage struct {
	ID          int64      `json:"message_id"`
	From        *tgUser    `json:"from"`
	SenderChat  *tgChat    `json:"sender_chat"`
	Chat        tgChat     `json:"chat"`
	Date        int64      `json:"date"`
	Text        string     `json:"text"`
	Entities    []entity   `json:"entities"`
	Thread      int64      `json:"message_thread_id"`
	Reply       *tgMessage `json:"reply_to_message"`
	MigrateTo   int64      `json:"migrate_to_chat_id"`
	MigrateFrom int64      `json:"migrate_from_chat_id"`
	NewTitle    string     `json:"new_chat_title"`
}
type membership struct {
	Chat   tgChat `json:"chat"`
	Member struct {
		Status string `json:"status"`
	} `json:"new_chat_member"`
}
type update struct {
	ID         int64       `json:"update_id"`
	Message    *tgMessage  `json:"message"`
	Membership *membership `json:"my_chat_member"`
}
type telegram struct {
	base, token string
	http        *http.Client
}

// A build-time override allows binary integration tests to use a local server.
// Runtime configuration cannot redirect the bot token to another endpoint.
var telegramEndpoint = "https://api.telegram.org"

type apiError struct {
	Code        int
	Retry       int
	Description string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("Telegram HTTP/API %d: %s", e.Code, e.Description)
}
func (t *telegram) call(ctx context.Context, method string, input, output any) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(t.base, "/")+"/bot"+t.token+"/"+method, bytes.NewReader(payload))
	if err != nil {
		return errors.New("invalid Telegram endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.http.Do(req)
	if err != nil {
		return errors.New("Telegram network error; request outcome may be unknown")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024+1))
	if err != nil || len(data) > 8*1024*1024 {
		return errors.New("Telegram response unreadable; request outcome may be unknown")
	}
	var value struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Code        int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			Retry   int   `json:"retry_after"`
			Migrate int64 `json:"migrate_to_chat_id"`
		} `json:"parameters"`
	}
	if err = json.Unmarshal(data, &value); err != nil {
		return errors.New("invalid Telegram response; request outcome may be unknown")
	}
	if !value.OK {
		if value.Code == 0 {
			value.Code = resp.StatusCode
		}
		return &apiError{value.Code, value.Parameters.Retry, strings.ReplaceAll(value.Description, t.token, "[redacted]")}
	}
	if resp.StatusCode != 200 {
		return errors.New("unexpected Telegram status; request outcome may be unknown")
	}
	if output != nil {
		if err = json.Unmarshal(value.Result, output); err != nil {
			return errors.New("invalid Telegram result; request outcome may be unknown")
		}
	}
	return nil
}
func (t *telegram) getUpdates(ctx context.Context, offset int64) ([]update, error) {
	var result []update
	err := t.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 25, "allowed_updates": []string{"message", "my_chat_member"}}, &result)
	return result, err
}
func (t *telegram) send(ctx context.Context, c *chat, j *job, text string) (int64, error) {
	data := map[string]any{"chat_id": c.ID, "text": text}
	if j.Thread > 0 {
		data["message_thread_id"] = j.Thread
	}
	if j.Input.ID > 0 {
		data["reply_parameters"] = map[string]any{"message_id": j.Input.ID, "allow_sending_without_reply": true}
	}
	var msg tgMessage
	if err := t.call(ctx, "sendMessage", data, &msg); err != nil {
		return 0, err
	}
	if msg.ID <= 0 {
		return 0, errors.New("missing sent message ID; outcome is unknown")
	}
	return msg.ID, nil
}
func displayChat(c tgChat) string {
	if c.Type == "private" {
		return strings.TrimSpace(c.First + " " + c.Last)
	}
	return c.Title
}
func chatName(c *chat) string {
	if strings.TrimSpace(c.Title) != "" {
		return c.Title
	}
	if c.Username != "" {
		return "@" + c.Username
	}
	return fmt.Sprintf("Chat %d", c.ID)
}
func acceptChat(c tgChat, owner int64) bool {
	return c.Type == "group" || c.Type == "supergroup" || (c.Type == "private" && c.ID == owner)
}
func upsertChat(v *state, input tgChat) *chat {
	key := strconv.FormatInt(input.ID, 10)
	c := v.Chats[key]
	if c == nil {
		c = &chat{ID: input.ID, Origin: input.ID, Type: input.Type, Available: true}
		v.Chats[key] = c
	}
	if title := displayChat(input); title != "" {
		c.Title = title
	}
	c.Username = input.Username
	c.Forum = input.Forum
	c.Type = input.Type
	return c
}
func recordFor(m *tgMessage) record {
	r := record{ID: m.ID, Time: m.Date, Text: m.Text, Thread: m.Thread}
	if m.From != nil {
		r.Author = m.From.ID
		r.Name = strings.TrimSpace(m.From.First + " " + m.From.Last)
		r.Bot = m.From.Bot
	}
	if m.Reply != nil {
		r.Reply = m.Reply.ID
	}
	return r
}
func ingest(home string, s store, cfg settings, bot tgUser, u update, observers ...func(string, map[string]any)) error {
	// Config registration precedes the atomic cursor change and is idempotent.
	var input *tgChat
	if u.Message != nil {
		input = &u.Message.Chat
	} else if u.Membership != nil {
		input = &u.Membership.Chat
	}
	if m := u.Message; m != nil {
		if m.MigrateTo != 0 {
			if err := migrateGroup(home, m.Chat.ID, m.MigrateTo); err != nil {
				return err
			}
		}
		if m.MigrateFrom != 0 {
			if err := migrateGroup(home, m.MigrateFrom, m.Chat.ID); err != nil {
				return err
			}
		}
	}
	if input != nil && acceptChat(*input, cfg.Telegram.Owner) && input.Type != "private" {
		if _, err := ensureGroup(home, input.ID); err != nil {
			return err
		}
	}
	var event string
	data := map[string]any{"update_id": u.ID, "offset": u.ID + 1}
	err := s.update(func(v *state) error {
		if u.ID < v.Offset {
			return nil
		}
		if input == nil || !acceptChat(*input, cfg.Telegram.Owner) {
			event = "update_filtered"
			data["reason"] = "unsupported_or_foreign_chat"
			v.Offset = u.ID + 1
			return nil
		}
		if input.Type == "private" && (u.Message == nil || u.Message.From == nil || u.Message.From.ID != cfg.Telegram.Owner) {
			event = "update_filtered"
			data["reason"] = "foreign_private_author"
			v.Offset = u.ID + 1
			return nil
		}
		event = "update_saved"
		data["chat_id"] = input.ID
		c := upsertChat(v, *input)
		if u.Membership != nil {
			status := u.Membership.Member.Status
			c.Available = status != "left" && status != "kicked"
		}
		if m := u.Message; m != nil {
			if m.Text == "" || m.From == nil || m.From.Bot || m.SenderChat != nil {
				event = "message_ignored"
				data["reason"] = "unsupported_message"
			}
			if m.MigrateTo != 0 {
				newID := strconv.FormatInt(m.MigrateTo, 10)
				oldID := strconv.FormatInt(c.ID, 10)
				if existing := v.Chats[newID]; existing != nil && existing != c {
					return errors.New("migration destination already has a chat state")
				}
				delete(v.Chats, oldID)
				c.ID = m.MigrateTo
				c.Type = "supergroup"
				for _, j := range c.Jobs {
					j.ChatID = c.ID
				}
				v.Chats[newID] = c
				v.Aliases[oldID] = newID
			} else if m.MigrateFrom != 0 {
				oldID := strconv.FormatInt(m.MigrateFrom, 10)
				newID := strconv.FormatInt(c.ID, 10)
				if old := v.Chats[oldID]; old != nil {
					if c.Workspace != "" || len(c.Jobs) > 0 {
						return errors.New("migration destination has existing history")
					}
					delete(v.Chats, oldID)
					old.ID = c.ID
					old.Type = c.Type
					old.Title = c.Title
					for _, j := range old.Jobs {
						j.ChatID = old.ID
					}
					v.Chats[newID] = old
					c = old
				}
				v.Aliases[oldID] = newID
			}
			if m.NewTitle != "" {
				c.Title = m.NewTitle
			}
			if m.Text != "" && m.From != nil && !m.From.Bot && m.SenderChat == nil {
				r := recordFor(m)
				r.Mention = hasMention(m.Text, m.Entities, bot.Username)
				j := &job{ID: requestID(), ChatID: c.ID, Status: "queued", Input: r, Thread: m.Thread}
				if err := enqueue(v, c, j); err != nil {
					return err
				}
				r = appendHistory(c, r)
				j.Input = r
				event = "message_queued"
				data["request_id"] = j.ID
				data["message_id"] = m.ID
				c.LastAt = time.Unix(m.Date, 0).UTC()
				c.Thread = m.Thread
				c.HasThread = true
				c.Available = true
			}
		}
		v.Offset = u.ID + 1
		return nil
	})
	if err == nil && event != "" {
		for _, observe := range observers {
			observe(event, data)
			observe("offset_saved", map[string]any{"update_id": u.ID, "offset": u.ID + 1})
		}
	}
	return err
}
