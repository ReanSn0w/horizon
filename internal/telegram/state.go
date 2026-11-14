package telegram

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/ReanSn0w/horizon/internal/maintenance"
)

type Entry struct {
	Attachments []Attachment `json:"attachments,omitempty" yaml:"attachments,omitempty"`
	MediaGroup  string       `json:"media_group_id,omitempty" yaml:"media_group_id,omitempty"`
	ID          int64        `json:"id" yaml:"id"`
	Author      int64        `json:"author" yaml:"author"`
	Name        string       `json:"name,omitempty" yaml:"name"`
	Time        int64        `json:"time,omitempty" yaml:"time"`
	Text        string       `json:"text" yaml:"text"`
	Thread      int64        `json:"thread,omitempty" yaml:"thread"`
	Reply       int64        `json:"reply,omitempty" yaml:"reply"`
	Bot         bool         `json:"bot,omitempty" yaml:"bot"`
	Trigger     bool         `json:"trigger,omitempty" yaml:"-"`
}

type Conversation struct {
	ID          int64     `json:"id"`
	Origin      int64     `json:"origin"`
	Type        string    `json:"type"`
	Title       string    `json:"title"`
	Available   bool      `json:"available"`
	Workspace   string    `json:"workspace,omitempty"`
	History     []Entry   `json:"history,omitempty"`
	Thread      int64     `json:"thread,omitempty"`
	LastInput   time.Time `json:"last_input,omitempty"`
	LastCompact time.Time `json:"last_compact,omitempty"`
	IdleFor     time.Time `json:"idle_for,omitempty"`
	IdleTask    string    `json:"idle_task,omitempty"`
	LastTask    string    `json:"last_task,omitempty"`
}

func (c Conversation) Route() string { return strconv.FormatInt(c.Origin, 10) }

type PendingInput struct {
	Message   *Entry  `json:"message,omitempty"`
	Quote     *Entry  `json:"quote,omitempty"`
	Recent    []Entry `json:"recent,omitempty"`
	ChatID    int64   `json:"chat_id,omitempty"`
	Origin    int64   `json:"origin,omitempty"`
	UpdateID  int64   `json:"update_id"`
	Route     string  `json:"route"`
	Workspace string  `json:"workspace"`
	Access    string  `json:"access"`
	Author    string  `json:"author"`
	Payload   string  `json:"payload"`
}

type Registry struct {
	Version int                     `json:"version"`
	Pending *PendingInput           `json:"pending,omitempty"`
	Offset  int64                   `json:"offset"`
	Chats   map[int64]*Conversation `json:"chats"`
}

type Store struct {
	dir string
}

func NewStore(home string, botID int64) Store {
	return Store{filepath.Join(home, "gateway", "telegram-v2", strconv.FormatInt(botID, 10))}
}

func (s Store) Snapshot() (Registry, error) {
	value := Registry{Version: 1, Chats: map[int64]*Conversation{}}
	if err := loadJSON(filepath.Join(s.dir, "chats.json"), &value); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Registry{}, err
	}
	if value.Version != 1 || value.Chats == nil || value.Offset < 0 {
		return Registry{}, errors.New("invalid Telegram chat registry")
	}
	seen := map[int64]bool{}
	for origin, chat := range value.Chats {
		if chat == nil || chat.Origin != origin || chat.ID == 0 || origin == 0 || seen[chat.ID] {
			return Registry{}, errors.New("invalid Telegram chat binding")
		}
		seen[chat.ID] = true
	}
	return value, nil
}

func (s Store) Update(edit func(*Registry) error) error {
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	unlock, err := maintenance.Lock(filepath.Join(s.dir, "chats.lock"), true)
	if err != nil {
		return err
	}
	defer unlock()
	value, err := s.Snapshot()
	if err != nil {
		return err
	}
	if err := edit(&value); err != nil {
		return err
	}
	return saveJSON(filepath.Join(s.dir, "chats.json"), value)
}

func (r Registry) Find(id int64) *Conversation {
	if c := r.Chats[id]; c != nil {
		return c
	}
	for _, c := range r.Chats {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func (r Registry) Sorted() []*Conversation {
	chats := make([]*Conversation, 0, len(r.Chats))
	for _, chat := range r.Chats {
		chats = append(chats, chat)
	}
	sort.Slice(chats, func(i, j int) bool { return chats[i].ID < chats[j].ID })
	return chats
}

// Resolve never falls back from an explicit host-selected route.
func (s Store) Resolve(owner int64, route string) (Conversation, error) {
	registry, err := s.Snapshot()
	if err != nil {
		return Conversation{}, err
	}
	if route == "" {
		if owner <= 0 {
			return Conversation{}, errors.New("Telegram owner is not configured")
		}
		if chat := registry.Chats[owner]; chat != nil {
			if !chat.Available {
				return Conversation{}, errors.New("Telegram owner chat is unavailable")
			}
			return *chat, nil
		}
		return Conversation{ID: owner, Origin: owner, Type: "private", Available: true}, nil
	}
	origin, err := strconv.ParseInt(route, 10, 64)
	if err != nil || origin == 0 || strconv.FormatInt(origin, 10) != route || (origin > 0 && origin != owner) {
		return Conversation{}, errors.New("invalid host-selected Telegram chat route")
	}
	chat := registry.Chats[origin]
	if chat == nil || !chat.Available || chat.ID > 0 && chat.ID != owner {
		return Conversation{}, errors.New("host-selected Telegram chat is unavailable")
	}
	return *chat, nil
}

func loadJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, value); err != nil {
		return errors.New("invalid Telegram state data")
	}
	return nil
}

func saveJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return WritePrivate(path, data, 0600)
}

func WritePrivate(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".telegram-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(mode); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	parent, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open Telegram state directory for sync: %w", err)
	}
	defer parent.Close()
	return parent.Sync()
}
