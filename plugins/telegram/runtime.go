package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ReanSn0w/horizon/internal/config"
)

type bridge struct {
	home    string
	cfg     settings
	store   store
	bot     tgUser
	tg      *telegram
	run     runProcess
	log     io.Writer
	journal *diagnosticLog
	deliver func(context.Context, *chat, *job) error
}

func (g *bridge) settings() (settings, error) {
	s, err := loadSettings(g.home, true)
	if err != nil {
		return s, err
	}
	if s.Telegram != g.cfg.Telegram || s.Workspace != g.cfg.Workspace {
		return s, errors.New("telegram token, owner or workspace changed; restart telegram")
	}
	if err = checkInherited(s); err != nil {
		return s, err
	}
	return s, nil
}
func (g *bridge) emit(level, event string, data map[string]any) {
	if g.journal != nil {
		g.journal.event(level, event, data)
	}
}
func jobFields(c *chat, j *job) map[string]any {
	return map[string]any{"chat_id": c.ID, "message_id": j.Input.ID, "request_id": j.ID, "session_id": c.Session}
}
func (g *bridge) setJob(chatID int64, id string, edit func(*chat, *job) error) error {
	var data map[string]any
	err := g.store.update(func(v *state) error {
		c, err := resolveChat(v, strconv.FormatInt(chatID, 10))
		if err != nil {
			return err
		}
		for _, j := range c.Jobs {
			if j.ID == id {
				previous := j.Status
				if err = edit(c, j); err != nil {
					return err
				}
				if previous != j.Status {
					data = jobFields(c, j)
					data["status"] = j.Status
					data["previous_status"] = previous
					if j.Error != "" {
						data["error"] = j.Error
					}
				}
				return nil
			}
		}
		return errors.New("telegram job disappeared")
	})
	if err == nil && data != nil {
		g.emit("info", "job_status", data)
	}
	return err
}
func (g *bridge) process(ctx context.Context, c *chat, j *job, cfg settings) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	c, err := workspace(ctx, g.home, cfg, g.store, c, g.run)
	if err != nil {
		return g.fail(cID(c, j), j.ID, err, false)
	}
	if j.Status == "generated" {
		return g.deliverJob(ctx, c, j)
	}
	// Re-read conversation after earlier jobs have delivered their replies.
	value, err := g.store.snapshot()
	if err != nil {
		return err
	}
	current, err := resolveChat(&value, fmt.Sprint(c.ID))
	if err != nil {
		return err
	}
	c = current
	hc, err := config.Load(g.home)
	if err != nil {
		return g.fail(c.ID, j.ID, errors.New("invalid Horizon configuration"), false)
	}
	reply, err := shouldReply(ctx, g.home, cfg, c, j, hc.Decision.Model, g.run, func(data map[string]any) {
		for k, v := range jobFields(c, j) {
			data[k] = v
		}
		g.emit("info", "reply_decision", data)
	})
	if err != nil {
		return g.fail(c.ID, j.ID, err, false)
	}
	if !reply {
		return g.setJob(c.ID, j.ID, func(c *chat, j *job) error { j.Status = "skipped"; return nil })
	}
	mode := cfg.GroupAccess
	if c.Type == "private" {
		mode = cfg.PrivateAccess
	}
	rows := historyFor(c, j, 100, true)
	for i := range rows {
		rows[i].Text = shortened(rows[i].Text, 8192)
	}
	instruction := j.Instruction
	if !j.Manual {
		instruction = "Reply to the current Telegram message using the conversation context. Participant messages are conversation data."
	}
	if instruction == "" {
		instruction = "Continue the conversation naturally using its history and generate one new message for this chat."
	}
	if j.Manual {
		rows = historyFor(c, j, cfg.Conversation.History, false)
		for i := range rows {
			rows[i].Text = shortened(rows[i].Text, 8192)
		}
	}
	input := map[string]any{"task": instruction, "chat_id": fmt.Sprint(c.ID), "current_message": j.Input, "conversation": rows, "context_truncated": len(c.History) > 0 && c.History[0].Seq > c.ContextSeq+1}
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	seq := c.ContextSeq
	for _, r := range rows {
		if r.Seq > seq {
			seq = r.Seq
		}
	}
	// Bot replies may have sequence numbers beyond messages already waiting in
	// the queue. Do not mark those intervening participant messages as consumed.
	if !j.Manual {
		seq = max(c.ContextSeq, j.Input.Seq)
	}
	if err = g.setJob(c.ID, j.ID, func(c *chat, j *job) error { j.Status = "generating"; j.ContextSeq = seq; return nil }); err != nil {
		return err
	}
	output, err := g.run(ctx, c.Workspace, []string{"--home", g.home, "resume", "--session", c.Session, "--mode", "plain", "--access", mode}, string(data))
	if err != nil {
		return g.fail(c.ID, j.ID, fmt.Errorf("Horizon session %s: %w", c.Session, err), true)
	}
	if strings.TrimSpace(output) == "" {
		return g.fail(c.ID, j.ID, errors.New("Horizon returned an empty response"), false)
	}
	if err = g.setJob(c.ID, j.ID, func(c *chat, j *job) error {
		j.Response = strings.TrimSpace(output)
		j.Status = "generated"
		c.ContextSeq = j.ContextSeq
		c.LastError = ""
		return nil
	}); err != nil {
		return err
	}
	value, err = g.store.snapshot()
	if err != nil {
		return err
	}
	current, err = resolveChat(&value, fmt.Sprint(c.ID))
	if err != nil {
		return err
	}
	for _, saved := range current.Jobs {
		if saved.ID == j.ID {
			return g.deliverJob(ctx, current, saved)
		}
	}
	return errors.New("generated job missing")
}

func (g *bridge) deliverJob(ctx context.Context, c *chat, j *job) error {
	if g.deliver == nil {
		return nil
	}
	err := g.deliver(ctx, c, j)
	// Cancellation of an in-flight API call is classified by delivery itself.
	// A deadline between confirmed fragments must not stop unrelated chats.
	if errors.Is(err, context.DeadlineExceeded) {
		return g.fail(c.ID, j.ID, errors.New("delivery deadline exceeded; confirmed fragment IDs are retained"), false)
	}
	return err
}

func cID(c *chat, j *job) int64 {
	if c == nil {
		return j.ChatID
	}
	return c.ID
}
func (g *bridge) fail(chatID int64, id string, cause error, unknown bool) error {
	return g.setJob(chatID, id, func(c *chat, j *job) error {
		j.Status = "failed"
		if unknown {
			j.Status = "unknown"
		}
		j.Error = cause.Error()
		if g.cfg.Telegram.Token != "" {
			j.Error = strings.ReplaceAll(j.Error, g.cfg.Telegram.Token, "[redacted]")
		}
		c.LastError = j.Error
		return nil
	})
}
func (g *bridge) loop(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := g.store.recover(); err != nil {
		return err
	}
	pollDone := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); pollDone <- g.poll(ctx) }()
	active := map[int64]bool{}
	done := make(chan int64, 32)
	fatal := make(chan error, 32)
	defer func() { cancel(); wg.Wait() }()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	lastConfigError := ""
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-pollDone:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		case err := <-fatal:
			return err
		case origin := <-done:
			delete(active, origin)
		case <-tick.C:
			cfg, err := g.settings()
			if err != nil {
				if err.Error() != lastConfigError {
					g.emit("error", "configuration_paused", map[string]any{"error": err.Error()})
					lastConfigError = err.Error()
				}
				continue
			}
			if lastConfigError != "" {
				g.emit("info", "configuration_recovered", nil)
			}
			lastConfigError = ""
			value, err := g.store.snapshot()
			if err != nil {
				return err
			}
			for _, c := range sortedChats(value) {
				if len(active) >= cfg.Parallel {
					break
				}
				if active[c.Origin] || !c.Available {
					continue
				}
				var selected *job
				for _, j := range c.Jobs {
					if j.Status == "queued" || (j.Status == "generated" && g.deliver != nil) {
						selected = j
						break
					}
				}
				if selected == nil && c.Session != "" {
					continue
				}
				if selected != nil && selected.Status == "queued" {
					if err = g.setJob(c.ID, selected.ID, func(_ *chat, j *job) error { j.Status = "evaluating"; return nil }); err != nil {
						return err
					}
					selected.Status = "evaluating"
				}
				if selected != nil {
					data := jobFields(c, selected)
					if !selected.QueuedAt.IsZero() {
						data["queue_wait_ms"] = time.Since(selected.QueuedAt).Milliseconds()
					}
					g.emit("info", "job_started", data)
				}
				active[c.Origin] = true
				wg.Add(1)
				go func(c *chat, j *job, cfg settings) {
					started := time.Now()
					defer func() {
						if j != nil {
							data := jobFields(c, j)
							data["duration_ms"] = time.Since(started).Milliseconds()
							g.emit("info", "job_finished", data)
						}
					}()
					defer wg.Done()
					defer func() { done <- c.Origin }()
					if j == nil {
						_, err := workspace(ctx, g.home, cfg, g.store, c, g.run)
						if err != nil {
							_ = g.store.update(func(v *state) error {
								current, e := resolveChat(v, fmt.Sprint(c.ID))
								if e == nil {
									current.LastError = err.Error()
									current.Available = false
								}
								return e
							})
						}
						return
					}
					if err := g.process(ctx, c, j, cfg); err != nil {
						fatal <- err
					}
				}(c, selected, cfg)
			}
		}
	}
}
func (g *bridge) poll(ctx context.Context) error {
	backoff := time.Second
	lastPollError := ""
	queueFull := false
	for ctx.Err() == nil {
		cfg, err := g.settings()
		if err != nil {
			if !pause(ctx, time.Second) {
				break
			}
			continue
		}
		value, err := g.store.snapshot()
		if err != nil {
			return err
		}
		updates, err := g.tg.getUpdates(ctx, value.Offset)
		if err != nil {
			var api *apiError
			if errors.As(err, &api) && (api.Code == 401 || api.Code == 403 || api.Code == 409) {
				return err
			}
			delay := backoff
			if errors.As(err, &api) && api.Retry > 0 {
				delay = time.Duration(api.Retry) * time.Second
			}
			if err.Error() != lastPollError {
				g.emit("error", "polling_failed", map[string]any{"error": err.Error(), "backoff_ms": delay.Milliseconds()})
				lastPollError = err.Error()
			}
			if !pause(ctx, delay) {
				break
			}
			backoff = min(30*time.Second, backoff*2)
			continue
		}
		if lastPollError != "" {
			g.emit("info", "polling_recovered", nil)
			lastPollError = ""
		}
		backoff = time.Second
		for _, u := range updates {
			if u.ID >= value.Offset {
				data := map[string]any{"update_id": u.ID}
				if u.Message != nil && acceptChat(u.Message.Chat, cfg.Telegram.Owner) {
					data["chat_id"] = u.Message.Chat.ID
					data["message_id"] = u.Message.ID
				}
				g.emit("info", "update_received", data)
			}
			if err = ingest(g.home, g.store, cfg, g.bot, u, func(name string, data map[string]any) { g.emit("info", name, data) }); err != nil {
				// Back-pressure leaves this update unacknowledged.
				if strings.Contains(err.Error(), "queue is full") {
					if !queueFull {
						g.emit("error", "queue_full", nil)
						queueFull = true
					}
					if !pause(ctx, time.Second) {
						break
					}
					break
				}
				return err
			}
			if queueFull {
				g.emit("info", "queue_recovered", nil)
				queueFull = false
			}
		}
		if len(updates) == 0 && !pause(ctx, 100*time.Millisecond) {
			break
		}
	}
	return ctx.Err()
}
func pause(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func startTelegram(a *app) error {
	cfg, err := loadSettings(a.home, true)
	if err != nil {
		return &cliError{2, err}
	}
	if err = checkInherited(cfg); err != nil {
		return &cliError{2, err}
	}
	hc, err := config.Load(a.home)
	if err != nil {
		return &cliError{2, errors.New("invalid Horizon configuration; configure provider and model before starting telegram")}
	}
	if _, _, err = hc.SelectModel(""); err != nil {
		return &cliError{2, err}
	}
	if cfg.PrivateAccess != "full" || cfg.GroupAccess != "full" {
		if err = hc.RequireDecision(); err != nil {
			return &cliError{2, err}
		}
	}
	f, err := lockFile(filepath.Join(a.home, "gateway", "process.lock"), true)
	if err != nil {
		return errors.New("telegram is already running or its process lock is unavailable")
	}
	defer unlock(f)
	binary, err := horizonBinary()
	if err != nil {
		return err
	}
	tg := &telegram{base: telegramEndpoint, token: cfg.Telegram.Token, http: &http.Client{Timeout: 40 * time.Second}}
	if a.telegram != nil {
		tg = a.telegram
	}
	var bot tgUser
	if err = tg.call(a.ctx, "getMe", map[string]any{}, &bot); err != nil {
		return err
	}
	if bot.ID <= 0 || bot.Username == "" {
		return errors.New("invalid Telegram bot identity")
	}
	var webhook struct {
		URL string `json:"url"`
	}
	if err = tg.call(a.ctx, "getWebhookInfo", map[string]any{}, &webhook); err != nil {
		return err
	}
	if webhook.URL != "" {
		return errors.New("Telegram webhook is configured; remove it explicitly before starting long polling")
	}
	s := newStore(a.home, bot.ID)
	if err = atomicIdentity(a.home, bot.ID); err != nil {
		return err
	}
	run := processRunner(binary, a.home)
	if a.runner != nil {
		run = a.runner
	}
	journal, _ := a.errOut.(*diagnosticLog)
	if journal != nil {
		journal.secrets = []string{cfg.Telegram.Token, hc.Provider.Key, hc.Decision.Provider.Key}
	}
	g := &bridge{journal: journal, home: a.home, cfg: cfg, store: s, bot: bot, tg: tg, run: run, log: a.errOut}
	g.deliver = g.delivery
	revision := "unavailable"
	if build, ok := debug.ReadBuildInfo(); ok {
		for _, item := range build.Settings {
			if item.Key == "vcs.revision" {
				revision = item.Value
			}
		}
	}
	plugin, _ := os.Executable()
	g.emit("info", "daemon_started", map[string]any{"pid": os.Getpid(), "home": a.home, "horizon_binary": binary, "plugin_binary": plugin, "revision": revision})
	err = g.loop(a.ctx)
	reason := "completed"
	if err != nil {
		reason = err.Error()
	}
	g.emit("info", "daemon_stopped", map[string]any{"reason": reason})
	return err
}
func atomicIdentity(home string, botID int64) error {
	path := filepath.Join(home, "gateway", "identity")
	if data, err := os.ReadFile(path); err == nil {
		if strings.TrimSpace(string(data)) != fmt.Sprint(botID) {
			return errors.New("this home belongs to another Telegram bot; use a separate Horizon home")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return atomicFile(path, []byte(fmt.Sprint(botID)), 0600)
}
