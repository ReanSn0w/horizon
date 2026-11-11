package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/ReanSn0w/horizon/internal/daemon"
	"github.com/ReanSn0w/horizon/internal/telegram"
)

type typingAction struct {
	chatID int64
	thread int64
	stop   context.CancelFunc
}

func (a *adapter) observe(ctx context.Context) {
	ticks := time.NewTicker(500 * time.Millisecond)
	defer ticks.Stop()
	var wg sync.WaitGroup
	active := map[string]typingAction{}
	defer func() {
		for _, action := range active {
			action.stop()
		}
		wg.Wait()
	}()
	nextMaintenance := time.Now()
	for {
		now := time.Now()
		registry, err := a.store.Snapshot()
		if err == nil {
			snapshot, err := a.running(ctx)
			if err != nil {
				snapshot = nil
			}
			a.refreshTyping(ctx, snapshot, registry, active, &wg, a.client.Typing)
			a.cancelStaleIdle(ctx, registry)
			if !now.Before(nextMaintenance) {
				a.requestIdle(ctx, registry, now)
				nextMaintenance = now.Add(time.Minute)
			}
		} else {
			a.refreshTyping(ctx, nil, telegram.Registry{}, active, &wg, a.client.Typing)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticks.C:
		}
	}
}

func (a *adapter) running(ctx context.Context) ([]daemon.Task, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	response, err := a.request(ctx, daemon.Request{Method: "list_running"})
	if err != nil {
		return nil, err
	}
	var result struct {
		Tasks []daemon.Task `json:"tasks"`
	}
	err = json.Unmarshal(response.Data, &result)
	return result.Tasks, err
}

func (a *adapter) refreshTyping(ctx context.Context, tasks []daemon.Task, registry telegram.Registry,
	active map[string]typingAction, wg *sync.WaitGroup, send func(context.Context, int64, int64) error) {
	wanted := map[string]*telegram.Conversation{}
	for _, task := range tasks {
		if task.State != "running" || task.IdleCompact || !task.Channel {
			continue
		}
		origin, err := strconv.ParseInt(task.Route, 10, 64)
		if err != nil {
			continue
		}
		if chat := registry.Chats[origin]; chat != nil && chat.Available && (chat.ID < 0 || chat.ID == a.bot.Owner) {
			wanted[task.ID] = chat
		}
	}
	for id, action := range active {
		chat := wanted[id]
		if chat == nil || chat.ID != action.chatID || chat.Thread != action.thread {
			action.stop()
			delete(active, id)
		}
	}
	for id, chat := range wanted {
		if _, exists := active[id]; exists {
			continue
		}
		actionCtx, stop := context.WithCancel(ctx)
		active[id] = typingAction{chat.ID, chat.Thread, stop}
		wg.Go(func() { runTyping(actionCtx, chat.ID, chat.Thread, send, 4*time.Second) })
	}
}

func runTyping(ctx context.Context, chat, thread int64, send func(context.Context, int64, int64) error, interval time.Duration) {
	ticks := time.NewTicker(interval)
	defer ticks.Stop()
	for ctx.Err() == nil {
		request, cancel := context.WithTimeout(ctx, 2*time.Second)
		_ = send(request, chat, thread)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticks.C:
		}
	}
}

func (a *adapter) requestIdle(ctx context.Context, registry telegram.Registry, now time.Time) {
	threshold := a.cfg.IdleDuration()
	if threshold <= 0 {
		return
	}
	for _, chat := range registry.Chats {
		if ctx.Err() != nil {
			return
		}
		if !chat.Available || chat.Workspace == "" || chat.LastInput.IsZero() || now.Sub(chat.LastInput) < threshold ||
			chat.IdleFor.Equal(chat.LastInput) || registry.Pending != nil && registry.Pending.Route == chat.Route() {
			continue
		}
		operation := fmt.Sprintf("telegram:%d:idle:%s:%d", a.bot.BotID(), chat.Route(), chat.LastInput.UnixNano())
		req := daemon.Request{Method: "idle_compact", Route: chat.Route(), OperationID: operation, Caller: daemon.Caller{Workspace: chat.Workspace}}
		callCtx, cancel := context.WithTimeout(ctx, time.Second)
		response, err := a.request(callCtx, req)
		cancel()
		if err != nil {
			continue
		}
		var task daemon.Task
		if json.Unmarshal(response.Data, &task) != nil || task.ID == "" || !task.IdleCompact {
			continue
		}
		_ = a.store.Update(func(value *telegram.Registry) error {
			if current := value.Chats[chat.Origin]; current != nil {
				current.IdleFor = chat.LastInput
				current.IdleTask = task.ID
				current.LastCompact = now
			}
			return nil
		})
	}
}

func (a *adapter) cancelStaleIdle(ctx context.Context, registry telegram.Registry) {
	for _, chat := range registry.Chats {
		if ctx.Err() != nil {
			return
		}
		if chat.IdleTask == "" || !chat.LastInput.After(chat.IdleFor) {
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, time.Second)
		_, err := a.request(callCtx, daemon.Request{Method: "idle_cancel", TaskID: chat.IdleTask, OperationID: "idle-cancel:" + chat.IdleTask})
		cancel()
		if err != nil {
			continue
		}
		_ = a.store.Update(func(value *telegram.Registry) error {
			if current := value.Chats[chat.Origin]; current != nil && current.IdleTask == chat.IdleTask {
				current.IdleTask = ""
			}
			return nil
		})
	}
}
