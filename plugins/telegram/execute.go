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
		fmt.Fprintf(a.out, "Telegram configuration: %s/config.yaml (updated: %t)\nSet plugins.telegram.telegram.bot_token and owner_user_id, then run horizon telegram start.\n", a.home, changed)
		return nil
	}
	if name == "start" {
		opt := value.(*startCommand)
		journal, err := openDiagnosticLog(opt.LogFile, a.errOut)
		if err != nil {
			return err
		}
		a.errOut = journal
		return startTelegram(a)
	}
	if name == "list" {
		return listChats(a, value.(*listCommand))
	}
	if name == "send" {
		return sendChat(a, value.(*sendCommand))
	}
	if name == "service" {
		return manageService(a, value.(*serviceCommand), nativeServiceHost())
	}
	return errors.New("unknown telegram command")
}
