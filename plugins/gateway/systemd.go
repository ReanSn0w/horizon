package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

func unitQuote(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	value = strings.ReplaceAll(value, "%", "%%")
	value = strings.ReplaceAll(value, "$", "$$")
	value = strings.ReplaceAll(value, "\n", "\\n")
	value = strings.ReplaceAll(value, "\r", "\\r")
	return "\"" + value + "\""
}
func systemdUnit(name, binary, home, path string) []byte {
	args := []string{binary, "--home", home, "gateway", "start"}
	quoted := []string{}
	for _, arg := range args {
		quoted = append(quoted, unitQuote(arg))
	}
	return []byte(fmt.Sprintf("# Horizon gateway managed: %s\n[Unit]\nDescription=Horizon Telegram gateway\nStartLimitIntervalSec=60\nStartLimitBurst=5\n\n[Service]\nType=simple\nExecStart=%s\nEnvironment=%s\nRestart=on-failure\nRestartSec=10\nKillMode=control-group\nTimeoutStopSec=15\nUMask=0077\nStandardOutput=journal\nStandardError=journal\n\n[Install]\nWantedBy=default.target\n", name, strings.Join(quoted, " "), unitQuote("PATH="+path)))
}
func systemdLifecycle(a *app, action string, info serviceInfo, descriptor string, host serviceHost) error {
	name := info.Name + ".service"
	call := func(args ...string) (string, error) {
		return host.run(a.ctx, "systemctl", append([]string{"--user"}, args...)...)
	}
	if _, err := call("show-environment"); err != nil {
		return errors.New("systemd user manager is unavailable; check the user session or configure lingering")
	}
	switch action {
	case "install":
		if _, err := call("daemon-reload"); err != nil {
			return err
		}
		_, err := call("enable", name)
		return err
	case "status":
		enabled, err := call("is-enabled", name)
		if err != nil {
			enabled = "disabled"
		}
		active, err := call("is-active", name)
		if err != nil {
			active = "inactive"
		}
		fmt.Fprintf(a.out, "Installed: true\nEnabled: %s\nActive: %s\nRunning: %t\n", strings.TrimSpace(enabled), strings.TrimSpace(active), gatewayRunning(a.home))
		return nil
	case "start", "stop", "restart":
		_, err := call(action, name)
		return err
	case "uninstall":
		if _, err := call("disable", "--now", name); err != nil {
			return err
		}
		if err := os.Remove(info.Path); err != nil {
			return err
		}
		if _, err := call("daemon-reload"); err != nil {
			return err
		}
		return os.Remove(descriptor)
	}
	return errors.New("unknown systemd action")
}
