package distribution

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

const stateDirectory = ".distribution"

type State struct {
	SchemaVersion int               `json:"schema_version"`
	Manifest      Manifest          `json:"manifest"`
	Platform      string            `json:"platform"`
	Executable    string            `json:"executable"`
	Home          string            `json:"home"`
	Installed     []string          `json:"installed"`
	Hashes        map[string]string `json:"hashes"`
}

func StatePath(home string) string { return filepath.Join(home, stateDirectory, "state.json") }
func LoadState(home string) (State, error) {
	var s State
	canonical, err := filepath.EvalSymlinks(home)
	if err != nil {
		return s, err
	}
	home, err = filepath.Abs(canonical)
	if err != nil {
		return s, err
	}
	data, err := os.ReadFile(StatePath(home))
	if err != nil {
		return s, fmt.Errorf("read managed installation (use install.sh first): %w", err)
	}
	if err = json.Unmarshal(data, &s); err != nil {
		return s, err
	}
	return s, s.Validate(home)
}
func (s State) Validate(home string) error {
	if s.SchemaVersion != 1 || s.Home != home || !filepath.IsAbs(s.Home) || !filepath.IsAbs(s.Executable) || filepath.Base(s.Executable) != "horizon" || !slices.Contains(Platforms, s.Platform) {
		return fmt.Errorf("invalid installation identity")
	}
	if err := s.Manifest.Validate(); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, id := range s.Installed {
		if seen[id] {
			return fmt.Errorf("duplicate installed plugin %s", id)
		}
		seen[id] = true
		if _, ok := s.Manifest.Catalog.Find(id); !ok {
			return fmt.Errorf("unknown managed plugin %s", id)
		}
	}
	closure, err := s.Manifest.Catalog.Closure(append(slices.Clone(s.Installed), s.Manifest.Catalog.Required()...))
	if err != nil {
		return err
	}
	for _, id := range closure {
		if !seen[id] {
			return fmt.Errorf("missing managed dependency %s", id)
		}
	}
	for _, name := range append([]string{"horizon"}, s.Binaries()...) {
		if len(s.Hashes[name]) != 64 {
			return fmt.Errorf("missing installed digest for %s", name)
		}
	}
	return nil
}
func (s State) Binaries() []string {
	var result []string
	for _, id := range s.Installed {
		result = append(result, "horizon-"+id)
	}
	return result
}
func (s State) CheckRunning(executable, version string) error {
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	if resolved != s.Executable {
		return fmt.Errorf("running Horizon %s differs from managed executable %s", resolved, s.Executable)
	}
	if !s.Healthy("horizon") {
		return fmt.Errorf("managed Horizon is missing or modified; recover with install.sh")
	}
	if version != s.Manifest.Tag {
		return fmt.Errorf("running version %s differs from managed release %s; use install.sh to recover", version, s.Manifest.Tag)
	}
	return nil
}
func (s State) Target(binary string) string {
	if binary == "horizon" {
		return s.Executable
	}
	return filepath.Join(s.Home, "plugins", binary)
}
func FileDigest(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return digest(data), nil
}
func (s State) Healthy(binary string) bool {
	info, err := os.Lstat(s.Target(binary))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0100 == 0 {
		return false
	}
	hash, err := FileDigest(s.Target(binary))
	return err == nil && hash == s.Hashes[binary]
}
