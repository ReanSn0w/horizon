package telegram

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ReanSn0w/horizon/internal/config"
	"gopkg.in/yaml.v3"
)

type BotSettings struct {
	Token string `yaml:"bot_token" json:"bot_token"`
	Owner int64  `yaml:"owner_user_id" json:"owner_user_id"`
}

type GroupSettings struct {
	OwnerOnly bool   `yaml:"owner_only" json:"owner_only"`
	Mode      string `yaml:"response_mode" json:"response_mode"`
}

type AdapterSettings struct {
	Workspace     string                   `yaml:"workspace_dir" json:"workspace_dir"`
	PrivateAccess string                   `yaml:"private_access" json:"private_access"`
	GroupAccess   string                   `yaml:"group_access" json:"group_access"`
	Defaults      GroupSettings            `yaml:"group_defaults" json:"group_defaults"`
	Groups        map[string]GroupSettings `yaml:"groups" json:"groups"`
	History       int                      `yaml:"history_messages" json:"history_messages"`
	IdleAfter     string                   `yaml:"idle_compact_after" json:"idle_compact_after"`
}

func DefaultAdapter(home string) AdapterSettings {
	return AdapterSettings{
		Workspace:     filepath.Join(home, "workspaces", "telegram"),
		PrivateAccess: "write", GroupAccess: "read",
		Defaults: GroupSettings{OwnerOnly: true, Mode: "mention"},
		Groups:   map[string]GroupSettings{},
		History:  20, IdleAfter: "12h",
	}
}

func ReadBot(cfg config.Config) (BotSettings, error) {
	var value BotSettings
	section, ok := cfg.Plugins["telegram"]
	if !ok {
		return value, errors.New("configure plugins.telegram.bot_token and owner_user_id")
	}
	if yamlKey(&section, "telegram") != nil {
		return value, errors.New("legacy Telegram configuration; run horizon telegram migrate --check")
	}
	if err := decodeSection(section, &value); err != nil {
		return value, errors.New("invalid plugins.telegram fields")
	}
	if value.Owner <= 0 || strings.TrimSpace(value.Token) == "" || strings.ContainsAny(value.Token, " /?#\\\r\n\t") {
		return value, errors.New("set plugins.telegram.bot_token and a positive owner_user_id")
	}
	return value, nil
}

func ReadAdapter(cfg config.Config, home string) (AdapterSettings, error) {
	value := DefaultAdapter(home)
	section, ok := cfg.Plugins["agent-telegram"]
	if !ok {
		return value, errors.New("configure plugins.agent-telegram or install agent:telegram with manager")
	}
	if err := decodeSection(section, &value); err != nil {
		return value, errors.New("invalid plugins.agent-telegram fields")
	}
	if strings.TrimSpace(value.Workspace) == "" || !validAccess(value.PrivateAccess) || !validAccess(value.GroupAccess) || value.History < 1 || value.History > 100 {
		return value, errors.New("invalid Telegram adapter workspace, access or history limit")
	}
	if !filepath.IsAbs(value.Workspace) {
		value.Workspace = filepath.Join(home, value.Workspace)
	}
	value.Workspace = filepath.Clean(value.Workspace)
	after, err := time.ParseDuration(value.IdleAfter)
	if err != nil || after < 0 || after > 0 && after < time.Hour {
		return value, errors.New("plugins.agent-telegram.idle_compact_after must be 0 or at least 1h")
	}
	if !validMode(value.Defaults.Mode) {
		return value, errors.New("Telegram group_defaults.response_mode must be mention or conversation")
	}
	for id, group := range value.Groups {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil || n >= 0 || id != strconv.FormatInt(n, 10) || !validMode(group.Mode) {
			return value, errors.New("Telegram groups require negative decimal IDs and response_mode mention or conversation")
		}
	}
	return value, nil
}

func (a AdapterSettings) Group(id, origin int64) GroupSettings {
	if group, ok := a.Groups[strconv.FormatInt(id, 10)]; ok {
		return group
	}
	if group, ok := a.Groups[strconv.FormatInt(origin, 10)]; ok {
		return group
	}
	return a.Defaults
}

func (a AdapterSettings) IdleDuration() time.Duration {
	d, _ := time.ParseDuration(a.IdleAfter)
	return d
}

func validAccess(value string) bool { return value == "read" || value == "write" || value == "full" }
func validMode(value string) bool   { return value == "mention" || value == "conversation" }

func decodeSection(section yaml.Node, target any) error {
	if section.Kind != yaml.MappingNode {
		return fmt.Errorf("expected plugin settings mapping")
	}
	data, err := yaml.Marshal(section)
	if err != nil {
		return err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	return decoder.Decode(target)
}

func yamlKey(mapping *yaml.Node, key string) *yaml.Node {
	if mapping != nil && mapping.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(mapping.Content); i += 2 {
			if mapping.Content[i].Value == key {
				return mapping.Content[i+1]
			}
		}
	}
	return nil
}
