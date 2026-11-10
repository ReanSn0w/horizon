package daemon

import (
	"bytes"
	"context"
	"errors"
	"github.com/ReanSn0w/horizon/internal/config"
	"gopkg.in/yaml.v3"
)

func LoadSettings(home string) (Settings, error) {
	cfg, err := config.Load(home)
	if err != nil {
		return Settings{}, errors.New("cannot load Horizon configuration")
	}
	s := Defaults()
	if n, ok := cfg.Plugins["daemon"]; ok {
		b, e := yaml.Marshal(n)
		if e != nil {
			return s, e
		}
		d := yaml.NewDecoder(bytes.NewReader(b))
		d.KnownFields(true)
		if e = d.Decode(&s); e != nil {
			return s, errors.New("invalid plugins.daemon settings")
		}
	}
	return s, s.Validate()
}
func Start(ctx context.Context, home string) error {
	settings, err := LoadSettings(home)
	if err != nil {
		return err
	}
	var queueErr error
	err = serve(ctx, home, nil, func(ctx context.Context) (Handler, func(), error) {
		q, e := OpenQueue(home, settings, HorizonRunner(home))
		if e != nil {
			return nil, nil, e
		}
		cleanupAdapters, e := q.SuperviseAdapters(ctx)
		if e != nil {
			return nil, nil, e
		}
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- q.Run(runCtx) }()
		return q.Handle, func() { cancel(); cleanupAdapters(); queueErr = <-done }, nil
	})
	if err != nil {
		return err
	}
	return queueErr
}
