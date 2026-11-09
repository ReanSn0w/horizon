package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Installation struct {
	Source Source `yaml:"source" json:"source"`
}
type Source struct {
	Type   string `yaml:"type" json:"type"`
	Branch string `yaml:"branch,omitempty" json:"branch,omitempty"`
}

func (s Source) Validate() error {
	switch s.Type {
	case "release":
		if s.Branch != "" {
			return errors.New("installation.source.branch is only valid for branch sources")
		}
	case "branch":
		if strings.TrimSpace(s.Branch) == "" || strings.HasPrefix(s.Branch, "-") || strings.ContainsAny(s.Branch, "\x00\r\n") {
			return errors.New("installation.source.branch must name a branch")
		}
	default:
		return errors.New("installation.source.type must be release or branch")
	}
	return nil
}
func ReadInstallation(home string) (Installation, error) {
	raw, _, err := readConfig(filepath.Join(home, "config.yaml"))
	if os.IsNotExist(err) {
		return Installation{Source: Source{Type: "release"}}, nil
	}
	if err != nil {
		return Installation{}, err
	}
	if raw.Installation == nil {
		return Installation{Source: Source{Type: "release"}}, nil
	}
	return *raw.Installation, raw.Installation.Source.Validate()
}
func WriteInstallation(home string, value Installation) error {
	if err := value.Source.Validate(); err != nil {
		return err
	}
	_, err := UpdateDocument(home, func(doc *yaml.Node) (bool, error) {
		root := doc.Content[0]
		old := mappingValue(root, "installation")
		if old == nil {
			var node yaml.Node
			if err := node.Encode(value); err != nil {
				return false, err
			}
			appendMapping(root, "installation", &node)
			return true, nil
		}
		var previous Installation
		if err := old.Decode(&previous); err != nil {
			return false, err
		}
		if previous == value {
			return false, nil
		}
		source := mappingValue(old, "source")
		if source == nil || source.Kind != yaml.MappingNode {
			return false, errors.New("installation.source must be a mapping")
		}
		set := func(key, text string) {
			if node := mappingValue(source, key); node != nil {
				node.Value = text
				node.Tag = "!!str"
				node.Kind = yaml.ScalarNode
			} else {
				appendMapping(source, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: text})
			}
		}
		set("type", value.Source.Type)
		if value.Source.Type == "branch" {
			set("branch", value.Source.Branch)
		} else {
			for i := 0; i+1 < len(source.Content); i += 2 {
				if source.Content[i].Value == "branch" {
					source.Content = append(source.Content[:i], source.Content[i+2:]...)
					break
				}
			}
		}
		return true, nil
	})
	return err
}
