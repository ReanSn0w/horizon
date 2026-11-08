package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

type listedChat struct {
	CurrentRequest string     `json:"current_request_id,omitempty"`
	CurrentStage   string     `json:"current_stage,omitempty"`
	CurrentSince   *time.Time `json:"current_stage_since,omitempty"`
	QueueWaitMS    *int64     `json:"oldest_queue_wait_ms,omitempty"`
	ID             string     `json:"chat_id"`
	Name           string     `json:"name"`
	Type           string     `json:"type"`
	Available      bool       `json:"available"`
	Workspace      string     `json:"workspace"`
	Session        string     `json:"session_id"`
	OwnerOnly      bool       `json:"owner_only"`
	ResponseMode   string     `json:"response_mode"`
	LastAt         *time.Time `json:"last_message_at"`
	Pending        int        `json:"pending"`
	Unknown        int        `json:"unknown"`
	LastJob        string     `json:"last_request_id,omitempty"`
	Status         string     `json:"last_status,omitempty"`
	Error          string     `json:"last_error,omitempty"`
}

func localStore(home string) (store, error) {
	data, err := os.ReadFile(filepath.Join(home, "gateway", "identity"))
	if errors.Is(err, os.ErrNotExist) {
		return newStore(home, 0), nil
	}
	if err != nil {
		return store{}, err
	}
	id, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || id <= 0 {
		return store{}, errors.New("invalid telegram bot identity")
	}
	return newStore(home, id), nil
}
func listChats(a *app, opt *listCommand) error {
	cfg, err := loadSettings(a.home, false)
	if err != nil {
		return &cliError{2, err}
	}
	s, err := localStore(a.home)
	if err != nil {
		return err
	}
	value, err := s.snapshot()
	if err != nil {
		return err
	}
	health, _ := readHealth(a.home)
	if time.Since(health.Updated) > 3*time.Second || !telegramRunning(a.home) {
		health.Active = nil
	}
	result := make([]listedChat, 0, len(value.Chats))
	for _, c := range sortedChats(value) {
		g := cfg.Defaults
		if custom, ok := cfg.Groups[fmt.Sprint(c.ID)]; ok {
			g = custom
		}
		if c.Type == "private" {
			g = groupSettings{true, "direct"}
		}
		item := listedChat{ID: fmt.Sprint(c.ID), Name: chatName(c), Type: c.Type, Available: c.Available, Workspace: c.Workspace, Session: c.Session, OwnerOnly: g.OwnerOnly, ResponseMode: g.ResponseMode, Error: c.LastError}
		if !c.LastAt.IsZero() {
			t := c.LastAt
			item.LastAt = &t
		}
		for _, active := range health.Active {
			if active.ChatID == c.ID {
				item.CurrentRequest = active.RequestID
				item.CurrentStage = active.Stage
				since := active.Since
				item.CurrentSince = &since
			}
		}
		for _, j := range c.Jobs {
			if pending(j.Status) {
				if j.Status == "queued" && !j.QueuedAt.IsZero() {
					ms := time.Since(j.QueuedAt).Milliseconds()
					if item.QueueWaitMS == nil || ms > *item.QueueWaitMS {
						item.QueueWaitMS = &ms
					}
				}
				item.Pending++
			}
			if j.Status == "unknown" {
				item.Unknown++
			}
			item.LastJob = j.ID
			item.Status = j.Status
		}
		result = append(result, item)
	}
	if opt.JSON {
		return json.NewEncoder(a.out).Encode(struct {
			Version int          `json:"schema_version"`
			Chats   []listedChat `json:"chats"`
		}{1, result})
	}
	w := tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "CHAT ID\tNAME\tTYPE\tAVAILABLE\tOWNER ONLY\tRESPONSE MODE\tLAST MESSAGE\tPENDING\tUNKNOWN\tLAST STATUS\tCURRENT REQUEST\tCURRENT STAGE\tQUEUE WAIT MS\tLAST ERROR\tWORKSPACE\tSESSION")
	for _, c := range result {
		last := "-"
		if c.LastAt != nil {
			last = c.LastAt.Format(time.RFC3339)
		}
		wait := "-"
		if c.QueueWaitMS != nil {
			wait = strconv.FormatInt(*c.QueueWaitMS, 10)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%t\t%t\t%s\t%s\t%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", c.ID, safeLine(c.Name), c.Type, c.Available, c.OwnerOnly, c.ResponseMode, last, c.Pending, c.Unknown, c.Status, c.CurrentRequest, c.CurrentStage, wait, safeLine(c.Error), safeLine(c.Workspace), c.Session)
	}
	return w.Flush()
}
func sendChat(a *app, opt *sendCommand) error {
	if opt.Chat == 0 || opt.Thread < 0 {
		return &cliError{2, errors.New("--chat must be a non-zero decimal ID and --thread must be non-negative")}
	}
	cfg, err := loadSettings(a.home, true)
	if err != nil {
		return &cliError{2, err}
	}
	if !telegramRunning(a.home) {
		return errors.New("telegram is not running; start it before send")
	}
	s, err := localStore(a.home)
	if err != nil {
		return err
	}
	var submitted job
	err = s.update(func(v *state) error {
		c, err := resolveChat(v, fmt.Sprint(opt.Chat))
		if err != nil {
			return err
		}
		if !c.Available || c.Type == "private" && c.ID != cfg.Telegram.Owner {
			return errors.New("chat is not an available permitted destination")
		}
		thread := opt.Thread
		if thread > 0 && !c.Forum {
			return &cliError{2, errors.New("--thread is only supported for forum chats")}
		}
		if c.Forum && thread == 0 {
			if !c.HasThread || c.Thread <= 0 {
				return errors.New("specify --thread for this forum chat")
			}
			thread = c.Thread
		}
		submitted = job{ID: requestID(), ChatID: c.ID, Status: "queued", Manual: true, Instruction: opt.Message, Thread: thread}
		return enqueue(v, c, &submitted)
	})
	if err != nil {
		return err
	}
	if !opt.Wait {
		return printJob(a.out, opt.JSON, &submitted)
	}
	fmt.Fprintln(a.errOut, "telegram: waiting for request", submitted.ID)
	for {
		value, err := s.snapshot()
		if err != nil {
			return err
		}
		c, err := resolveChat(&value, fmt.Sprint(submitted.ChatID))
		if err != nil {
			return err
		}
		var found *job
		for _, j := range c.Jobs {
			if j.ID == submitted.ID {
				found = j
				break
			}
		}
		if found == nil {
			return fmt.Errorf("request %s result is no longer retained; inspect telegram list", submitted.ID)
		}
		if !pending(found.Status) {
			if err = printJob(a.out, opt.JSON, found); err != nil {
				return err
			}
			if found.Status != "sent" {
				return fmt.Errorf("request %s: %s: %s", found.ID, found.Status, found.Error)
			}
			return nil
		}
		if !telegramRunning(a.home) {
			return fmt.Errorf("telegram stopped; request %s is still retained", submitted.ID)
		}
		if !pause(a.ctx, 250*time.Millisecond) {
			fmt.Fprintln(a.errOut, "telegram: accepted request", submitted.ID, "remains queued or running")
			return context.Canceled
		}
	}
}
func printJob(w io.Writer, asJSON bool, j *job) error {
	if asJSON {
		return json.NewEncoder(w).Encode(j)
	}
	_, err := fmt.Fprintf(w, "%s\t%s\n", j.ID, j.Status)
	return err
}
