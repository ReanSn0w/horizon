package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// PluginTemplate is the validated, plugin-owned configuration section returned
// during initialization. A nil Section means that the plugin needs no section.
type PluginTemplate struct {
	Name        string
	Section     *yaml.Node
	LegacyNames []string
}

// MergePluginTemplates adds missing plugin settings in one atomic config update.
// Existing scalar values, including null and empty strings, remain untouched.
func MergePluginTemplates(home string, templates []PluginTemplate) (map[string]string, error) {
	status := make(map[string]string, len(templates))
	_, err := UpdateDocument(home, func(document *yaml.Node) (bool, error) {
		root := document.Content[0]
		plugins := mappingValue(root, "plugins")
		if plugins != nil && plugins.Kind != yaml.MappingNode {
			return false, fmt.Errorf("plugins must be a mapping")
		}
		changed := false
		for _, template := range templates {
			if template.Section == nil {
				status[template.Name] = "no_section"
				continue
			}
			if template.Section.Kind != yaml.MappingNode {
				return false, fmt.Errorf("plugins.%s template must be a mapping", template.Name)
			}
			if plugins == nil {
				plugins = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				appendMapping(root, "plugins", plugins)
				changed = true
			}
			section := mappingValue(plugins, template.Name)
			var legacyKey *yaml.Node
			var legacySection *yaml.Node
			for _, name := range template.LegacyNames {
				for i := 0; i+1 < len(plugins.Content); i += 2 {
					if plugins.Content[i].Value == name {
						if section != nil || legacySection != nil {
							return false, fmt.Errorf("plugins.%s conflicts with plugins.%s", template.Name, name)
						}
						legacyKey, legacySection = plugins.Content[i], plugins.Content[i+1]
					}
				}
			}
			if legacySection != nil {
				if legacySection.Kind != yaml.MappingNode {
					return false, fmt.Errorf("plugins.%s must be a mapping", legacyKey.Value)
				}
				legacyKey.Value = template.Name
				section = legacySection
				changed = true
			}
			if section == nil {
				appendMapping(plugins, template.Name, template.Section)
				status[template.Name] = "added"
				changed = true
				continue
			}
			if section.Kind != yaml.MappingNode {
				return false, fmt.Errorf("plugins.%s must be a mapping", template.Name)
			}
			if mergeMissingMapping(section, template.Section) {
				changed = true
				status[template.Name] = "updated"
			} else if legacySection != nil {
				status[template.Name] = "updated"
			} else {
				status[template.Name] = "existing"
			}
		}
		return changed, nil
	})
	if err != nil {
		return nil, err
	}
	return status, nil
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func appendMapping(node *yaml.Node, key string, value *yaml.Node) {
	if len(node.Content) == 0 {
		node.Style &^= yaml.FlowStyle
	}
	node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

func mergeMissingMapping(target, defaults *yaml.Node) bool {
	changed := false
	for i := 0; i+1 < len(defaults.Content); i += 2 {
		key := defaults.Content[i].Value
		value := mappingValue(target, key)
		if value == nil {
			appendMapping(target, key, defaults.Content[i+1])
			changed = true
		} else if value.Kind == yaml.MappingNode && defaults.Content[i+1].Kind == yaml.MappingNode {
			if mergeMissingMapping(value, defaults.Content[i+1]) {
				changed = true
			}
		}
	}
	return changed
}
