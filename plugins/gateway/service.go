package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type serviceInfo struct {
	Manager string `yaml:"manager"`
	Name    string `yaml:"name"`
	Path    string `yaml:"path"`
}
type serviceHost struct {
	platform, userHome, configHome string
	uid                            int
	run                            func(context.Context, string, ...string) (string, error)
}

func nativeServiceHost() serviceHost {
	home, _ := os.UserHomeDir()
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	return serviceHost{runtime.GOOS, home, configHome, os.Getuid(), func(ctx context.Context, name string, args ...string) (string, error) {
		binary, err := exec.LookPath(name)
		if err != nil {
			return "", fmt.Errorf("%s is unavailable", name)
		}
		return processRunner(binary, "")(ctx, "", args, "")
	}}
}
func serviceName(home string) string {
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	if absolute, err := filepath.Abs(home); err == nil {
		home = absolute
	}
	sum := sha256.Sum256([]byte(home))
	return fmt.Sprintf("io.horizon.gateway.%x", sum[:8])
}
func xmlText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
func launchPlist(name, binary, home, path string) []byte {
	args := []string{binary, "--home", home, "gateway", "start", "--log-file", filepath.Join(home, "gateway", "service.log")}
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!-- Horizon gateway managed: " + name + " -->\n<plist version=\"1.0\"><dict>\n")
	b.WriteString("<key>Label</key><string>" + xmlText(name) + "</string>\n<key>ProgramArguments</key><array>")
	for _, arg := range args {
		b.WriteString("<string>" + xmlText(arg) + "</string>")
	}
	b.WriteString("</array>\n")
	b.WriteString("<key>RunAtLoad</key><true/>\n<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>\n<key>ThrottleInterval</key><integer>10</integer>\n")
	b.WriteString("<key>EnvironmentVariables</key><dict><key>PATH</key><string>" + xmlText(path) + "</string></dict>\n")
	b.WriteString("<key>StandardOutPath</key><string>/dev/null</string>\n<key>StandardErrorPath</key><string>/dev/null</string>\n</dict></plist>\n")
	return []byte(b.String())
}
func manageService(a *app, opt *serviceCommand, host serviceHost) error {
	action := opt.Args.Action
	switch action {
	case "install", "uninstall", "start", "stop", "restart", "status":
	default:
		return &cliError{2, errors.New("service action must be install, uninstall, start, stop, restart or status")}
	}
	home, err := filepath.EvalSymlinks(a.home)
	if err != nil {
		return err
	}
	home, err = filepath.Abs(home)
	if err != nil {
		return err
	}
	f, err := lockFile(filepath.Join(home, "gateway", "service.lock"), false)
	if err != nil {
		return err
	}
	defer unlock(f)
	descriptor := filepath.Join(home, "gateway", "service.yaml")
	var info serviceInfo
	data, readErr := os.ReadFile(descriptor)
	if readErr == nil {
		if yaml.Unmarshal(data, &info) != nil {
			return errors.New("invalid service descriptor")
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	manager := opt.Manager
	if manager == "" {
		manager = info.Manager
	}
	if manager == "" {
		if host.platform == "darwin" {
			manager = "launchd"
		} else if host.platform == "linux" {
			manager = "systemd"
		}
	}
	if manager == "launchd" && host.platform != "darwin" || manager == "systemd" && host.platform != "linux" || manager == "" {
		return errors.New("service manager is unsupported on this platform")
	}
	name := serviceName(home)
	target := filepath.Join(host.userHome, "Library", "LaunchAgents", name+".plist")
	if manager == "systemd" {
		target = filepath.Join(host.configHome, "systemd", "user", name+".service")
	}
	if readErr == nil && (info.Manager != manager || info.Name != name || info.Path != target) {
		return errors.New("service descriptor does not match this home and manager")
	}
	if action == "install" {
		binary, err := horizonBinary()
		if err != nil {
			return err
		}
		if strings.ContainsAny(home+binary+target, "\r\n\x00") {
			return errors.New("service paths contain unsupported control characters")
		}
		var content []byte
		if manager == "launchd" {
			content = launchPlist(name, binary, home, os.Getenv("PATH"))
		} else {
			return errors.New("systemd service support is not configured")
		}
		if old, err := os.ReadFile(target); err == nil {
			if !strings.Contains(string(old), "Horizon gateway managed: "+name) {
				return errors.New("refusing to overwrite an unrelated service")
			}
			if bytes.Equal(old, content) && readErr == nil {
				if manager == "launchd" {
					if _, err := host.run(a.ctx, "launchctl", "enable", "gui/"+strconv.Itoa(host.uid)+"/"+name); err != nil {
						return err
					}
				}
				fmt.Fprintln(a.out, "Service already installed:", target)
				return nil
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		if err = atomicFile(target, content, 0600); err != nil {
			return err
		}
		info = serviceInfo{manager, name, target}
		data, _ = yaml.Marshal(info)
		if err = atomicFile(descriptor, data, 0600); err != nil {
			return err
		}
		if manager == "launchd" {
			if _, err := host.run(a.ctx, "launchctl", "enable", "gui/"+strconv.Itoa(host.uid)+"/"+name); err != nil {
				return err
			}
		}
		fmt.Fprintln(a.out, "Service installed:", target)
		return nil
	}
	if readErr != nil {
		if action == "status" {
			fmt.Fprintln(a.out, "Service: not installed")
			return nil
		}
		if action == "uninstall" {
			return nil
		}
		return errors.New("service is not installed; run gateway service install")
	}
	current, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	if !strings.Contains(string(current), "Horizon gateway managed: "+name) {
		return errors.New("service file is not owned by this gateway")
	}
	if manager == "systemd" {
		return errors.New("systemd service support is not configured")
	}
	domain := "gui/" + strconv.Itoa(host.uid)
	label := domain + "/" + name
	loaded := func() bool { _, err := host.run(a.ctx, "launchctl", "print", label); return err == nil }
	switch action {
	case "status":
		enabled := true
		if output, err := host.run(a.ctx, "launchctl", "print-disabled", domain); err == nil && strings.Contains(output, "\""+name+"\" => true") {
			enabled = false
		}
		fmt.Fprintf(a.out, "Installed: true\nEnabled: %t\nLoaded: %t\nRunning: %t\n", enabled, loaded(), gatewayRunning(home))
		return nil
	case "start":
		if _, err = host.run(a.ctx, "launchctl", "enable", label); err != nil {
			return err
		}
		if !loaded() {
			if _, err = host.run(a.ctx, "launchctl", "bootstrap", domain, target); err != nil {
				return err
			}
		}
		_, err = host.run(a.ctx, "launchctl", "kickstart", label)
	case "restart":
		if _, err = host.run(a.ctx, "launchctl", "enable", label); err != nil {
			return err
		}
		if !loaded() {
			if _, err = host.run(a.ctx, "launchctl", "bootstrap", domain, target); err != nil {
				return err
			}
		}
		_, err = host.run(a.ctx, "launchctl", "kickstart", "-k", label)
	case "stop", "uninstall":
		if _, err = host.run(a.ctx, "launchctl", "disable", label); err != nil {
			return err
		}
		if loaded() {
			if _, err = host.run(a.ctx, "launchctl", "bootout", label); err != nil {
				return err
			}
		}
		if action == "uninstall" {
			if err = os.Remove(target); err != nil {
				return err
			}
			err = os.Remove(descriptor)
		}
	}
	return err
}
