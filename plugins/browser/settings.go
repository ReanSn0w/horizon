package main

import (
	"bytes"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/ReanSn0w/horizon/internal/config"
	"gopkg.in/yaml.v3"
)

type settings struct {
	APIKey         string `yaml:"api_key"`
	APIURL         string `yaml:"api_url"`
	TimeoutMinutes int    `yaml:"timeout_minutes"`
	ActionTimeout  string `yaml:"action_timeout"`
	actionTimeout  time.Duration
}

func loadSettings(cfg config.Config) (settings, error) {
	s := settings{APIURL: "https://api.browser-use.com/api/v4", TimeoutMinutes: 10, ActionTimeout: "30s"}
	if node, ok := cfg.Plugins["browser"]; ok {
		data, err := yaml.Marshal(&node)
		if err != nil {
			return s, fmt.Errorf("invalid plugins.browser settings")
		}
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		if err := decoder.Decode(&s); err != nil {
			return s, fmt.Errorf("invalid plugins.browser settings")
		}
	}
	if strings.TrimSpace(s.APIKey) == "" {
		return s, fmt.Errorf("plugins.browser.api_key is required")
	}
	u, err := url.Parse(s.APIURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return s, fmt.Errorf("plugins.browser.api_url must be an HTTP(S) URL")
	}
	if s.TimeoutMinutes < 1 || s.TimeoutMinutes > 240 {
		return s, fmt.Errorf("plugins.browser.timeout_minutes must be 1..240")
	}
	s.actionTimeout, err = time.ParseDuration(s.ActionTimeout)
	if err != nil || s.actionTimeout < time.Second || s.actionTimeout > 60*time.Second {
		return s, fmt.Errorf("plugins.browser.action_timeout must be 1s..60s")
	}
	return s, nil
}
