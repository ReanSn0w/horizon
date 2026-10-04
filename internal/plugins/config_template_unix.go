//go:build unix

package plugins

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/ReanSn0w/horizon/internal/config"
	"gopkg.in/yaml.v3"
)

const ConfigProtocolVersion = 1

// ReadConfigTemplate runs the optional, read-only initialization protocol.
// Process output is never included in errors because it could contain secrets.
func ReadConfigTemplate(entry Entry, home string) (config.PluginTemplate, error) {
	empty := config.PluginTemplate{}
	if entry.Metadata.ConfigProtocolVersion != ConfigProtocolVersion {
		return empty, fmt.Errorf("config template protocol unsupported")
	}
	executable, err := os.Executable()
	if err != nil {
		return empty, fmt.Errorf("locate Horizon executable: %w", err)
	}
	cmd := exec.Command(entry.Path, "horizon-plugin-config-template")
	cmd.Env = Environment(os.Environ(), home, executable)
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout := &cappedBuffer{exceeded: make(chan struct{})}
	stderr := &cappedBuffer{exceeded: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		return empty, fmt.Errorf("start config template command: %w", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	timer := time.NewTimer(MetadataTimeout)
	defer timer.Stop()
	select {
	case err = <-waited:
	case <-timer.C:
		killGroup(cmd)
		<-waited
		return empty, fmt.Errorf("config template timed out")
	case <-stdout.exceeded:
		killGroup(cmd)
		<-waited
		return empty, fmt.Errorf("config template stdout exceeds limit")
	case <-stderr.exceeded:
		killGroup(cmd)
		<-waited
		return empty, fmt.Errorf("config template stderr exceeds limit")
	}
	if err != nil || stdout.once || stderr.once {
		return empty, fmt.Errorf("config template command failed")
	}
	var response struct {
		Version     int             `json:"config_protocol_version"`
		Section     json.RawMessage `json:"section"`
		LegacyNames []string        `json:"legacy_names,omitempty"`
	}
	if err := Decode(stdout.buffer.Bytes(), &response); err != nil || response.Version != ConfigProtocolVersion || len(response.Section) == 0 {
		return empty, fmt.Errorf("invalid config template response")
	}
	template := config.PluginTemplate{Name: entry.Name, LegacyNames: response.LegacyNames}
	if !bytes.Equal(bytes.TrimSpace(response.Section), []byte("null")) {
		var document yaml.Node
		if err := yaml.Unmarshal(response.Section, &document); err != nil || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
			return empty, fmt.Errorf("config template section must be an object or null")
		}
		template.Section = document.Content[0]
		clearTemplateFlow(template.Section)
	}
	seen := map[string]bool{}
	for _, name := range template.LegacyNames {
		if !validName.MatchString(name) || name == entry.Name || seen[name] || strings.TrimSpace(name) != name {
			return empty, fmt.Errorf("invalid config template legacy name")
		}
		seen[name] = true
	}
	if template.Section == nil && len(template.LegacyNames) != 0 {
		return empty, fmt.Errorf("config template without section cannot migrate legacy names")
	}
	return template, nil
}

func clearTemplateFlow(node *yaml.Node) {
	node.Style &^= yaml.FlowStyle
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			node.Content[i].Style = 0
		}
	}
	for _, child := range node.Content {
		clearTemplateFlow(child)
	}
}
