package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/ReanSn0w/horizon/internal/session"
)

type serviceOptions struct {
	Platform string `long:"platform" choice:"linux" choice:"darwin" description:"Target operating system (print only)"`
	Output   string `long:"output" description:"Destination file instead of the user service directory" value-name:"PATH"`
}

func serviceDefinition(platform, home, binary string) (string, []byte, error) {
	for _, path := range []string{home, binary} {
		if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
			return "", nil, fmt.Errorf("service paths must be absolute and contain no line breaks")
		}
	}
	id := "horizon-agent-" + session.WorkspaceID(home)
	switch platform {
	case "linux":
		quote := func(s string) string { return strconv.Quote(strings.ReplaceAll(s, "%", "%%")) }
		execPath := quote(strings.ReplaceAll(binary, "$", "$$"))
		content := fmt.Sprintf("[Unit]\nDescription=Horizon agent\n\n[Service]\nType=exec\nExecStart=%s start\nEnvironment=%s\nWorkingDirectory=%s\nRestart=on-failure\nRestartSec=5\nUMask=0077\n\n[Install]\nWantedBy=default.target\n", execPath, quote("HORIZON_HOME="+home), quote(home))
		return id + ".service", []byte(content), nil
	case "darwin":
		escape := func(s string) string { var b bytes.Buffer; _ = xml.EscapeText(&b, []byte(s)); return b.String() }
		content := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>start</string></array>
<key>EnvironmentVariables</key><dict><key>HORIZON_HOME</key><string>%s</string></dict>
<key>WorkingDirectory</key><string>%s</string>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
<key>ThrottleInterval</key><integer>5</integer>
<key>Umask</key><integer>63</integer>
</dict></plist>
`, id, escape(binary), escape(home), escape(home))
		return id + ".plist", []byte(content), nil
	default:
		return "", nil, fmt.Errorf("service files are supported on Linux and macOS")
	}
}

func service(home, action string, options serviceOptions, out io.Writer) error {
	platform := options.Platform
	if platform == "" {
		platform = runtime.GOOS
	}
	if action == "install" && platform != runtime.GOOS {
		return fmt.Errorf("use service print to generate files for another platform")
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	name, data, err := serviceDefinition(platform, home, binary)
	if err != nil {
		return err
	}
	if action == "print" {
		if options.Output != "" {
			return fmt.Errorf("--output requires service install")
		}
		_, err = out.Write(data)
		return err
	}
	if os.Getenv("HORIZON_INHERITED_ACCESS") != "" && os.Getenv("HORIZON_INHERITED_ACCESS") != "full" {
		return fmt.Errorf("service installation outside Horizon home requires full access")
	}
	path := options.Output
	if path == "" {
		userHome, e := os.UserHomeDir()
		if e != nil {
			return e
		}
		if platform == "darwin" {
			path = filepath.Join(userHome, "Library", "LaunchAgents", name)
		} else {
			configHome := os.Getenv("XDG_CONFIG_HOME")
			if configHome == "" {
				configHome = filepath.Join(userHome, ".config")
			}
			if !filepath.IsAbs(configHome) {
				return fmt.Errorf("XDG_CONFIG_HOME must be absolute")
			}
			path = filepath.Join(configHome, "systemd", "user", name)
		}
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return err
	}
	if filepath.Ext(path) != filepath.Ext(name) || strings.ContainsAny(path, "\x00\r\n") {
		return fmt.Errorf("service output must end in %s and contain no line breaks", filepath.Ext(name))
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
		return err
	}
	fmt.Fprintln(out, "Installed service file:", path)
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	if platform == "darwin" {
		fmt.Fprintf(out, "Start: launchctl bootstrap gui/$(id -u) %s\nStop before updating: launchctl bootout gui/$(id -u) %s\n", quote(path), quote(path))
	} else {
		fmt.Fprintf(out, "Start and enable: systemctl --user enable --now %s\nStop before updating: systemctl --user stop %s\n", quote(path), quote(filepath.Base(path)))
	}
	return nil
}
