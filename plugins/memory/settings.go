package main

import (
	"bytes"
	"fmt"
	"time"

	"github.com/ReanSn0w/horizon/internal/config"
	"gopkg.in/yaml.v3"
)

type settings struct {
	Threshold                           int    `yaml:"threshold"`
	Interval                            string `yaml:"interval"`
	Model                               string `yaml:"model"`
	NoteLimit                           int    `yaml:"note_limit"`
	SummaryLimit                        int    `yaml:"summary_limit"`
	ContextLimit                        int    `yaml:"context_limit"`
	APITimeout                          string `yaml:"api_timeout"`
	RetryInterval                       string `yaml:"retry_interval"`
	MaxPendingNotes                     int    `yaml:"max_pending_notes"`
	MaxPendingBytes                     int    `yaml:"max_pending_bytes"`
	interval, apiTimeout, retryInterval time.Duration
}

func loadSettings(cfg config.Config) (settings, error) {
	s := settings{Threshold: 5, Interval: "24h", Model: cfg.DefaultModel, NoteLimit: 4096, SummaryLimit: 16384, ContextLimit: 16000, APITimeout: "45s", RetryInterval: "5m", MaxPendingNotes: 256, MaxPendingBytes: 262144}
	if node, ok := cfg.Plugins["memory"]; ok {
		data, err := yaml.Marshal(&node)
		if err != nil {
			return s, err
		}
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		if err := decoder.Decode(&s); err != nil {
			return s, fmt.Errorf("invalid plugins.memory settings")
		}
	}
	if s.Threshold < 1 || s.Threshold > 256 || s.NoteLimit < 1 || s.NoteLimit > 16384 || s.SummaryLimit < 1 || s.SummaryLimit > 32768 || s.ContextLimit < 256 || s.ContextLimit > 32000 || s.MaxPendingNotes < 1 || s.MaxPendingNotes > 1024 || s.MaxPendingBytes < s.NoteLimit || s.MaxPendingBytes > 1<<20 {
		return s, fmt.Errorf("plugins.memory limits outside supported bounds")
	}
	if _, _, err := cfg.SelectModel(s.Model); err != nil {
		return s, fmt.Errorf("plugins.memory.model must name a configured profile")
	}
	var err error
	s.interval, err = time.ParseDuration(s.Interval)
	if err != nil || s.interval <= 0 {
		return s, fmt.Errorf("plugins.memory.interval must be positive")
	}
	s.apiTimeout, err = time.ParseDuration(s.APITimeout)
	if err != nil || s.apiTimeout <= 0 || s.apiTimeout > 45*time.Second {
		return s, fmt.Errorf("plugins.memory.api_timeout must be positive and at most 45s")
	}
	s.retryInterval, err = time.ParseDuration(s.RetryInterval)
	if err != nil || s.retryInterval <= 0 {
		return s, fmt.Errorf("plugins.memory.retry_interval must be positive")
	}
	return s, nil
}
