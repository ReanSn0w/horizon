package main

import (
	"context"
	"time"
)

const typingRefresh = 4 * time.Second

func (g *bridge) startTyping(parent context.Context, c *chat, j *job) func() {
	if g.tg == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	interval := typingRefresh
	if g.typingInterval > 0 {
		interval = g.typingInterval
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		lastError := ""
		for {
			if ctx.Err() != nil {
				return
			}
			requestCtx, stop := context.WithTimeout(ctx, 3*time.Second)
			err := g.tg.typing(requestCtx, c, j)
			stop()
			if err != nil && ctx.Err() == nil && err.Error() != lastError {
				g.emit("error", "typing_failed", map[string]any{"chat_id": c.ID, "request_id": j.ID, "error": err.Error()})
				lastError = err.Error()
			} else if err == nil {
				lastError = ""
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
