package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/daemon"
	"github.com/ReanSn0w/horizon/internal/plugins"
	"gopkg.in/yaml.v3"
)

func validManagedName(name string) bool {
	return namePattern.MatchString(name) || strings.HasPrefix(name, "agent:") && namePattern.MatchString(strings.TrimPrefix(name, "agent:"))
}
func managedBinary(name string) string {
	if strings.HasPrefix(name, "agent:") {
		return "agent-" + strings.TrimPrefix(name, "agent:")
	}
	return "horizon-" + name
}
func inspectManaged(home, name string) error {
	if strings.HasPrefix(name, "agent:") {
		_, e := daemon.AdapterInspect(context.Background(), home, strings.TrimPrefix(name, "agent:"))
		return e
	}
	return plugins.Inspect(home, name, nil).Err
}
func adapterTemplate(stage, home, name string) (config.PluginTemplate, error) {
	short := strings.TrimPrefix(name, "agent:")
	empty := config.PluginTemplate{Name: "agent-" + short}
	m, e := daemon.AdapterInspect(context.Background(), stage, short)
	if e != nil {
		return empty, e
	}
	if m.ConfigProtocolVersion == 0 {
		return empty, nil
	}
	p, e := daemon.AdapterPath(stage, short)
	if e != nil {
		return empty, e
	}
	b, e := daemon.AdapterCommand(context.Background(), p, home, "agent-plugin-config-template")
	if e != nil {
		return empty, e
	}
	var raw struct {
		Version int             `json:"config_protocol_version"`
		Section json.RawMessage `json:"section"`
	}
	if plugins.Decode(b, &raw) != nil || raw.Version != 1 || len(raw.Section) == 0 {
		return empty, fmt.Errorf("invalid adapter config template")
	}
	if string(raw.Section) == "null" {
		return empty, nil
	}
	var doc yaml.Node
	if yaml.Unmarshal(raw.Section, &doc) != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return empty, fmt.Errorf("adapter section must be an object")
	}
	empty.Section = doc.Content[0]
	return empty, nil
}
func disableAdapter(home, name string) error {
	short := strings.TrimPrefix(name, "agent:")
	_, err := config.UpdateDocument(home, func(doc *yaml.Node) (bool, error) {
		n := doc.Content[0]
		for _, key := range []string{"plugins", "daemon", "adapters"} {
			var next *yaml.Node
			for i := 0; i+1 < len(n.Content); i += 2 {
				if n.Content[i].Value == key {
					next = n.Content[i+1]
					break
				}
			}
			if next == nil {
				return false, nil
			}
			n = next
		}
		if n.Kind != yaml.SequenceNode {
			return false, fmt.Errorf("plugins.daemon.adapters must be a sequence")
		}
		kept := n.Content[:0]
		changed := false
		for _, item := range n.Content {
			if item.Value == short {
				changed = true
			} else {
				kept = append(kept, item)
			}
		}
		n.Content = kept
		return changed, nil
	})
	return err
}
