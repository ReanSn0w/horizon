package manager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Plugin struct {
	Kind        string `json:"kind,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Package     string `json:"package"`
	Binary      string `json:"binary"`
}

var reserved = map[string]bool{"resume": true, "sessions": true, "models": true, "skills": true, "memory": true, "decision": true, "version": true, "help": true, "init": true}

func Catalog(source string) (map[string]Plugin, error) {
	paths, err := filepath.Glob(filepath.Join(source, "plugins", "*", "plugin.json"))
	if err != nil {
		return nil, err
	}
	result := map[string]Plugin{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var p Plugin
		if err = json.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("invalid plugin description %s", filepath.Base(filepath.Dir(path)))
		}
		expected := "./plugins/" + filepath.Base(filepath.Dir(path))
		if !namePattern.MatchString(p.Name) || (reserved[p.Name] || p.Name == "agent" && p.Kind != "adapter") || p.Description == "" || p.Package != expected || (p.Kind != "" && p.Kind != "cli" && p.Kind != "adapter") || p.Binary != managedBinary(pluginID(p)) {
			return nil, fmt.Errorf("invalid plugin description in %s", expected)
		}
		if _, ok := result[pluginID(p)]; ok {
			return nil, fmt.Errorf("duplicate plugin %s", p.Name)
		}
		resolved, err := filepath.EvalSymlinks(filepath.Dir(path))
		if err != nil {
			return nil, err
		}
		root, err := filepath.EvalSymlinks(source)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(root, resolved)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("plugin escapes source directory")
		}
		result[pluginID(p)] = p
	}
	if p, ok := result["manager"]; !ok || p.Package != "./plugins/manager" {
		return nil, fmt.Errorf("source revision does not support horizon-manager")
	}
	return result, nil
}

func pluginID(p Plugin) string {
	if p.Kind == "adapter" {
		return "agent:" + p.Name
	}
	return p.Name
}
