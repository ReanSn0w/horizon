package config

import (
	"errors"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"strings"
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
		var node yaml.Node
		if err := node.Encode(value); err != nil {
			return false, err
		}
		root := doc.Content[0]
		if old := mappingValue(root, "installation"); old != nil {
			node.HeadComment = old.HeadComment
			node.LineComment = old.LineComment
			*old = node
		} else {
			appendMapping(root, "installation", &node)
		}
		return true, nil
	})
	return err
}
