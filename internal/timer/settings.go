package timer

import (
	"bytes"
	"errors"
	"time"

	"github.com/ReanSn0w/horizon/internal/config"
	"gopkg.in/yaml.v3"
)

const defaultReload = 30 * time.Second

type Settings struct {
	Access         string
	ReloadInterval time.Duration
}

func ConfigTemplate() map[string]any {
	return map[string]any{"access": "write", "reload_interval": defaultReload.String()}
}

func LoadSettings(home string) (Settings, error) {
	cfg, err := config.Load(home)
	if err != nil {
		return Settings{}, errors.New("cannot load Horizon configuration")
	}
	raw := struct {
		Access string `yaml:"access"`
		Reload string `yaml:"reload_interval"`
	}{Access: "write", Reload: defaultReload.String()}
	if node, ok := cfg.Plugins["agent-timer"]; ok {
		b, err := yaml.Marshal(node)
		if err != nil {
			return Settings{}, errors.New("invalid plugins.agent-timer settings")
		}
		decoder := yaml.NewDecoder(bytes.NewReader(b))
		decoder.KnownFields(true)
		if err := decoder.Decode(&raw); err != nil {
			return Settings{}, errors.New("invalid plugins.agent-timer settings")
		}
	}
	if accessLevel(raw.Access) == 0 {
		return Settings{}, errors.New("plugins.agent-timer.access must be read, write or full")
	}
	reload, err := time.ParseDuration(raw.Reload)
	if err != nil || reload < time.Second {
		return Settings{}, errors.New("plugins.agent-timer.reload_interval must be a Go duration of at least 1s")
	}
	return Settings{Access: raw.Access, ReloadInterval: reload}, nil
}

func accessLevel(value string) int {
	switch value {
	case "read":
		return 1
	case "write":
		return 2
	case "full":
		return 3
	}
	return 0
}
