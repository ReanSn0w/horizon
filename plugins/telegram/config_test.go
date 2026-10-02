package main

import (
	"bytes"
	"fmt"
	"github.com/ReanSn0w/horizon/internal/config"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func configHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("# keep\nmode: unit\ndisabled_skills: []\nplugins:\n  other:\n    custom: value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := initSettings(home); err != nil {
		t.Fatal(err)
	}
	return home
}
func TestConfigPreservation(t *testing.T) {
	home := configHome(t)
	before, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
	if changed, err := initSettings(home); err != nil || changed {
		t.Fatalf("%t %v", changed, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := ensureGroup(home, -123); err != nil {
				t.Error(err)
			}
			if _, err := config.UpdateDisabledSkills(home, "test", true, nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	data, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
	if !strings.Contains(string(data), "# keep") || !strings.Contains(string(data), "custom: value") {
		t.Fatal("lost unrelated settings")
	}
	s, err := loadSettings(home, false)
	if err != nil || !s.Groups["-123"].OwnerOnly || s.GroupAccess != "read" {
		t.Fatalf("%+v %v", s, err)
	}
	if !strings.Contains(string(before), "owner_user_id: 0") {
		t.Fatal("incorrect defaults")
	}
	target := filepath.Join(home, "config.yaml")
	alias := t.TempDir()
	if err := os.Symlink(target, filepath.Join(alias, "config.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureGroup(alias, -456); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(filepath.Join(alias, "config.yaml")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("replaced symlink")
	}
}
func TestConfigValidation(t *testing.T) {
	home := configHome(t)
	if _, err := loadSettings(home, true); err == nil {
		t.Fatal("accepted missing credentials")
	}
	p := filepath.Join(home, "config.yaml")
	data, _ := os.ReadFile(p)
	data = append(data, []byte("\ninvalid: [\n")...)
	os.WriteFile(p, data, 0600)
	if _, err := loadSettings(home, false); err == nil {
		t.Fatal("accepted invalid YAML")
	}
	s := defaults(home)
	t.Setenv("HORIZON_INHERITED_ACCESS", "write")
	if checkInherited(s) == nil {
		t.Fatal("inherited mode overrides group read")
	}
}

func TestConversationBotNamesConfig(t *testing.T) {
	for _, tc := range []struct {
		name    string
		yaml    string
		want    []string
		invalid bool
	}{
		{"legacy", "", []string{}, false},
		{"empty", "bot_names: []", []string{}, false},
		{"aliases", `bot_names: ["Курису", "Кристина", "Kurisu"]`, []string{"Курису", "Кристина", "Kurisu"}, false},
		{"blank", `bot_names: ["  "]`, nil, true},
		{"scalar", "bot_names: Kurisu", nil, true},
		{"long", "bot_names: [" + strings.Repeat("я", 129) + "]", nil, true},
		{"many", "bot_names: [" + strings.Repeat("Kurisu, ", 32) + "Курису]", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			data := "plugins:\n  telegram:\n    conversation:\n      history_messages: 20\n"
			if tc.yaml != "" {
				data += "      " + tc.yaml + "\n"
			}
			if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := loadSettings(home, false)
			if tc.invalid {
				if err == nil {
					t.Fatal("accepted invalid bot_names")
				}
				return
			}
			if err != nil || !reflect.DeepEqual(cfg.Conversation.BotNames, tc.want) {
				t.Fatalf("bot_names=%v err=%v", cfg.Conversation.BotNames, err)
			}
			if _, err := initSettings(home); err != nil {
				t.Fatal(err)
			}
			after, err := loadSettings(home, false)
			if err != nil || !reflect.DeepEqual(after.Conversation.BotNames, tc.want) {
				t.Fatalf("init changed bot_names=%v err=%v", after.Conversation.BotNames, err)
			}
		})
	}
}

func TestInitMigratesLegacyPluginName(t *testing.T) {
	home := configHome(t)
	path := filepath.Join(home, "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte("  telegram:"), []byte("  gateway:"), 1)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if changed, err := initSettings(home); err != nil || !changed {
		t.Fatalf("migration: changed=%t err=%v", changed, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, bytes.Replace(data, []byte("  gateway:"), []byte("  telegram:"), 1)) {
		t.Fatal("migration changed values or unrelated configuration")
	}
	if _, err := loadSettings(home, false); err != nil {
		t.Fatal(err)
	}
	if changed, err := initSettings(home); err != nil || changed {
		t.Fatalf("second init: changed=%t err=%v", changed, err)
	}
}

func TestInitRejectsConflictingPluginNames(t *testing.T) {
	home := configHome(t)
	path := filepath.Join(home, "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("  gateway: {}\n")...)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := initSettings(home); err == nil {
		t.Fatal("accepted conflicting configurations")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, data) {
		t.Fatal("conflict changed configuration")
	}
}

func TestInitWritesBlockYAMLAndExpandsExistingInlineSettings(t *testing.T) {
	for _, initial := range []string{
		"mode: unit\n",
		"mode: unit\nplugins: {}\n",
		"# retained comment\nmode: unit\nplugins: {other: {custom: unchanged}, telegram: {telegram: {bot_token: test-token, owner_user_id: 7}, workspace_dir: ./chat-data, private_access: full, group_defaults: {owner_only: false, response_mode: conversation}, groups: {}}}\n",
	} {
		t.Run(fmt.Sprintf("case_%d", len(initial)), func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, "config.yaml")
			if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
				t.Fatal(err)
			}
			var before settings
			legacy := strings.Contains(initial, "test-token")
			if legacy {
				var err error
				before, err = loadSettings(home, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			if changed, err := initSettings(home); err != nil || !changed {
				t.Fatalf("init changed=%t err=%v", changed, err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var doc yaml.Node
			if err := yaml.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}
			plugins := nodeValue(doc.Content[0], "plugins")
			telegram := nodeValue(plugins, "telegram")
			for _, node := range []*yaml.Node{plugins, telegram, nodeValue(telegram, "telegram"), nodeValue(telegram, "group_defaults"), nodeValue(telegram, "conversation")} {
				if node == nil || node.Style&yaml.FlowStyle != 0 {
					t.Fatal("settings still use inline YAML")
				}
			}
			if legacy {
				after, err := loadSettings(home, false)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, after) || !strings.Contains(string(data), "# retained comment") {
					t.Fatal("format conversion changed settings or lost comments")
				}
				if other := nodeValue(plugins, "other"); other.Style&yaml.FlowStyle == 0 {
					t.Fatal("reformatted another plugin's own section")
				}
			}
			if changed, err := initSettings(home); err != nil || changed {
				t.Fatalf("repeated init changed=%t err=%v", changed, err)
			}
			again, _ := os.ReadFile(path)
			if !bytes.Equal(data, again) {
				t.Fatal("repeated init rewrote YAML")
			}
			if _, err := ensureGroup(home, -123); err != nil {
				t.Fatal(err)
			}
			data, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := yaml.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}
			groups := nodeValue(nodeValue(nodeValue(doc.Content[0], "plugins"), "telegram"), "groups")
			if groups.Style&yaml.FlowStyle != 0 || nodeValue(groups, "-123") == nil {
				t.Fatal("new group settings were written inline")
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("changed configuration permissions")
			}
		})
	}
}
