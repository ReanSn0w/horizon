package config

import (
	"bytes"
	"errors"
	"io"

	"gopkg.in/yaml.v3"
)

// MergeCoreTemplate adds missing core settings without replacing explicit values.
// Named model/provider maps and plugin-owned settings are never filled from the
// example entries. The manager supplies the template from the selected revision.
func MergeCoreTemplate(home string, data []byte) (bool, error) {
	var template yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&template); err != nil || len(template.Content) != 1 || template.Content[0].Kind != yaml.MappingNode {
		return false, errors.New("core configuration template must be a YAML mapping")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return false, errors.New("core configuration template must contain one document")
	}
	if _, err := decodeDocument(&template); err != nil {
		return false, errors.New("core configuration template is incompatible with this manager; use the manager from the selected revision")
	}
	return UpdateDocument(home, func(doc *yaml.Node) (bool, error) {
		root, defaults := doc.Content[0], template.Content[0]
		changed := false
		if mappingValue(root, "extensions") != nil && mappingValue(defaults, "extensions") == nil {
			return false, errors.New("selected revision does not support extensions; migrate configuration before downgrading")
		}
		// mode is an explicitly retired core key, not inferred from plugin names.
		if old := mappingValue(root, "mode"); old != nil && mappingValue(defaults, "mode") == nil {
			if old.Kind != yaml.ScalarNode || old.Value != "unit" {
				return false, errors.New("cannot migrate unsupported legacy mode; resolve it before updating")
			}
			for i := 0; i < len(root.Content); i += 2 {
				if root.Content[i].Value == "mode" {
					comment := root.Content[i].HeadComment
					for _, part := range []string{root.Content[i].LineComment, old.HeadComment, old.LineComment, old.FootComment} {
						if part != "" {
							if comment != "" {
								comment += "\n"
							}
							comment += part
						}
					}
					root.Content = append(root.Content[:i], root.Content[i+2:]...)
					if comment != "" {
						if root.HeadComment != "" {
							root.HeadComment += "\n"
						}
						root.HeadComment += comment
					}
					break
				}
			}
			changed = true
		} else if old == nil && mappingValue(defaults, "mode") != nil {
			for i := 0; i < len(defaults.Content); i += 2 {
				if defaults.Content[i].Value == "mode" {
					root.Content = append(root.Content, defaults.Content[i], defaults.Content[i+1])
					break
				}
			}
			changed = true
		}
		// These maps are user-defined, not schemas: example entries are not defaults.
		// They are supplied wholesale only by first-install initialization.
		for i := 0; i < len(defaults.Content); i += 2 {
			key, value := defaults.Content[i], defaults.Content[i+1]
			switch key.Value {
			case "installation", "models", "providers", "plugins", "extensions", "mode":
				continue
			case "default_model", "decision_model":
				if mappingValue(root, key.Value) == nil && mappingValue(mappingValue(root, "models"), value.Value) == nil {
					continue
				}
			}
			existing := mappingValue(root, key.Value)
			if key.Value == "memory" && value.Kind == yaml.MappingNode {
				// No memory section used to mean disabled. Adding defaults must not enable it.
				if existing == nil {
					if enabled := mappingValue(value, "enabled"); enabled != nil {
						enabled.Value = "false"
						enabled.Tag = "!!bool"
					} else {
						appendMapping(value, "enabled", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false"})
					}
				}
				// Missing memory.model follows the user's default profile, not the sample.
				if model := mappingValue(value, "model"); model != nil {
					model.Value = ""
					if selected := mappingValue(root, "default_model"); selected != nil {
						model.Value = selected.Value
					}
				}
			}
			if existing == nil {
				root.Content = append(root.Content, key, value)
				changed = true
			} else if existing.Kind == yaml.MappingNode && value.Kind == yaml.MappingNode {
				changed = mergeCoreMapping(existing, value) || changed
			}
		}
		return changed, nil
	})
}

func mergeCoreMapping(target, defaults *yaml.Node) bool {
	changed := false
	for i := 0; i < len(defaults.Content); i += 2 {
		key, value := defaults.Content[i], defaults.Content[i+1]
		existing := mappingValue(target, key.Value)
		if existing == nil {
			target.Content = append(target.Content, key, value)
			changed = true
		} else if existing.Kind == yaml.MappingNode && value.Kind == yaml.MappingNode {
			changed = mergeCoreMapping(existing, value) || changed
		}
	}
	return changed
}
