package main

import (
	"errors"
	"fmt"
)

func execute(a *app, name string, value any) error {
	if err := a.resolveHome(); err != nil {
		return err
	}
	if name == "init" {
		changed, err := initSettings(a.home)
		if err != nil {
			return &cliError{2, err}
		}
		fmt.Fprintf(a.out, "Gateway configuration: %s/config.yaml (updated: %t)\nSet plugins.gateway.telegram.bot_token and owner_user_id, then run horizon gateway start.\n", a.home, changed)
		return nil
	}
	return errors.New("gateway command is not configured")
}
