package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ReanSn0w/horizon/internal/config"
	"gopkg.in/yaml.v3"
)

type groupSettings struct {
	OwnerOnly    bool   `yaml:"owner_only" json:"owner_only"`
	ResponseMode string `yaml:"response_mode" json:"response_mode"`
}
type settings struct {
	Telegram struct {
		Token string `yaml:"bot_token"`
		Owner int64  `yaml:"owner_user_id"`
	} `yaml:"telegram"`
	Workspace     string        `yaml:"workspace_dir"`
	PrivateAccess string        `yaml:"private_access"`
	GroupAccess   string        `yaml:"group_access"`
	Defaults      groupSettings `yaml:"group_defaults"`
	Conversation  struct {
		History   int     `yaml:"history_messages"`
		Threshold float64 `yaml:"reply_threshold"`
	} `yaml:"conversation"`
	Parallel int                      `yaml:"max_parallel_chats"`
	Groups   map[string]groupSettings `yaml:"groups"`
}

func defaults(home string) settings {
	var s settings
	s.Workspace = filepath.Join(home, "workspaces", "telegram")
	s.PrivateAccess = "write"
	s.GroupAccess = "read"
	s.Defaults = groupSettings{true, "mention"}
	s.Conversation.History = 20
	s.Conversation.Threshold = .7
	s.Parallel = 2
	s.Groups = map[string]groupSettings{}
	return s
}
func nodeValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}
func putNode(n *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content[i+1] = value
			return
		}
	}
	n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}
func encodedNode(value any) *yaml.Node { var n yaml.Node; _ = n.Encode(value); return &n }
func mergeMissing(target, source *yaml.Node) bool {
	changed := false
	for i := 0; i < len(source.Content); i += 2 {
		k := source.Content[i].Value
		v := nodeValue(target, k)
		if v == nil {
			putNode(target, k, source.Content[i+1])
			changed = true
		} else if v.Kind == yaml.MappingNode && source.Content[i+1].Kind == yaml.MappingNode {
			if mergeMissing(v, source.Content[i+1]) {
				changed = true
			}
		}
	}
	return changed
}
func loadSettings(home string, ready bool) (settings, error) {
	data, err := os.ReadFile(filepath.Join(home, "config.yaml"))
	if err != nil {
		return settings{}, err
	}
	var document yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err = dec.Decode(&document); err != nil {
		return settings{}, errors.New("invalid config.yaml; check YAML syntax")
	}
	var extra yaml.Node
	if err = dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return settings{}, errors.New("config.yaml must contain one document")
	}
	if len(document.Content) != 1 {
		return settings{}, errors.New("invalid config.yaml")
	}
	section := nodeValue(nodeValue(document.Content[0], "plugins"), "gateway")
	if section == nil {
		return settings{}, errors.New("plugins.gateway is missing; run 'horizon gateway init'")
	}
	payload, err := yaml.Marshal(section)
	if err != nil {
		return settings{}, err
	}
	s := defaults(home)
	dec = yaml.NewDecoder(bytes.NewReader(payload))
	dec.KnownFields(true)
	if err = dec.Decode(&s); err != nil {
		return s, errors.New("invalid plugins.gateway; check field names and types")
	}
	if !filepath.IsAbs(s.Workspace) {
		s.Workspace = filepath.Join(home, s.Workspace)
	}
	s.Workspace = filepath.Clean(s.Workspace)
	if s.Groups == nil {
		s.Groups = map[string]groupSettings{}
	}
	if err = s.validate(ready); err != nil {
		return s, err
	}
	return s, nil
}
func validAccess(s string) bool { return s == "read" || s == "write" || s == "full" }
func (s settings) validate(ready bool) error {
	if ready && (strings.TrimSpace(s.Telegram.Token) == "" || s.Telegram.Owner <= 0) {
		return errors.New("set plugins.gateway.telegram.bot_token and a positive owner_user_id")
	}
	if s.Telegram.Owner < 0 || !validAccess(s.PrivateAccess) || !validAccess(s.GroupAccess) || s.Workspace == "" || s.Parallel < 1 || s.Parallel > 32 || s.Conversation.History < 1 || s.Conversation.History > 100 || !(s.Conversation.Threshold >= 0 && s.Conversation.Threshold <= 1) {
		return errors.New("invalid gateway access, workspace, owner or limits")
	}
	if s.Defaults.ResponseMode != "mention" && s.Defaults.ResponseMode != "conversation" {
		return errors.New("response_mode must be mention or conversation")
	}
	for id, g := range s.Groups {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil || n >= 0 {
			return errors.New("gateway group IDs must be negative decimal integers")
		}
		if g.ResponseMode != "mention" && g.ResponseMode != "conversation" {
			return errors.New("group response_mode must be mention or conversation")
		}
	}
	return nil
}
func initSettings(home string) (bool, error) {
	return config.UpdateDocument(home, func(doc *yaml.Node) (bool, error) {
		root := doc.Content[0]
		plugins := nodeValue(root, "plugins")
		if plugins == nil {
			plugins = encodedNode(map[string]any{})
			putNode(root, "plugins", plugins)
		}
		if plugins.Kind != yaml.MappingNode {
			return false, errors.New("plugins must be a mapping")
		}
		gateway := nodeValue(plugins, "gateway")
		if gateway == nil {
			putNode(plugins, "gateway", encodedNode(defaults(home)))
			return true, nil
		}
		if gateway.Kind != yaml.MappingNode {
			return false, errors.New("plugins.gateway must be a mapping")
		}
		return mergeMissing(gateway, encodedNode(defaults(home))), nil
	})
}
func ensureGroup(home string, id int64) (groupSettings, error) {
	var result groupSettings
	_, err := config.UpdateDocument(home, func(doc *yaml.Node) (bool, error) {
		gateway := nodeValue(nodeValue(doc.Content[0], "plugins"), "gateway")
		if gateway == nil {
			return false, errors.New("run gateway init")
		}
		s, err := loadSettings(home, false)
		if err != nil {
			return false, err
		}
		key := strconv.FormatInt(id, 10)
		if g, ok := s.Groups[key]; ok {
			result = g
			return false, nil
		}
		result = s.Defaults
		groups := nodeValue(gateway, "groups")
		if groups == nil {
			groups = encodedNode(map[string]any{})
			putNode(gateway, "groups", groups)
		}
		if groups.Kind != yaml.MappingNode {
			return false, errors.New("groups must be a mapping")
		}
		putNode(groups, key, encodedNode(result))
		return true, nil
	})
	return result, err
}
func checkInherited(s settings) error {
	if access, ok := os.LookupEnv("HORIZON_INHERITED_ACCESS"); ok && (access != s.PrivateAccess || access != s.GroupAccess) {
		return fmt.Errorf("HORIZON_INHERITED_ACCESS conflicts with gateway private_access/group_access; run gateway outside the inherited agent turn")
	}
	return nil
}

func migrateGroup(home string, oldID, newID int64) error {
	_, err := config.UpdateDocument(home, func(doc *yaml.Node) (bool, error) {
		groups := nodeValue(nodeValue(nodeValue(doc.Content[0], "plugins"), "gateway"), "groups")
		if groups == nil {
			return false, errors.New("missing group settings")
		}
		oldKey, newKey := strconv.FormatInt(oldID, 10), strconv.FormatInt(newID, 10)
		old := nodeValue(groups, oldKey)
		if old == nil {
			return false, nil
		}
		if nodeValue(groups, newKey) != nil {
			return false, nil
		}
		putNode(groups, newKey, old)
		return true, nil
	})
	return err
}
