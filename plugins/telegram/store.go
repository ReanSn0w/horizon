package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

type record struct {
	Seq     int64  `yaml:"seq" json:"seq"`
	ID      int64  `yaml:"id" json:"id"`
	Author  int64  `yaml:"author" json:"author"`
	Name    string `yaml:"name" json:"name"`
	Time    int64  `yaml:"time" json:"time"`
	Text    string `yaml:"text" json:"text"`
	Thread  int64  `yaml:"thread" json:"thread"`
	Reply   int64  `yaml:"reply" json:"reply"`
	Bot     bool   `yaml:"bot" json:"bot"`
	Mention bool   `yaml:"mention" json:"-"`
}
type chat struct {
	Origin     int64     `yaml:"origin"`
	ID         int64     `yaml:"id"`
	Type       string    `yaml:"type"`
	Title      string    `yaml:"title"`
	Username   string    `yaml:"username"`
	Available  bool      `yaml:"available"`
	Forum      bool      `yaml:"forum"`
	Thread     int64     `yaml:"thread"`
	HasThread  bool      `yaml:"has_thread"`
	Workspace  string    `yaml:"workspace"`
	Session    string    `yaml:"session"`
	History    []record  `yaml:"history"`
	NextSeq    int64     `yaml:"next_seq"`
	ContextSeq int64     `yaml:"context_seq"`
	LastAt     time.Time `yaml:"last_at"`
	LastError  string    `yaml:"last_error"`
	Jobs       []*job    `yaml:"jobs"`
}
type job struct {
	QueuedAt    time.Time `yaml:"queued_at,omitempty" json:"-"`
	ID          string    `yaml:"id" json:"request_id"`
	ChatID      int64     `yaml:"chat_id" json:"-"`
	Status      string    `yaml:"status" json:"status"`
	Error       string    `yaml:"error,omitempty" json:"error,omitempty"`
	Input       record    `yaml:"input" json:"-"`
	Manual      bool      `yaml:"manual" json:"-"`
	Instruction string    `yaml:"instruction" json:"-"`
	Thread      int64     `yaml:"thread" json:"-"`
	Response    string    `yaml:"response,omitempty" json:"-"`
	Parts       []string  `yaml:"parts,omitempty" json:"-"`
	Sent        []int64   `yaml:"sent,omitempty" json:"message_ids,omitempty"`
	ContextSeq  int64     `yaml:"context_seq" json:"-"`
}
type state struct {
	Version int               `yaml:"version"`
	BotID   int64             `yaml:"bot_id"`
	Offset  int64             `yaml:"offset"`
	Chats   map[string]*chat  `yaml:"chats"`
	Aliases map[string]string `yaml:"aliases"`
}
type store struct {
	dir   string
	botID int64
}

func newStore(home string, botID int64) store {
	return store{filepath.Join(home, "gateway", "telegram", strconv.FormatInt(botID, 10)), botID}
}
func (s store) read() (state, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, "state.yaml"))
	if errors.Is(err, os.ErrNotExist) {
		return state{Version: 1, BotID: s.botID, Chats: map[string]*chat{}, Aliases: map[string]string{}}, nil
	}
	if err != nil {
		return state{}, err
	}
	var v state
	if err = decodeState(data, &v); err != nil {
		return v, err
	}
	if v.Version != 1 || v.BotID != s.botID || v.Chats == nil || v.Aliases == nil {
		return v, errors.New("invalid or unsupported telegram state")
	}
	for key, c := range v.Chats {
		if c == nil || key != strconv.FormatInt(c.ID, 10) {
			return v, errors.New("invalid chat registry")
		}
		for _, j := range c.Jobs {
			if j == nil || j.ID == "" || j.ChatID != c.ID || !validJobStatus(j.Status) {
				return v, errors.New("invalid telegram job")
			}
		}
	}
	return v, nil
}
func validJobStatus(s string) bool {
	switch s {
	case "queued", "evaluating", "generating", "generated", "sending", "sent", "skipped", "failed", "unknown":
		return true
	}
	return false
}
func atomicFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".telegram-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(mode); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	err = dir.Sync()
	if errors.Is(err, syscall.EINVAL) {
		return nil
	}
	return err
}
func lockFile(path string, nonblock bool) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	flags := syscall.LOCK_EX
	if nonblock {
		flags |= syscall.LOCK_NB
	}
	if err = syscall.Flock(int(f.Fd()), flags); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func unlock(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }
func (s store) update(edit func(*state) error) error {
	f, err := lockFile(filepath.Join(s.dir, "state.lock"), false)
	if err != nil {
		return err
	}
	defer unlock(f)
	value, err := s.read()
	if err != nil {
		return err
	}
	if err = edit(&value); err != nil {
		return err
	}
	for _, c := range value.Chats {
		trimChat(c)
	}
	data, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	return atomicFile(filepath.Join(s.dir, "state.yaml"), data, 0600)
}
func (s store) snapshot() (state, error) {
	if _, err := os.Stat(s.dir); errors.Is(err, os.ErrNotExist) {
		return s.read()
	}
	f, err := lockFile(filepath.Join(s.dir, "state.lock"), false)
	if err != nil {
		return state{}, err
	}
	defer unlock(f)
	return s.read()
}
func trimChat(c *chat) {
	if len(c.History) > 100 {
		c.History = append([]record(nil), c.History[len(c.History)-100:]...)
	}
	completed := 0
	for i := len(c.Jobs) - 1; i >= 0; i-- {
		j := c.Jobs[i]
		if j.Status == "sent" || j.Status == "failed" || j.Status == "skipped" {
			completed++
			j.Response = ""
			j.Parts = nil
			j.Instruction = ""
			j.Input.Text = ""
			if completed > 100 {
				c.Jobs = append(c.Jobs[:i], c.Jobs[i+1:]...)
			}
		}
	}
}
func appendHistory(c *chat, r record) record {
	c.NextSeq++
	r.Seq = c.NextSeq
	c.History = append(c.History, r)
	return r
}
func requestID() string {
	var v [16]byte
	if _, err := io.ReadFull(rand.Reader, v[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(v[:])
}
func pending(status string) bool {
	return status == "queued" || status == "evaluating" || status == "generating" || status == "generated" || status == "sending"
}
func enqueue(v *state, c *chat, j *job) error {
	total, count := 0, 0
	for _, ch := range v.Chats {
		for _, old := range ch.Jobs {
			if pending(old.Status) {
				total++
				if ch.ID == c.ID {
					count++
				}
			}
		}
	}
	if total >= 1000 || count >= 100 {
		return errors.New("telegram queue is full")
	}
	if j.QueuedAt.IsZero() {
		j.QueuedAt = time.Now().UTC()
	}
	c.Jobs = append(c.Jobs, j)
	return nil
}
func (s store) recover() error {
	return s.update(func(v *state) error {
		for _, c := range v.Chats {
			for _, j := range c.Jobs {
				switch j.Status {
				case "evaluating":
					j.Status = "queued"
				case "generating", "sending":
					j.Status = "unknown"
					j.Error = "Interrupted external action; outcome is unknown and will not be retried"
					c.LastError = j.Error
				}
			}
		}
		return nil
	})
}
func resolveChat(v *state, id string) (*chat, error) {
	for i := 0; i < 8; i++ {
		next, ok := v.Aliases[id]
		if !ok {
			break
		}
		id = next
	}
	c := v.Chats[id]
	if c == nil {
		return nil, fmt.Errorf("unknown chat ID %s; use telegram list", id)
	}
	return c, nil
}
func sortedChats(v state) []*chat {
	result := make([]*chat, 0, len(v.Chats))
	for _, c := range v.Chats {
		result = append(result, c)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
func telegramRunning(home string) bool {
	f, err := lockFile(filepath.Join(home, "gateway", "process.lock"), true)
	if err == nil {
		unlock(f)
		return false
	}
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}

func decodeState(data []byte, v *state) error {
	d := yaml.NewDecoder(bytes.NewReader(data))
	d.KnownFields(true)
	if err := d.Decode(v); err != nil {
		return errors.New("cannot decode telegram state")
	}
	var extra yaml.Node
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("telegram state has trailing data")
	}
	return nil
}
