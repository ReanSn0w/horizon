package memory

import (
	"time"

	"github.com/ReanSn0w/horizon/config"
)

type settings struct {
	Threshold                           int
	Model                               string
	NoteLimit                           int
	SummaryLimit                        int
	ContextLimit                        int
	MaxPendingNotes                     int
	MaxPendingBytes                     int
	interval, apiTimeout, retryInterval time.Duration
}

func defaultSettings(model string) settings {
	return settings{Threshold: 5, Model: model, NoteLimit: 4096, SummaryLimit: 16384, ContextLimit: 16000, MaxPendingNotes: 256, MaxPendingBytes: 262144, interval: 24 * time.Hour, apiTimeout: 45 * time.Second, retryInterval: 5 * time.Minute}
}

func loadSettings(cfg config.Config) (settings, error) {
	if cfg.Memory.Threshold == 0 {
		return defaultSettings(cfg.DefaultModel), nil
	}
	m := cfg.Memory
	return settings{Threshold: m.Threshold, Model: m.Model, NoteLimit: m.NoteLimit, SummaryLimit: m.SummaryLimit, ContextLimit: m.ContextLimit, MaxPendingNotes: m.MaxPendingNotes, MaxPendingBytes: m.MaxPendingBytes, interval: m.Interval, apiTimeout: m.APITimeout, retryInterval: m.RetryInterval}, nil
}
