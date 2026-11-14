package config

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"

	"gopkg.in/yaml.v3"
)

// ExtensionSettings controls model-facing tools, independently of supervisor inputs.
type ExtensionSettings struct {
	Enabled       bool `yaml:"enabled"`
	Communication bool `yaml:"communication,omitempty"`
}

type ExtensionMap map[string]ExtensionSettings

func (m *ExtensionMap) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("extensions must be a mapping")
	}
	result := ExtensionMap{}
	for i := 0; i < len(n.Content); i += 2 {
		name := n.Content[i].Value
		if _, exists := result[name]; exists {
			return fmt.Errorf("duplicate extension %q", name)
		}
		var settings ExtensionSettings
		if err := settings.UnmarshalYAML(n.Content[i+1]); err != nil {
			return fmt.Errorf("extensions.%s: %w", name, err)
		}
		result[name] = settings
	}
	*m = result
	return nil
}

var extensionName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func (s *ExtensionSettings) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("extension settings must be a mapping")
	}
	seen := map[string]bool{}
	for i := 0; i < len(n.Content); i += 2 {
		key, value := n.Content[i].Value, n.Content[i+1]
		if seen[key] || (key != "enabled" && key != "communication") || value.Tag != "!!bool" {
			return fmt.Errorf("extension settings require unique boolean enabled/communication fields")
		}
		seen[key] = true
		var v bool
		if err := value.Decode(&v); err != nil {
			return err
		}
		if key == "enabled" {
			s.Enabled = v
		} else {
			s.Communication = v
		}
	}
	return nil
}

func extensionLists(settings map[string]ExtensionSettings) ([]string, []string, error) {
	var enabled, communication []string
	for name, s := range settings {
		if !extensionName.MatchString(name) || name == "memory" {
			return nil, nil, fmt.Errorf("invalid extension name %q (memory is built in)", name)
		}
		if s.Enabled {
			enabled = append(enabled, name)
			if s.Communication {
				communication = append(communication, name)
			}
		}
	}
	if len(enabled) > 16 {
		return nil, nil, fmt.Errorf("extensions supports at most 16 enabled extensions")
	}
	sort.Strings(enabled)
	sort.Strings(communication)
	return enabled, communication, nil
}

// MigrateExtensions preserves legacy grants; it never enables omitted plugins.
// Legacy memory is moved into its built-in section before migrating extension names.
func MigrateExtensions(home string) (bool, error) {
	return UpdateDocument(home, MigrateExtensionsDocument)
}

// MigrateExtensionsDocument migrates a staged document without acquiring another lock.
func MigrateExtensionsDocument(doc *yaml.Node) (bool, error) {
	memoryChanged, err := migrateMemoryDocument(doc)
	if err != nil {
		return false, err
	}
	root := doc.Content[0]
	old := mappingValue(root, "agent_plugins")
	grants := mappingValue(root, "agent_communication")
	if old == nil && grants == nil {
		return memoryChanged, nil
	}
	raw, err := decodeDocument(doc)
	if err != nil {
		return false, err
	}
	if len(raw.Extensions) > 0 && (len(raw.AgentPlugins) > 0 || len(raw.AgentCommunication) > 0) {
		return false, fmt.Errorf("extensions conflicts with legacy agent_plugins/agent_communication")
	}
	settings := map[string]ExtensionSettings{}
	for _, name := range raw.AgentPlugins {
		if _, ok := settings[name]; ok {
			return false, fmt.Errorf("duplicate legacy extension %q", name)
		}
		settings[name] = ExtensionSettings{Enabled: true}
	}
	for _, name := range raw.AgentCommunication {
		s, ok := settings[name]
		if !ok || s.Communication {
			return false, fmt.Errorf("invalid legacy agent_communication entry %q", name)
		}
		s.Communication = true
		settings[name] = s
	}
	if _, _, err := extensionLists(settings); err != nil {
		return false, err
	}
	target := mappingValue(root, "extensions")
	if target == nil {
		target = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		appendMapping(root, "extensions", target)
	}
	if target.Kind != yaml.MappingNode {
		return false, fmt.Errorf("extensions must be a mapping")
	}
	names := make([]string, 0, len(settings))
	for name := range settings {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		var n yaml.Node
		if err := n.Encode(settings[name]); err != nil {
			return false, err
		}
		appendMapping(target, name, &n)
	}
	for i := 0; i < len(root.Content); {
		if root.Content[i].Value != "agent_plugins" && root.Content[i].Value != "agent_communication" {
			i += 2
			continue
		}
		// Keep user comments from the retired keys and sequence entries.
		preserveExtensionComments(target, root.Content[i])
		preserveExtensionComments(target, root.Content[i+1])
		root.Content = append(root.Content[:i], root.Content[i+2:]...)
	}
	return true, nil
}

func preserveExtensionComments(target, n *yaml.Node) {
	for _, comment := range []string{n.HeadComment, n.LineComment, n.FootComment} {
		if comment != "" {
			if target.HeadComment != "" {
				target.HeadComment += "\n"
			}
			target.HeadComment += comment
		}
	}
	for _, child := range n.Content {
		preserveExtensionComments(target, child)
	}
}

// EnsureExtension adds a record only if absent, preserving explicit disabled states.
func EnsureExtension(home, name string, defaults ExtensionSettings) error {
	if _, _, err := extensionLists(map[string]ExtensionSettings{name: defaults}); err != nil {
		return err
	}
	_, err := UpdateDocument(home, func(doc *yaml.Node) (bool, error) {
		root := doc.Content[0]
		extensions := mappingValue(root, "extensions")
		if extensions == nil {
			extensions = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			appendMapping(root, "extensions", extensions)
		}
		if extensions.Kind != yaml.MappingNode {
			return false, fmt.Errorf("extensions must be a mapping")
		}
		if mappingValue(extensions, name) != nil {
			return false, nil
		}
		var n yaml.Node
		if err := n.Encode(defaults); err != nil {
			return false, err
		}
		appendMapping(extensions, name, &n)
		raw, err := decodeDocument(doc)
		if err != nil {
			return false, err
		}
		if _, _, err = extensionLists(raw.Extensions); err != nil {
			return false, err
		}
		return true, nil
	})
	return err
}

func DisableExtension(home, name string) error {
	_, err := UpdateDocument(home, func(doc *yaml.Node) (bool, error) {
		extension := mappingValue(mappingValue(doc.Content[0], "extensions"), name)
		if extension == nil {
			return false, nil
		}
		enabled := mappingValue(extension, "enabled")
		if enabled == nil {
			return false, nil
		}
		if enabled.Tag == "!!bool" && enabled.Value == "false" {
			return false, nil
		}
		enabled.Tag = "!!bool"
		enabled.Value = strconv.FormatBool(false)
		return true, nil
	})
	return err
}

// AddAdapter enables a newly installed input without changing existing entries.
func AddAdapter(home, name string, agentSupervisor bool) error {
	_, err := UpdateDocument(home, func(doc *yaml.Node) (bool, error) {
		n := doc.Content[0]
		supervisor := "agent"
		if !agentSupervisor {
			supervisor = "daemon"
		}
		for _, key := range []string{"plugins", supervisor} {
			next := mappingValue(n, key)
			if next == nil {
				next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				appendMapping(n, key, next)
			}
			if next.Kind != yaml.MappingNode {
				return false, fmt.Errorf("%s must be a mapping", key)
			}
			n = next
		}
		adapters := mappingValue(n, "adapters")
		if adapters == nil {
			adapters = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			appendMapping(n, "adapters", adapters)
		}
		if adapters.Kind != yaml.SequenceNode {
			return false, fmt.Errorf("plugins.%s.adapters must be a sequence", supervisor)
		}
		if slices.ContainsFunc(adapters.Content, func(n *yaml.Node) bool { return n.Value == name }) {
			return false, nil
		}
		adapters.Content = append(adapters.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name})
		return true, nil
	})
	return err
}
