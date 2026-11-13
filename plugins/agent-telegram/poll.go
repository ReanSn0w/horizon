package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/daemon"
	"github.com/ReanSn0w/horizon/internal/maintenance"
	"github.com/ReanSn0w/horizon/internal/plugins"
	"github.com/ReanSn0w/horizon/internal/telegram"
)

func start(ctx context.Context) error {
	home, err := config.ResolveHome("")
	if err != nil {
		return errors.New("cannot resolve Telegram adapter home")
	}
	credential := os.Getenv("HORIZON_ADAPTER_CREDENTIAL")
	if os.Getenv("HORIZON_ADAPTER_NAME") != "telegram" || os.Getenv("HORIZON_DAEMON_SOCKET") != daemon.Socket(home) || credential == "" {
		return errors.New("agent-telegram must be started by horizon agent")
	}
	bot, settings, err := validateSettings(home)
	if err != nil {
		return err
	}
	path, err := telegram.PollLock(home)
	if err != nil {
		return err
	}
	release, err := maintenance.Lock(path, true)
	if err != nil {
		return errors.New("Telegram polling lock is busy; stop the previous Telegram service")
	}
	defer release()
	if err := telegram.CheckIdentity(home, bot.BotID()); err != nil {
		return err
	}
	if err := telegram.MarkUse(home); err != nil {
		return err
	}
	client := telegram.New(bot.Token)
	me, err := client.Identity(ctx)
	if err != nil || me.ID != bot.BotID() {
		return errors.New("cannot verify configured Telegram bot identity")
	}
	if err := client.CheckPolling(ctx); err != nil {
		return err
	}
	if err := telegram.BindIdentity(home, me.ID); err != nil {
		return err
	}
	a := &adapter{home: home, cfg: settings, bot: bot, username: me.Username, credential: credential, store: telegram.NewStore(home, me.ID), client: client, call: daemon.Call}
	if err := a.hello(ctx); err != nil {
		return err
	}
	observerCtx, stop := context.WithCancel(ctx)
	observerDone := make(chan struct{})
	go func() {
		defer close(observerDone)
		a.observe(observerCtx)
	}()
	err = a.poll(ctx)
	stop()
	<-observerDone
	return err
}

func validateSettings(home string) (telegram.BotSettings, telegram.AdapterSettings, error) {
	cfg, err := config.Load(home)
	if err != nil {
		return telegram.BotSettings{}, telegram.AdapterSettings{}, fmt.Errorf("load Horizon configuration: %w", err)
	}
	bot, err := telegram.ReadBot(cfg)
	if err != nil {
		return bot, telegram.AdapterSettings{}, err
	}
	settings, err := telegram.ReadAdapter(cfg, home)
	if err != nil {
		return bot, settings, err
	}
	if !slices.Contains(cfg.AgentPlugins, "telegram") || !slices.Contains(cfg.AgentCommunication, "telegram") {
		return bot, settings, errors.New("Telegram input requires telegram in agent_plugins and agent_communication")
	}
	extension := plugins.Inspect(home, "telegram", nil)
	if extension.Err != nil || extension.Metadata.AgentProtocolVersion != plugins.ChannelProtocolVersion {
		return bot, settings, errors.New("Telegram input requires the installed horizon-telegram v2 extension")
	}
	if settings.PrivateAccess != "full" || settings.GroupAccess != "full" {
		if err := cfg.RequireDecision(); err != nil {
			return bot, settings, err
		}
	}
	return bot, settings, nil
}

func (a *adapter) hello(ctx context.Context) error {
	response, err := a.request(ctx, daemon.Request{Method: "hello"})
	if err != nil {
		return err
	}
	var hello struct {
		Capabilities []string `json:"capabilities"`
	}
	if json.Unmarshal(response.Data, &hello) != nil || !slices.Contains(hello.Capabilities, daemon.ActiveInputCapability) {
		return errors.New("Telegram requires daemon active-input capability")
	}
	// A read validates the supervisor's per-start adapter credential.
	_, err = a.request(ctx, daemon.Request{Method: "list"})
	return err
}

func (a *adapter) poll(ctx context.Context) error {
	for ctx.Err() == nil {
		err := a.pollOnce(ctx)
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			a.report(err)
			delay := time.Second
			var api *telegram.APIError
			if errors.As(err, &api) && api.Retry > 0 {
				delay = time.Duration(min(api.Retry, 300)) * time.Second
			}
			wait(ctx, delay)
		}
	}
	return nil
}

func (a *adapter) pollOnce(ctx context.Context) error {
	if err := a.flush(ctx); err != nil {
		return err
	}
	value, err := a.store.Snapshot()
	if err != nil {
		return err
	}
	updates, err := a.client.Updates(ctx, value.Offset)
	if err != nil {
		return err
	}
	slices.SortStableFunc(updates, func(a, b telegram.Update) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	for _, update := range updates {
		if err := a.receive(ctx, update); err != nil {
			return err
		}
	}
	if len(updates) == 0 {
		wait(ctx, 100*time.Millisecond)
	}
	return nil
}

func (a *adapter) report(err error) {
	var failure *daemon.Failure
	var api *telegram.APIError
	message := "Telegram polling or input persistence failed"
	switch {
	case errors.As(err, &failure):
		message = "daemon rejected Telegram input: " + failure.Code
	case errors.As(err, &api):
		message = fmt.Sprintf("Telegram polling returned API status %d", api.Code)
	}
	_ = telegram.WritePrivate(filepath.Join(a.home, "gateway", "telegram-adapter.error"), []byte(message+"\n"), 0600)
}

func wait(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
