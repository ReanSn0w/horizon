package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/ReanSn0w/horizon/internal/session"
)

type serviceOptions struct {
	Output string `long:"output" description:"Destination file instead of the user service directory" value-name:"PATH"`
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
<key>StandardOutPath</key><string>%s</string>
<key>StandardErrorPath</key><string>%s</string>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
<key>ThrottleInterval</key><integer>5</integer>
<key>Umask</key><integer>63</integer>
</dict></plist>
`, id, escape(binary), escape(home), escape(home), escape(filepath.Join(home, "run", "agent.stdout.log")), escape(filepath.Join(home, "run", "agent.stderr.log")))
		return id + ".plist", []byte(content), nil
	default:
		return "", nil, fmt.Errorf("service files are supported on Linux and macOS")
	}
}

func service(home, action string, options serviceOptions, out io.Writer) error {
	platform := runtime.GOOS
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	name, data, err := serviceDefinition(platform, home, binary)
	if err != nil {
		return err
	}
	if action != "install" && action != "remove" {
		return fmt.Errorf("unknown service action %q", action)
	}
	if os.Getenv("HORIZON_INHERITED_ACCESS") != "" && os.Getenv("HORIZON_INHERITED_ACCESS") != "full" {
		return fmt.Errorf("service file changes outside Horizon home require full access")
	}
	path := options.Output
	if path == "" {
		path, err = servicePath(platform, home)
		if err != nil {
			return err
		}
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return err
	}
	if filepath.Ext(path) != filepath.Ext(name) || strings.ContainsAny(path, "\x00\r\n") {
		return fmt.Errorf("service output must end in %s and contain no line breaks", filepath.Ext(name))
	}
	if action == "remove" {
		if options.Output != "" {
			return fmt.Errorf("remove accepts only the default service path")
		}
		if serviceLoaded(platform, home, path) {
			return fmt.Errorf("stop the service with horizon agent stop before removing its file")
		}
		if platform == "linux" && exec.Command("systemctl", "--user", "is-enabled", "--quiet", filepath.Base(path)).Run() == nil {
			if err := exec.Command("systemctl", "--user", "disable", filepath.Base(path)).Run(); err != nil {
				return fmt.Errorf("disable user service before removal: %w", err)
			}
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		if platform == "linux" {
			if err := exec.Command("systemctl", "--user", "daemon-reload").Run(); err != nil {
				return fmt.Errorf("reload user services after removal: %w", err)
			}
		}
		_, err = fmt.Fprintln(out, "Removed service file:", path)
		return err
	}
	if err := os.MkdirAll(filepath.Join(home, "run"), 0700); err != nil {
		return err
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
	return nil
}

func serviceLoaded(platform, home, path string) bool {
	switch platform {
	case "darwin":
		label := "horizon-agent-" + session.WorkspaceID(home)
		return exec.Command("launchctl", "print", "gui/"+strconv.Itoa(os.Getuid())+"/"+label).Run() == nil
	case "linux":
		return exec.Command("systemctl", "--user", "is-active", "--quiet", filepath.Base(path)).Run() == nil
	default:
		return false
	}
}

func servicePath(platform, home string) (string, error) {
	name := "horizon-agent-" + session.WorkspaceID(home)
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch platform {
	case "darwin":
		return filepath.Join(userHome, "Library", "LaunchAgents", name+".plist"), nil
	case "linux":
		configHome := os.Getenv("XDG_CONFIG_HOME")
		if configHome == "" {
			configHome = filepath.Join(userHome, ".config")
		}
		if !filepath.IsAbs(configHome) {
			return "", fmt.Errorf("XDG_CONFIG_HOME must be absolute")
		}
		return filepath.Join(configHome, "systemd", "user", name+".service"), nil
	default:
		return "", fmt.Errorf("service files are supported on Linux and macOS")
	}
}
