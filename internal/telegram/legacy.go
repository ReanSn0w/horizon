package telegram

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/daemon"
	"github.com/ReanSn0w/horizon/internal/session"
	"gopkg.in/yaml.v3"
)

type legacySettings struct {
	Telegram      BotSettings              `yaml:"telegram"`
	Workspace     string                   `yaml:"workspace_dir"`
	PrivateAccess string                   `yaml:"private_access"`
	GroupAccess   string                   `yaml:"group_access"`
	Defaults      GroupSettings            `yaml:"group_defaults"`
	Groups        map[string]GroupSettings `yaml:"groups"`
	Conversation  struct {
		BotNames  []string  `yaml:"bot_names"`
		History   int       `yaml:"history_messages"`
		IdleAfter string    `yaml:"idle_compact_after"`
		Threshold yaml.Node `yaml:"reply_threshold"`
	} `yaml:"conversation"`
	Parallel int    `yaml:"max_parallel_chats"`
	Progress string `yaml:"progress_messages"`
}

type legacyRecord struct {
	Entry    `yaml:",inline"`
	Sequence int64 `yaml:"seq"`
	Mention  bool  `yaml:"mention"`
}

type legacyJob struct {
	ID     string       `yaml:"id"`
	ChatID int64        `yaml:"chat_id"`
	State  string       `yaml:"status"`
	Input  legacyRecord `yaml:"input"`
	Manual bool         `yaml:"manual"`
}

type legacyChat struct {
	Origin         int64          `yaml:"origin"`
	ID             int64          `yaml:"id"`
	Type           string         `yaml:"type"`
	Title          string         `yaml:"title"`
	Available      bool           `yaml:"available"`
	Thread         int64          `yaml:"thread"`
	Workspace      string         `yaml:"workspace"`
	Session        string         `yaml:"session"`
	History        []legacyRecord `yaml:"history"`
	LastAt         time.Time      `yaml:"last_at"`
	LastReceivedAt time.Time      `yaml:"last_received_at"`
	LastCompact    time.Time      `yaml:"last_compact_attempt_at"`
	Jobs           []*legacyJob   `yaml:"jobs"`
}

type legacyGateway struct {
	Version int                    `yaml:"version"`
	BotID   int64                  `yaml:"bot_id"`
	Offset  int64                  `yaml:"offset"`
	Chats   map[string]*legacyChat `yaml:"chats"`
}

func readLegacyConfig(path, home string) (legacySettings, string, error) {
	var legacy legacySettings
	defaults := DefaultAdapter(home)
	legacy.Workspace, legacy.PrivateAccess, legacy.GroupAccess = defaults.Workspace, defaults.PrivateAccess, defaults.GroupAccess
	legacy.Defaults, legacy.Groups = defaults.Defaults, defaults.Groups
	legacy.Conversation.History, legacy.Conversation.IdleAfter = defaults.History, defaults.IdleAfter
	legacy.Parallel = 2
	bytes, err := os.ReadFile(path)
	if err != nil {
		return legacy, "", err
	}
	var document yaml.Node
	if yaml.Unmarshal(bytes, &document) != nil || len(document.Content) != 1 {
		return legacy, "", errors.New("invalid legacy configuration")
	}
	mapping := yamlKey(document.Content[0], "plugins")
	name := "telegram"
	section := yamlKey(mapping, name)
	if yamlKey(section, "telegram") == nil {
		if yamlKey(mapping, "gateway") != nil && section != nil {
			var existing BotSettings
			if decodeSection(*section, &existing) != nil || existing != (BotSettings{}) {
				return legacy, "", errors.New("new Telegram settings conflict with legacy plugins.gateway")
			}
		}
		name = "gateway"
		section = yamlKey(mapping, name)
	} else if yamlKey(mapping, "gateway") != nil {
		return legacy, "", errors.New("conflicting legacy plugins.telegram and plugins.gateway sections")
	}
	if section == nil || section.Kind != yaml.MappingNode {
		return legacy, "", errors.New("no legacy Telegram configuration found")
	}
	// Manager may have filled the new top-level defaults during plugin install.
	clean := *section
	clean.Content = nil
	for i := 0; i < len(section.Content); i += 2 {
		key := section.Content[i].Value
		value := section.Content[i+1]
		if key == "bot_token" || key == "owner_user_id" {
			if (key == "bot_token" && value.Value != "") || (key == "owner_user_id" && value.Value != "0") {
				return legacy, "", errors.New("remove conflicting new Telegram settings before migration")
			}
			continue
		}
		clean.Content = append(clean.Content, section.Content[i], value)
	}
	if err := decodeSection(clean, &legacy); err != nil {
		return legacy, "", errors.New("invalid legacy Telegram settings")
	}
	if legacy.Telegram.BotID() <= 0 || legacy.Telegram.Owner <= 0 || strings.ContainsAny(legacy.Telegram.Token, " /?#\\\r\n\t") ||
		legacy.Parallel < 1 || legacy.Parallel > 32 {
		return legacy, "", errors.New("invalid legacy bot identity, owner or parallel limit")
	}
	return legacy, name, nil
}

func (l legacySettings) adapter() AdapterSettings {
	return AdapterSettings{Workspace: l.Workspace, PrivateAccess: l.PrivateAccess, GroupAccess: l.GroupAccess,
		Defaults: l.Defaults, Groups: l.Groups, History: l.Conversation.History, IdleAfter: l.Conversation.IdleAfter}
}

func readLegacyGateway(path string, bot int64) (legacyGateway, error) {
	value := legacyGateway{Version: 1, BotID: bot, Chats: map[string]*legacyChat{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return value, nil
	}
	if err != nil {
		return value, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF ||
		value.Version != 1 || value.BotID != bot || value.Offset < 0 || value.Chats == nil {
		return value, errors.New("invalid legacy Telegram gateway state")
	}
	origins := map[int64]bool{}
	ids := map[string]bool{}
	for id, chat := range value.Chats {
		if chat == nil || id != strconv.FormatInt(chat.ID, 10) || chat.ID == 0 || chat.Origin == 0 || origins[chat.Origin] ||
			(chat.ID < 0) != (chat.Origin < 0) || chat.Workspace == "" && chat.Session != "" {
			return value, errors.New("invalid legacy Telegram chat binding")
		}
		origins[chat.Origin] = true
		for _, job := range chat.Jobs {
			if job == nil || job.ChatID != chat.ID || job.ID == "" || len(job.ID) > 128 || ids[job.ID] {
				return value, errors.New("invalid legacy Telegram job binding")
			}
			ids[job.ID] = true
			switch job.State {
			case "queued", "evaluating", "generating", "generated", "sending", "sent", "skipped", "failed", "unknown":
			default:
				return value, errors.New("invalid legacy Telegram job state")
			}
		}
	}
	return value, nil
}

func legacyStatePath(home string, bot int64) string {
	return filepath.Join(home, "gateway", "telegram", strconv.FormatInt(bot, 10), "state.yaml")
}

type migrationData struct {
	registry Registry
	dialogs  []daemon.ImportedDialog
	report   MigrationReport
}

func prepareLegacy(home string, old legacyGateway, bot BotSettings, settings AdapterSettings, apply bool) (migrationData, error) {
	data := migrationData{registry: Registry{Version: 1, Offset: old.Offset, Chats: map[int64]*Conversation{}}}
	keys := make([]string, 0, len(old.Chats))
	for key := range old.Chats {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	store := session.NewStore(home)
	for _, key := range keys {
		c := old.Chats[key]
		if c.ID > 0 && c.ID != bot.Owner {
			data.report.Archived += len(c.Jobs)
			continue
		}
		chat := &Conversation{ID: c.ID, Origin: c.Origin, Type: c.Type, Title: c.Title, Available: c.Available, Thread: c.Thread,
			Workspace: c.Workspace, LastInput: c.LastReceivedAt, LastCompact: c.LastCompact}
		if chat.LastInput.IsZero() {
			chat.LastInput = c.LastAt
		}
		data.registry.Chats[chat.Origin] = chat
		access := settings.GroupAccess
		if chat.ID > 0 {
			access = settings.PrivateAccess
		}
		dialog := daemon.ImportedDialog{Route: chat.Route(), Session: c.Session, Access: access, Channel: true}
		queuedRecords := map[int64]bool{}
		group := settings.Group(chat.ID, chat.Origin)
		for _, job := range c.Jobs {
			eligible := chat.ID > 0 && job.Input.Author == bot.Owner || chat.ID < 0 &&
				(!group.OwnerOnly || job.Input.Author == bot.Owner) && (group.Mode == "conversation" || job.Input.Mention)
			op := fmt.Sprintf("telegram:%d:legacy:%s", bot.BotID(), job.ID)
			switch {
			case (job.State == "queued" || job.State == "evaluating") && !job.Manual && eligible:
				if job.Input.ID <= 0 || strings.TrimSpace(job.Input.Text) == "" || job.Input.Author <= 0 {
					return data, errors.New("legacy pending input has invalid author, message ID or text")
				}
				payload, err := encodeLegacyInput(chat.ID, job.Input.Entry, legacyContext(c, job.Input, bot.Owner, settings.History))
				if err != nil {
					return data, err
				}
				queuedRecords[job.Input.ID] = true
				dialog.Jobs = append(dialog.Jobs, daemon.ImportedJob{OperationID: op, State: "queued",
					Author: fmt.Sprintf("Telegram user %d", job.Input.Author), Message: payload})
				data.report.Queued++
			case job.State == "unknown" || job.State == "generating" || job.State == "sending":
				dialog.Jobs = append(dialog.Jobs, daemon.ImportedJob{OperationID: op, State: "unknown"})
				data.report.Unknown++
			default:
				data.report.Archived++
			}
		}
		for _, record := range c.History {
			entry := record.Entry
			if chat.ID > 0 && !entry.Bot && entry.Author != bot.Owner {
				continue
			}
			entry.Trigger = queuedRecords[entry.ID]
			chat.History = append(chat.History, entry)
		}
		chat.History = chat.History[max(0, len(chat.History)-settings.History):]
		if c.Session != "" || len(dialog.Jobs) > 0 {
			if c.Workspace != "" || apply {
				workspace, err := Workspace(settings.Workspace, bot.BotID(), *chat)
				if err != nil {
					return data, err
				}
				chat.Workspace, dialog.Workspace = workspace, workspace
			}
			if c.Session != "" {
				workspace, err := store.ResolveWorkspace(chat.Workspace)
				if err != nil {
					return data, err
				}
				if _, err = store.ReadSnapshot(workspace, c.Session); err != nil {
					return data, fmt.Errorf("cannot verify the existing session for Telegram chat %d", chat.ID)
				}
			}
			data.dialogs = append(data.dialogs, dialog)
		}
	}
	data.report.Chats = len(data.registry.Chats)
	return data, nil
}

func legacyContext(chat *legacyChat, input legacyRecord, owner int64, limit int) []Entry {
	recent := []Entry{}
	for _, previous := range chat.History {
		if input.Sequence > 0 && previous.Sequence >= input.Sequence || input.Sequence == 0 && previous.ID >= input.ID {
			continue
		}
		if chat.ID > 0 && !previous.Bot && previous.Author != owner {
			continue
		}
		recent = append(recent, previous.Entry)
	}
	return recent[max(0, len(recent)-limit):]
}

func adapterSection(value AdapterSettings, home string) (AdapterSettings, error) {
	var node yaml.Node
	if err := node.Encode(value); err != nil {
		return AdapterSettings{}, err
	}
	return ReadAdapter(config.Config{Plugins: map[string]yaml.Node{"agent-telegram": node}}, home)
}
