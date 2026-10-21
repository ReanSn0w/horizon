// Package catalog embeds the official plugins shipped from this repository.
package catalog

import (
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"regexp"
	"slices"

	"gopkg.in/yaml.v3"
)

//go:embed catalog.yaml
var source []byte

type Plugin struct {
	ID          string   `yaml:"id" json:"id"`
	Description string   `yaml:"description" json:"description"`
	Source      string   `yaml:"source" json:"-"`
	Binary      string   `yaml:"binary" json:"binary"`
	Requires    []string `yaml:"requires" json:"requires"`
	Required    bool     `yaml:"required,omitempty" json:"required,omitempty"`
}
type Catalog struct {
	SchemaVersion int      `yaml:"schema_version" json:"schema_version"`
	Plugins       []Plugin `yaml:"plugins" json:"plugins"`
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Reserved names mirror the built-in commands; cli tests check the boundary.
var Reserved = []string{"init", "resume", "models", "sessions", "skills"}

func Embedded() Catalog {
	c, err := Parse(source)
	if err != nil {
		panic(err)
	}
	return c
}
func Parse(data []byte) (Catalog, error) {
	var c Catalog
	d := yaml.NewDecoder(bytes.NewReader(data))
	d.KnownFields(true)
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return c, fmt.Errorf("catalog must contain one document")
	}
	return c, c.Validate()
}
func (c Catalog) Validate() error {
	if c.SchemaVersion != 1 {
		return fmt.Errorf("unsupported catalog schema %d", c.SchemaVersion)
	}
	seen := map[string]bool{}
	for _, p := range c.Plugins {
		if !namePattern.MatchString(p.ID) || slices.Contains(Reserved, p.ID) || seen[p.ID] {
			return fmt.Errorf("invalid or duplicate plugin %q", p.ID)
		}
		if p.Binary != "horizon-"+p.ID || p.Description == "" {
			return fmt.Errorf("invalid plugin %q", p.ID)
		}
		seen[p.ID] = true
	}
	for _, p := range c.Plugins {
		deps := map[string]bool{}
		for _, dep := range p.Requires {
			if !seen[dep] || deps[dep] {
				return fmt.Errorf("invalid dependency %q of %s", dep, p.ID)
			}
			deps[dep] = true
		}
	}
	_, err := c.Closure(c.IDs())
	return err
}
func (c Catalog) IDs() []string {
	var ids []string
	for _, p := range c.Plugins {
		ids = append(ids, p.ID)
	}
	return ids
}
func (c Catalog) Find(id string) (Plugin, bool) {
	for _, p := range c.Plugins {
		if p.ID == id {
			return p, true
		}
	}
	return Plugin{}, false
}
func (c Catalog) Required() []string {
	var ids []string
	for _, p := range c.Plugins {
		if p.Required {
			ids = append(ids, p.ID)
		}
	}
	return ids
}

// Closure returns dependencies before their consumers, with each ID once.
func (c Catalog) Closure(ids []string) ([]string, error) {
	state := map[string]int{}
	result := []string{}
	var visit func(string) error
	visit = func(id string) error {
		if state[id] == 1 {
			return fmt.Errorf("dependency cycle at %s", id)
		}
		if state[id] == 2 {
			return nil
		}
		p, ok := c.Find(id)
		if !ok {
			return fmt.Errorf("unknown plugin %q", id)
		}
		state[id] = 1
		for _, dep := range p.Requires {
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[id] = 2
		result = append(result, id)
		return nil
	}
	for _, id := range ids {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	return result, nil
}
func Description(id string) string {
	p, ok := Embedded().Find(id)
	if !ok {
		panic("unknown official plugin: " + id)
	}
	return p.Description
}
