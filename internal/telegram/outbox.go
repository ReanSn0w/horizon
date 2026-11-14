package telegram

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ReanSn0w/horizon/internal/maintenance"
)

type Part struct {
	Text      string `json:"text"`
	State     string `json:"state"`
	MessageID int64  `json:"message_id,omitempty"`
}

type delivery struct {
	Kind        string    `json:"kind,omitempty"`
	File        *SentFile `json:"file,omitempty"`
	Snapshot    string    `json:"snapshot,omitempty"`
	OperationID string    `json:"operation_id"`
	ChatID      int64     `json:"chat_id"`
	Origin      int64     `json:"origin"`
	Session     string    `json:"session,omitempty"`
	Created     time.Time `json:"created"`
	Parts       []Part    `json:"parts"`
	FailureCode int       `json:"telegram_error_code,omitempty"`
}

type PartResult struct {
	State     string `json:"state"`
	MessageID int64  `json:"message_id,omitempty"`
}

type SendResult struct {
	Kind        string       `json:"kind,omitempty"`
	File        *SentFile    `json:"file,omitempty"`
	OperationID string       `json:"operation_id"`
	ChatID      int64        `json:"chat_id"`
	State       string       `json:"state"`
	MessageIDs  []int64      `json:"message_ids"`
	Parts       []PartResult `json:"parts"`
	FailureCode int          `json:"telegram_error_code,omitempty"`
}

func (d delivery) result() SendResult {
	result := SendResult{Kind: d.Kind, File: d.File, OperationID: d.OperationID, ChatID: d.ChatID, State: "confirmed", MessageIDs: []int64{}, FailureCode: d.FailureCode}
	for _, part := range d.Parts {
		state := part.State
		switch state {
		case "sending":
			state = "unknown"
			result.State = state
		case "rejected":
			result.State = state
		case "pending":
			if result.State == "confirmed" {
				result.State = state
			}
		case "confirmed":
			result.MessageIDs = append(result.MessageIDs, part.MessageID)
		default:
			state = "unknown"
			result.State = "unknown"
		}
		result.Parts = append(result.Parts, PartResult{State: state, MessageID: part.MessageID})
	}
	return result
}

func (s Store) operationPath(id string) (string, error) {
	if id == "" || len(id) > 512 || !utf8.ValidString(id) {
		return "", errors.New("invalid Telegram operation_id")
	}
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(s.dir, "outbox", hex.EncodeToString(sum[:])+".json"), nil
}

func (s Store) ReadSend(id string, origin int64) (SendResult, error) {
	path, err := s.operationPath(id)
	if err != nil {
		return SendResult{}, err
	}
	var value delivery
	if err = loadJSON(path, &value); err != nil || value.OperationID != id || origin != 0 && value.Origin != origin {
		return SendResult{}, errors.New("Telegram operation not found for this chat")
	}
	return value.result(), nil
}

type Sender func(context.Context, int64, string) (int64, error)

func (s Store) Send(ctx context.Context, send Sender, chat Conversation, sessionID, op, text string) (SendResult, error) {
	if !utf8.ValidString(text) || strings.TrimSpace(text) == "" || len(text) > 16*1024 {
		return SendResult{}, errors.New("Telegram text must be nonempty UTF-8 and at most 16 KiB")
	}
	path, err := s.operationPath(op)
	if err != nil {
		return SendResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return SendResult{}, err
	}
	unlock, err := maintenance.Lock(path+".lock", true)
	if err != nil {
		return SendResult{}, errors.New("cannot acquire Telegram operation lock; check read_send before repeating")
	}
	defer unlock()
	parts := SplitMessage(text)
	var value delivery
	err = loadJSON(path, &value)
	switch {
	case errors.Is(err, os.ErrNotExist):
		value = delivery{OperationID: op, ChatID: chat.ID, Origin: chat.Origin, Session: sessionID, Created: time.Now().UTC()}
		for _, part := range parts {
			value.Parts = append(value.Parts, Part{Text: part, State: "pending"})
		}
		if err := saveJSON(path, value); err != nil {
			return SendResult{}, err
		}
	case err != nil:
		return SendResult{}, err
	default:
		texts := make([]string, 0, len(value.Parts))
		for _, part := range value.Parts {
			texts = append(texts, part.Text)
		}
		if value.File != nil || value.Kind != "" || value.Origin != chat.Origin || value.Session != sessionID || value.OperationID != op || !slices.Equal(texts, parts) {
			return SendResult{}, errors.New("Telegram operation_id already exists with different contents")
		}
	}
	return deliver(ctx, path, &value, chat, func(text string) (int64, error) { return send(ctx, value.ChatID, text) })
}

func deliver(ctx context.Context, path string, value *delivery, chat Conversation, send func(string) (int64, error)) (SendResult, error) {
	for i := range value.Parts {
		part := &value.Parts[i]
		switch part.State {
		case "confirmed":
			continue
		case "pending":
			if ctx.Err() != nil || value.ChatID != chat.ID || !chat.Available {
				return value.result(), nil
			}
		case "rejected", "sending":
			return value.result(), nil
		default:
			return SendResult{}, errors.New("invalid Telegram part state")
		}
		part.State = "sending"
		if err := saveJSON(path, value); err != nil {
			return SendResult{}, err
		}
		id, err := send(part.Text)
		if err != nil || id <= 0 {
			var rejection *APIError
			if errors.As(err, &rejection) {
				part.State = "rejected"
				value.FailureCode = rejection.Code
				if e := saveJSON(path, value); e != nil {
					return SendResult{}, fmt.Errorf("Telegram send outcome requires operation lookup: %w", e)
				}
			}
			return value.result(), nil
		}
		part.State = "confirmed"
		part.MessageID = id
		if err := saveJSON(path, value); err != nil {
			return SendResult{}, fmt.Errorf("Telegram send outcome requires operation lookup: %w", err)
		}
	}
	return value.result(), nil
}

type ConfirmedMessage struct {
	Entry
	ChatID      int64  `json:"chat_id"`
	OperationID string `json:"operation_id"`
}

// Confirmed sends are read from the same journal that records confirmation.
// Only messages with IDs already recorded by this session are omitted.
func (s Store) Confirmed(chat Conversation, recorded map[string]map[int64]bool, limit int) ([]ConfirmedMessage, error) {
	dir := filepath.Join(s.dir, "outbox")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	recent := []ConfirmedMessage{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" || len(entry.Name()) != 69 {
			continue
		}
		var value delivery
		if err := loadJSON(filepath.Join(dir, entry.Name()), &value); err != nil {
			return nil, err
		}
		if value.Origin != chat.Origin {
			continue
		}
		for _, part := range value.Parts {
			if part.State == "confirmed" && !recorded[value.OperationID][part.MessageID] {
				entry := Entry{ID: part.MessageID, Name: "Telegram bot", Time: value.Created.Unix(), Text: part.Text, Bot: true}
				if value.File != nil {
					entry.Attachments = []Attachment{{Kind: value.Kind, Name: value.File.Name, Size: value.File.Size, Checksum: value.File.Checksum, State: "sent"}}
				}
				recent = append(recent, ConfirmedMessage{Entry: entry, ChatID: value.ChatID, OperationID: value.OperationID})
			}
		}
	}
	slices.SortFunc(recent, func(a, b ConfirmedMessage) int {
		if a.Time < b.Time || a.Time == b.Time && a.ID < b.ID {
			return -1
		}
		if a.Time > b.Time || a.ID > b.ID {
			return 1
		}
		return 0
	})
	return recent[max(0, len(recent)-limit):], nil
}
