package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/telegram"
)

func inspect(home string, args []string, out io.Writer) error {
	if args[0] == "list" && len(args) > 2 || args[0] == "list" && len(args) == 2 && args[1] != "--json" ||
		args[0] == "read" && (len(args) != 3 || args[1] != "--operation-id") {
		return errors.New("usage: horizon telegram list [--json] | read --operation-id ID")
	}
	cfg, err := config.Load(home)
	if err != nil {
		return errors.New("cannot load Horizon configuration")
	}
	bot, err := telegram.ReadBot(cfg)
	if err != nil {
		return err
	}
	store := telegram.NewStore(home, bot.BotID())
	if args[0] == "read" {
		value, err := store.ReadSend(args[2], 0)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(value)
	}
	registry, err := store.Snapshot()
	if err != nil {
		return err
	}
	type chat struct {
		ID        int64  `json:"chat_id"`
		Name      string `json:"name"`
		Available bool   `json:"available"`
		Type      string `json:"type"`
	}
	items := []chat{}
	for _, c := range registry.Sorted() {
		if c.ID > 0 && c.ID != bot.Owner {
			continue
		}
		name := c.Title
		if name == "" {
			name = fmt.Sprintf("Chat %d", c.ID)
		}
		items = append(items, chat{ID: c.ID, Name: name, Available: c.Available, Type: c.Type})
	}
	if len(args) == 2 {
		return json.NewEncoder(out).Encode(map[string]any{"schema_version": 2, "chats": items})
	}
	fmt.Fprintln(out, "CHAT ID\tNAME")
	for _, item := range items {
		fmt.Fprintf(out, "%d\t%s\n", item.ID, strings.Join(strings.Fields(item.Name), " "))
	}
	return nil
}
