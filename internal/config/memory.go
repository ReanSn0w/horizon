package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"gopkg.in/yaml.v3"
)

func defaultRawMemory(model string) rawMemory {
	enabled := true
	return rawMemory{Enabled: &enabled, Threshold: 5, Interval: "24h", Model: model, NoteLimit: 4096, SummaryLimit: 16384, ContextLimit: 16000, APITimeout: "45s", RetryInterval: "5m", MaxPendingNotes: 256, MaxPendingBytes: 262144}
}

func validateMemory(raw rawConfig, legacyEnabled bool, models map[string]Model) (Memory, []string, error) {
	legacyNode, hasLegacySettings := raw.Plugins["memory"]
	agentPlugins := slices.DeleteFunc(slices.Clone(raw.AgentPlugins), func(name string) bool { return name == "memory" })
	if raw.Memory.Node != nil && (legacyEnabled || hasLegacySettings) {
		return Memory{}, nil, errors.New("memory conflicts with legacy agent_plugins/plugins.memory; remove one definition, then run horizon-manager install")
	}
	if raw.Memory.Node == nil && !legacyEnabled && !hasLegacySettings {
		return Memory{}, agentPlugins, nil
	}
	settings := defaultRawMemory(raw.DefaultModel)
	enabled := false
	if raw.Memory.Node != nil {
		enabledNode := mappingValue(raw.Memory.Node, "enabled")
		if raw.Memory.Node.Kind != yaml.MappingNode || enabledNode != nil && enabledNode.Tag == "!!null" {
			return Memory{}, nil, errors.New("invalid memory settings")
		}
		data, err := yaml.Marshal(raw.Memory.Node)
		if err != nil {
			return Memory{}, nil, err
		}
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		if err := decoder.Decode(&settings); err != nil {
			return Memory{}, nil, errors.New("invalid memory settings")
		}
		if settings.Enabled == nil {
			value := true
			settings.Enabled = &value
		}
		enabled = *settings.Enabled
	} else if legacyEnabled || hasLegacySettings {
		if hasLegacySettings {
			data, err := yaml.Marshal(&legacyNode)
			if err != nil {
				return Memory{}, nil, err
			}
			decoder := yaml.NewDecoder(bytes.NewReader(data))
			decoder.KnownFields(true)
			if err := decoder.Decode(&settings); err != nil {
				return Memory{}, nil, errors.New("invalid plugins.memory settings")
			}
		}
		enabled = legacyEnabled
	}
	if settings.Model == "" {
		settings.Model = raw.DefaultModel
	}
	if settings.Threshold < 1 || settings.Threshold > 256 || settings.NoteLimit < 1 || settings.NoteLimit > 16384 || settings.SummaryLimit < 1 || settings.SummaryLimit > 32768 || settings.ContextLimit < 256 || settings.ContextLimit > 32000 || settings.MaxPendingNotes < 1 || settings.MaxPendingNotes > 1024 || settings.MaxPendingBytes < settings.NoteLimit || settings.MaxPendingBytes > 1<<20 {
		return Memory{}, nil, errors.New("memory limits outside supported bounds")
	}
	if _, ok := models[settings.Model]; !ok {
		return Memory{}, nil, fmt.Errorf("memory.model %q does not name a configured profile", settings.Model)
	}
	interval, err := time.ParseDuration(settings.Interval)
	if err != nil || interval <= 0 {
		return Memory{}, nil, errors.New("memory.interval must be positive")
	}
	apiTimeout, err := time.ParseDuration(settings.APITimeout)
	if err != nil || apiTimeout <= 0 || apiTimeout > 45*time.Second {
		return Memory{}, nil, errors.New("memory.api_timeout must be positive and at most 45s")
	}
	retryInterval, err := time.ParseDuration(settings.RetryInterval)
	if err != nil || retryInterval <= 0 {
		return Memory{}, nil, errors.New("memory.retry_interval must be positive")
	}
	return Memory{Enabled: enabled, Threshold: settings.Threshold, Interval: interval, Model: settings.Model, NoteLimit: settings.NoteLimit, SummaryLimit: settings.SummaryLimit, ContextLimit: settings.ContextLimit, APITimeout: apiTimeout, RetryInterval: retryInterval, MaxPendingNotes: settings.MaxPendingNotes, MaxPendingBytes: settings.MaxPendingBytes}, agentPlugins, nil
}

// MigrateMemory moves the legacy plugin settings into the built-in section.
func MigrateMemory(home string) (bool, error) {
	// Existing init intentionally preserves arbitrary user config files. Avoid
	// forcing a strict parse unless this file actually contains legacy memory.
	data, err := os.ReadFile(filepath.Join(home, "config.yaml"))
	if err != nil {
		return false, err
	}
	var preview yaml.Node
	if yaml.Unmarshal(data, &preview) != nil || len(preview.Content) != 1 || preview.Content[0].Kind != yaml.MappingNode {
		return false, nil
	}
	previewRoot := preview.Content[0]
	previewPlugins := mappingValue(previewRoot, "plugins")
	if mappingValue(previewPlugins, "memory") == nil && !sequenceContains(mappingValue(previewRoot, "agent_plugins"), "memory") {
		return false, nil
	}
	return UpdateDocument(home, func(document *yaml.Node) (bool, error) {
		root := document.Content[0]
		if mappingValue(root, "memory") != nil {
			plugins := mappingValue(root, "plugins")
			if plugins != nil && mappingValue(plugins, "memory") != nil || sequenceContains(mappingValue(root, "agent_plugins"), "memory") {
				return false, errors.New("memory conflicts with legacy agent_plugins/plugins.memory; remove one definition before migration")
			}
			return false, nil
		}
		plugins := mappingValue(root, "plugins")
		legacy := mappingValue(plugins, "memory")
		agents := mappingValue(root, "agent_plugins")
		enabled := sequenceContains(agents, "memory")
		if legacy == nil && !enabled {
			return false, nil
		}
		section := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		appendMapping(section, "enabled", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprint(enabled)})
		if legacy != nil {
			section.Content = append(section.Content, legacy.Content...)
			removeMapping(plugins, "memory")
		}
		appendMapping(root, "memory", section)
		removeSequenceValue(agents, "memory")
		return true, nil
	})
}

func removeMapping(node *yaml.Node, key string) {
	if node == nil || node.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content = append(node.Content[:i], node.Content[i+2:]...)
			return
		}
	}
}

func sequenceContains(node *yaml.Node, value string) bool {
	if node == nil || node.Kind != yaml.SequenceNode {
		return false
	}
	return slices.ContainsFunc(node.Content, func(item *yaml.Node) bool { return item.Value == value })
}

func removeSequenceValue(node *yaml.Node, value string) {
	if node == nil || node.Kind != yaml.SequenceNode {
		return
	}
	node.Content = slices.DeleteFunc(node.Content, func(item *yaml.Node) bool { return item.Value == value })
}
