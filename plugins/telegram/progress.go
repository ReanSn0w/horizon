package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"
)

const (
	defaultProgressDelay    = time.Minute
	defaultProgressInterval = 2 * time.Minute
)

type progressReporter struct {
	events chan string
	stopFn func()
}

func (p progressReporter) observe(event childEvent) {
	stage := progressStage(event)
	if stage == "" || p.events == nil {
		return
	}
	select {
	case p.events <- stage:
	default:
		// Keep the most recent stage without blocking the Horizon event stream.
		select {
		case <-p.events:
		default:
		}
		select {
		case p.events <- stage:
		default:
		}
	}
}

func (p progressReporter) stop() {
	if p.stopFn != nil {
		p.stopFn()
	}
}

func progressStage(event childEvent) string {
	switch event.Type {
	case "model_request_started":
		return "Ожидаю модель"
	case "model_request_completed":
		return "Обрабатываю ответ модели"
	case "tool_started":
		var name string
		_ = json.Unmarshal(event.Data["name"], &name)
		if name == "shell_exec" {
			return "Выполняю команду"
		}
		return "Работаю с инструментом"
	case "tool_completed":
		return "Обрабатываю результат инструмента"
	}
	return ""
}

func (g *bridge) startProgress(parent context.Context, cfg settings, c *chat, j *job) progressReporter {
	if g.tg == nil || cfg.ProgressMessages == "off" || (cfg.ProgressMessages == "private" && c.Type != "private") {
		return progressReporter{}
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	p := progressReporter{events: make(chan string, 1)}
	var once sync.Once
	p.stopFn = func() { once.Do(func() { cancel(); <-done }) }
	delay := g.progressDelay
	if delay <= 0 {
		delay = defaultProgressDelay
	}
	interval := g.progressInterval
	if interval <= 0 {
		interval = defaultProgressInterval
	}
	tick := g.progressTick
	if tick <= 0 {
		tick = 5 * time.Second
	}
	go func() {
		defer close(done)
		started := time.Now()
		stage := "Обрабатываю запрос"
		var lastSent time.Time
		var progressID int64
		var retryAfter time.Time
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case next := <-p.events:
				stage = next
			case <-ticker.C:
			}
			now := time.Now()
			if now.Sub(started) < delay || now.Before(retryAfter) || (!lastSent.IsZero() && now.Sub(lastSent) < interval) {
				continue
			}
			text := fmt.Sprintf("Работаю · %d мин · %s", max(1, int(now.Sub(started)/time.Minute)), stage)
			requestCtx, stop := context.WithTimeout(ctx, 3*time.Second)
			var err error
			if progressID == 0 {
				progressID, err = g.tg.send(requestCtx, c, j, text)
			} else {
				err = g.tg.edit(requestCtx, c.ID, progressID, text)
			}
			stop()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				var api *apiError
				if errors.As(err, &api) && api.Code == 429 {
					retryAfter = now.Add(time.Duration(max(api.Retry, 1)) * time.Second)
					g.emit("info", "progress_rate_limited", map[string]any{"chat_id": c.ID, "request_id": j.ID, "retry_after": max(api.Retry, 1)})
					continue
				}
				g.emit("error", "progress_failed", map[string]any{"chat_id": c.ID, "request_id": j.ID, "error": err.Error()})
				return // The outcome of a send may be unknown; never send it again blindly.
			}
			if lastSent.IsZero() {
				if err := g.setJob(c.ID, j.ID, func(_ *chat, saved *job) error { saved.ProgressID = progressID; return nil }); err != nil {
					g.emit("error", "progress_state_failed", map[string]any{"chat_id": c.ID, "request_id": j.ID, "error": err.Error()})
					return
				}
			}
			lastSent = now
		}
	}()
	return p
}

func (g *bridge) finishProgress(chatID int64, jobID string) {
	if g.tg == nil {
		return
	}
	value, err := g.store.snapshot()
	if err != nil {
		return
	}
	c, err := resolveChat(&value, strconv.FormatInt(chatID, 10))
	if err != nil {
		return
	}
	var current *job
	for _, candidate := range c.Jobs {
		if candidate.ID == jobID {
			current = candidate
			break
		}
	}
	if current == nil || current.ProgressID == 0 {
		return
	}
	var action func(context.Context) error
	switch current.Status {
	case "sent", "skipped":
		action = func(ctx context.Context) error { return g.tg.deleteMessage(ctx, c.ID, current.ProgressID) }
	case "failed":
		action = func(ctx context.Context) error {
			return g.tg.edit(ctx, c.ID, current.ProgressID, "Работа завершилась с ошибкой.")
		}
	case "unknown":
		action = func(ctx context.Context) error {
			return g.tg.edit(ctx, c.ID, current.ProgressID, "Работа прервана; итог неизвестен.")
		}
	default:
		return
	}
	parent := g.store.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	err = action(ctx)
	var api *apiError
	if errors.As(err, &api) && api.Code == 429 {
		delay := time.Duration(max(api.Retry, 1)) * time.Second
		if delay <= 10*time.Second && pause(ctx, delay) {
			err = action(ctx)
		}
	}
	if err != nil {
		g.emit("error", "progress_finish_failed", map[string]any{"chat_id": c.ID, "request_id": jobID, "error": err.Error()})
		return
	}
	if err := g.setJob(c.ID, jobID, func(_ *chat, saved *job) error { saved.ProgressID = 0; return nil }); err != nil {
		g.emit("error", "progress_state_failed", map[string]any{"chat_id": c.ID, "request_id": jobID, "error": err.Error()})
	}
}

func (g *bridge) recoverProgress(ctx context.Context) {
	value, err := g.store.snapshot()
	if err != nil {
		return
	}
	for _, c := range value.Chats {
		for _, j := range c.Jobs {
			if ctx.Err() != nil {
				return
			}
			if j.ProgressID != 0 {
				g.finishProgress(c.ID, j.ID)
			}
		}
	}
}
