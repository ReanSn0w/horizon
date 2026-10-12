package config

import (
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// MergeProviderAttributionFields adds only the new optional fields to an
// existing provider mapping. Invalid or unrelated personal config files stay
// untouched, as they did before init learned about these fields.
func MergeProviderAttributionFields(home string) (bool, error) {
	path := filepath.Join(home, "config.yaml")
	_, document, err := readConfig(path)
	if err != nil || mappingValue(document.Content[0], "provider") == nil {
		return false, nil
	}
	return UpdateDocument(home, func(document *yaml.Node) (bool, error) {
		provider := mappingValue(document.Content[0], "provider")
		if provider == nil || provider.Kind != yaml.MappingNode {
			return false, nil
		}
		changed := false
		for _, key := range []string{"app_url", "app_title"} {
			if mappingValue(provider, key) == nil {
				appendMapping(provider, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "", Style: yaml.DoubleQuotedStyle})
				changed = true
			}
		}
		return changed, nil
	})
}
