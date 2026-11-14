package manager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/ReanSn0w/horizon/internal/config"
)

const stateVersion = 1

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

type State struct {
	Version           int           `json:"version"`
	Commit            string        `json:"commit"`
	Ref               string        `json:"ref"`
	Source            config.Source `json:"source"`
	Plugins           []string      `json:"plugins"`
	ConfiguredPlugins []string      `json:"configured_plugins,omitempty"`
}

func ReadState(home string) (State, error) {
	var s State
	data, err := os.ReadFile(filepath.Join(home, ".manager", "state.json"))
	if err != nil {
		return s, err
	}
	if err = json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("invalid manager state")
	}
	if s.Version != stateVersion || !commitPattern.MatchString(s.Commit) {
		return s, fmt.Errorf("unsupported or invalid manager state")
	}
	if err = s.Source.Validate(); err != nil {
		return s, err
	}
	seen := map[string]bool{}
	for _, name := range s.Plugins {
		if !validManagedName(name) || name == "manager" || seen[name] {
			return s, fmt.Errorf("invalid installed plugin name")
		}
		seen[name] = true
	}
	seen = map[string]bool{}
	for _, name := range s.ConfiguredPlugins {
		if !validManagedName(name) || name == "manager" || seen[name] {
			return s, fmt.Errorf("invalid configured plugin name")
		}
		seen[name] = true
	}
	return s, nil
}

func configuredPlugins(groups ...[]string) []string {
	var names []string
	for _, group := range groups {
		for _, name := range group {
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	slices.Sort(names)
	return names
}
func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(data, '\n'), 0600)
}
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".manager-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
