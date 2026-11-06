package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type disabledIDs []string

func validSkillID(id string) bool {
	return id != "" && id != "." && id != ".." && !filepath.IsAbs(id) && !strings.ContainsAny(id, "/\\\x00")
}

func (ids *disabledIDs) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.SequenceNode {
		return errors.New("disabled_skills must be a list of skill IDs")
	}
	seen := map[string]bool{}
	for _, item := range node.Content {
		if item.Kind != yaml.ScalarNode || item.Tag != "!!str" || !validSkillID(item.Value) {
			return errors.New("disabled_skills entries must be valid string directory IDs")
		}
		if !seen[item.Value] {
			*ids = append(*ids, item.Value)
			seen[item.Value] = true
		}
	}
	return nil
}

// readConfig validates the document's structure without requiring a ready provider.
func readConfig(path string) (rawConfig, *yaml.Node, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return rawConfig{}, nil, err
	}
	var raw rawConfig
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil {
		return raw, nil, fmt.Errorf("parse configuration %q: %w", path, err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return raw, nil, fmt.Errorf("configuration %q must contain exactly one YAML document", path)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return raw, nil, fmt.Errorf("parse configuration %q: %w", path, err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return raw, nil, fmt.Errorf("configuration %q must be a YAML mapping", path)
	}
	// yaml.v3 does not call UnmarshalYAML for null values.
	root := document.Content[0]
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == "disabled_skills" && root.Content[i+1].Tag == "!!null" {
			return raw, nil, errors.New("disabled_skills must be a list of skill IDs")
		}
	}
	return raw, &document, nil
}

func LoadDisabledSkills(home string) ([]string, error) {
	raw, _, err := readConfig(filepath.Join(home, "config.yaml"))
	return append([]string(nil), raw.DisabledSkills...), err
}
