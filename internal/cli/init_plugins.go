package cli

import (
	"sort"

	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/plugins"
)

func initializePluginConfig(home string, reserved []string) (map[string]string, []string, error) {
	entries, err := plugins.Candidates(home, reserved)
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	templates := make([]config.PluginTemplate, 0, len(entries))
	var skipped []string
	for _, candidate := range entries {
		if candidate.Err != nil {
			skipped = append(skipped, candidate.Name)
			continue
		}
		entry := plugins.Inspect(home, candidate.Name, reserved)
		if entry.Err != nil || entry.Metadata.ConfigProtocolVersion != plugins.ConfigProtocolVersion {
			skipped = append(skipped, candidate.Name)
			continue
		}
		template, err := plugins.ReadConfigTemplate(entry, home)
		if err != nil {
			skipped = append(skipped, candidate.Name)
			continue
		}
		templates = append(templates, template)
	}
	if len(templates) == 0 {
		return nil, skipped, nil
	}
	statuses, err := config.MergePluginTemplates(home, templates)
	return statuses, skipped, err
}

func sortedKeys[V any](values map[string]V) []string {
	result := make([]string, 0, len(values))
	for name := range values {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}
