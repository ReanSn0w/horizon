package main

import (
	"errors"
)

func execute(a *app, name string, value any) error {
	if err := a.resolveHome(); err != nil {
		return err
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
