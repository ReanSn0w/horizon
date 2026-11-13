package manager

import (
	"fmt"

	"github.com/ReanSn0w/horizon/internal/config"
	"gopkg.in/yaml.v3"
)

// Called only on the staged configuration before transactional deployment.
func prepareSupervisorConfig(home string, agentSupervisor bool) error {
	_, err := config.UpdateDocument(home, func(doc *yaml.Node) (bool, error) {
		root := doc.Content[0]
		if !agentSupervisor {
			return false, nil
		}
		changed := false
		for i := 0; i < len(root.Content); i += 2 {
			key, value := root.Content[i], root.Content[i+1]
			if key.Value != "plugins" || value.Kind != yaml.MappingNode {
				continue
			}
			legacy, current := -1, -1
			for j := 0; j < len(value.Content); j += 2 {
				switch value.Content[j].Value {
				case "daemon":
					legacy = j
				case "agent":
					current = j
				}
			}
			if legacy >= 0 {
				if current >= 0 {
					return false, fmt.Errorf("both plugins.agent and plugins.daemon exist; resolve the conflict before updating")
				}
				value.Content[legacy].Value = "agent"
				changed = true
			}
		}
		return changed, nil
	})
	return err
}
