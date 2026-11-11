package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ReanSn0w/horizon/internal/daemon"
	"github.com/ReanSn0w/horizon/internal/telegram"
)

type adapter struct {
	home       string
	cfg        telegram.AdapterSettings
	bot        telegram.BotSettings
	username   string
	credential string
	store      telegram.Store
	client     *telegram.Client
	call       func(context.Context, string, daemon.Request) (daemon.Response, error)
}

func (a *adapter) request(ctx context.Context, r daemon.Request) (daemon.Response, error) {
	r.Caller.Kind = "adapter"
	r.Caller.Source = "telegram"
	r.Caller.Credential = a.credential
	r.Caller.Access = "full"
	if r.Caller.Workspace == "" {
		r.Caller.Workspace = a.home
	}
	if r.ID == "" {
		r.ID = r.Method
	}
	return a.call(ctx, a.home, r)
}

func (a *adapter) flush(ctx context.Context) error {
	value, err := a.store.Snapshot()
	if err != nil || value.Pending == nil {
		return err
	}
	p := value.Pending
	operationID := fmt.Sprintf("telegram:%d:update:%d", a.bot.BotID(), p.UpdateID)
	request := daemon.Request{
		Method: "submit", OperationID: operationID, Delivery: daemon.ActiveDelivery,
		Channel: true, Route: p.Route, Access: p.Access, Author: p.Author, Message: p.Payload,
		Caller: daemon.Caller{Workspace: p.Workspace},
	}
	response, err := a.request(ctx, request)
	var failure *daemon.Failure
	if errors.As(err, &failure) && failure.Code == "outcome_unknown" {
		response, err = a.request(ctx, daemon.Request{Method: "lookup", OperationID: operationID})
	}
	if err != nil {
		return err
	}
	var task daemon.Task
	if json.Unmarshal(response.Data, &task) != nil || task.ID == "" || task.Input == nil ||
		task.Input.Route != p.Route || task.Input.Source != "telegram" || task.Input.Status != "stored" && task.Input.Status != "included" {
		return errors.New("daemon did not confirm durable Telegram input")
	}
	return a.store.Update(func(state *telegram.Registry) error {
		if state.Pending == nil || state.Pending.UpdateID != p.UpdateID {
			return errors.New("Telegram pending update changed during submission")
		}
		state.Pending = nil
		state.Offset = p.UpdateID + 1
		for _, chat := range state.Chats {
			if chat.Route() == p.Route {
				chat.LastTask = task.ID
				break
			}
		}
		return nil
	})
}

func (a *adapter) receive(ctx context.Context, u telegram.Update) error {
	if u.ID < 0 || u.ID == math.MaxInt64 {
		return errors.New("invalid Telegram update ID")
	}
	err := a.store.Update(func(state *telegram.Registry) error {
		if u.ID < state.Offset {
			return nil
		}
		if state.Pending != nil {
			return errors.New("flush pending Telegram input before reading another update")
		}
		p, err := a.prepare(state, u)
		if err != nil {
			return err
		}
		if p == nil {
			state.Offset = u.ID + 1
		} else {
			state.Pending = p
		}
		return nil
	})
	if err != nil {
		return err
	}
	return a.flush(ctx)
}

func (a *adapter) prepare(state *telegram.Registry, u telegram.Update) (*telegram.PendingInput, error) {
	if u.Message == nil {
		if member := u.Membership; member != nil && (isGroup(member.Chat) || member.Chat.Type == "private" && member.Chat.ID == a.bot.Owner && state.Find(a.bot.Owner) != nil) {
			chat := upsert(state, member.Chat)
			status := member.Member.Status
			chat.Available = status != "kicked" && status != "left"
		}
		return nil, nil
	}
	m := u.Message
	private := m.Chat.Type == "private" && m.Chat.ID == a.bot.Owner && m.From != nil && m.From.ID == a.bot.Owner
	if !private && !isGroup(m.Chat) {
		return nil, nil
	}
	if oldID, newID := migration(m); oldID != 0 {
		return nil, migrateGroup(state, m.Chat, oldID, newID)
	}
	if strings.TrimSpace(m.Text) == "" || m.ID <= 0 || m.From == nil || m.From.ID <= 0 || m.From.Bot || m.SenderChat != nil {
		return nil, nil
	}
	chat := upsert(state, m.Chat)
	chat.Available = true
	chat.Thread = m.Thread
	chat.LastInput = time.Now().UTC()
	record := entry(m)
	group := a.cfg.Group(chat.ID, chat.Origin)
	record.Trigger = private || (!group.OwnerOnly || m.From.ID == a.bot.Owner) &&
		(group.Mode == "conversation" || telegram.HasMention(m.Text, m.Entities, a.username))
	recent := make([]telegram.Entry, 0, len(chat.History))
	for _, item := range chat.History {
		if !item.Trigger {
			recent = append(recent, item)
		}
	}
	chat.History = append(chat.History, record)
	chat.History = chat.History[max(0, len(chat.History)-a.cfg.History):]
	if !record.Trigger {
		return nil, nil
	}
	workspace, err := telegram.Workspace(a.cfg.Workspace, a.bot.BotID(), *chat)
	if err != nil {
		return nil, err
	}
	chat.Workspace = workspace
	access := a.cfg.GroupAccess
	if private {
		access = a.cfg.PrivateAccess
	}
	var quote *telegram.Entry
	if m.Reply != nil && (m.Reply.Chat.ID == 0 || m.Reply.Chat.ID == m.Chat.ID) && m.Reply.Text != "" {
		value := entry(m.Reply)
		quote = &value
	}
	payload, err := inputPayload(chat.ID, record, quote, recent)
	if err != nil {
		return nil, err
	}
	return &telegram.PendingInput{UpdateID: u.ID, Route: chat.Route(), Workspace: workspace, Access: access, Author: fmt.Sprintf("Telegram user %d", record.Author), Payload: payload}, nil
}

func inputPayload(chatID int64, message telegram.Entry, quote *telegram.Entry, recent []telegram.Entry) (string, error) {
	value := struct {
		ChatID  int64            `json:"chat_id"`
		Message telegram.Entry   `json:"message"`
		Quote   *telegram.Entry  `json:"quote,omitempty"`
		Recent  []telegram.Entry `json:"recent,omitempty"`
	}{chatID, message, quote, recent}
	for {
		data, err := json.Marshal(value)
		if err != nil {
			return "", err
		}
		if len(data) <= 64*1024 {
			return string(data), nil
		}
		if len(value.Recent) == 0 {
			return "", errors.New("Telegram message exceeds input limit")
		}
		value.Recent = value.Recent[1:]
	}
}

func isGroup(c telegram.Chat) bool {
	return c.ID < 0 && (c.Type == "group" || c.Type == "supergroup")
}
func entry(m *telegram.Message) telegram.Entry {
	r := telegram.Entry{ID: m.ID, Text: m.Text, Time: m.Date, Thread: m.Thread}
	if m.From != nil {
		r.Author = m.From.ID
		r.Name = strings.TrimSpace(m.From.First + " " + m.From.Last)
		if r.Name == "" {
			r.Name = m.From.Username
		}
		if len(r.Name) > 256 {
			end := 256
			for !utf8.RuneStart(r.Name[end]) {
				end--
			}
			r.Name = r.Name[:end]
		}
		r.Bot = m.From.Bot
	}
	if m.Reply != nil {
		r.Reply = m.Reply.ID
	}
	return r
}

func upsert(r *telegram.Registry, input telegram.Chat) *telegram.Conversation {
	chat := r.Find(input.ID)
	if chat == nil {
		chat = &telegram.Conversation{ID: input.ID, Origin: input.ID, Type: input.Type, Available: true}
		r.Chats[input.ID] = chat
	}
	if chat.ID == input.ID {
		chat.Type = input.Type
		title := input.Title
		if input.Type == "private" {
			title = strings.TrimSpace(input.First + " " + input.Last)
		}
		if title != "" {
			chat.Title = title
		}
	}
	return chat
}

func migration(m *telegram.Message) (int64, int64) {
	if m.Chat.ID < 0 && m.MigrateTo < 0 && m.MigrateTo != m.Chat.ID {
		return m.Chat.ID, m.MigrateTo
	}
	if m.Chat.ID < 0 && m.MigrateFrom < 0 && m.MigrateFrom != m.Chat.ID {
		return m.MigrateFrom, m.Chat.ID
	}
	return 0, 0
}

func migrateGroup(state *telegram.Registry, input telegram.Chat, oldID, newID int64) error {
	old, newChat := state.Find(oldID), state.Find(newID)
	if old == nil {
		old = &telegram.Conversation{ID: oldID, Origin: oldID, Type: "group", Title: input.Title, Available: true}
		state.Chats[oldID] = old
	}
	if newChat != nil && newChat != old {
		if newChat.Workspace != "" || len(newChat.History) > 0 {
			return errors.New("Telegram supergroup destination already has a workspace or history")
		}
		delete(state.Chats, newChat.Origin)
	}
	old.ID = newID
	old.Type = "supergroup"
	old.Available = true
	if input.Title != "" {
		old.Title = input.Title
	}
	return nil
}
