package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func (s *Store) loadOrRecoverRegistry() ([]Workspace, error) {
	var registry []Workspace
	err := s.withRegistryLock(func() error {
		var err error
		registry, err = s.readOrRecoverRegistryUnlocked()
		return err
	})
	return registry, err
}

func (s *Store) readOrRecoverRegistryUnlocked() ([]Workspace, error) {
	registry, err := s.readRegistryUnlocked()
	if err == nil {
		return registry, nil
	}
	brokenRegistry := err
	registry, err = s.recoverRegistryUnlocked()
	if err != nil {
		return nil, fmt.Errorf("read workspace registry: %v; recovery failed: %w", brokenRegistry, err)
	}
	if err := s.writeRegistryUnlocked(registry); err != nil {
		return nil, fmt.Errorf("save recovered workspace registry: %w", err)
	}
	return registry, nil
}

func (s *Store) readRegistryUnlocked() ([]Workspace, error) {
	path := filepath.Join(s.DialogsDir(), "workspaces.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var registry []Workspace
	if err := json.Unmarshal(data, &registry); err != nil {
		return nil, fmt.Errorf("parse %q: %w", path, err)
	}
	seen := make(map[string]string, len(registry))
	for index, entry := range registry {
		if entry.ID == "" || entry.Dir == "" || entry.Last.IsZero() {
			return nil, fmt.Errorf("registry entry %d is incomplete", index)
		}
		if previous, exists := seen[entry.ID]; exists {
			return nil, fmt.Errorf("registry contains duplicate ID %q for %q and %q", entry.ID, previous, entry.Dir)
		}
		seen[entry.ID] = entry.Dir
	}
	return registry, nil
}

func (s *Store) writeRegistryUnlocked(registry []Workspace) error {
	sort.Slice(registry, func(i, j int) bool { return registry[i].ID < registry[j].ID })
	if registry == nil {
		registry = []Workspace{}
	}
	return s.writeJSON(filepath.Join(s.DialogsDir(), "workspaces.json"), registry)
}

type sessionMetadata struct {
	FormatVersion  int       `json:"format_version"`
	SessionID      string    `json:"session_id"`
	Workspace      string    `json:"workspace"`
	CreatedAt      time.Time `json:"created_at"`
	LastAccessedAt time.Time `json:"last_accessed_at"`
}

func (s *Store) recoverRegistryUnlocked() ([]Workspace, error) {
	entries, err := os.ReadDir(s.DialogsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return []Workspace{}, nil
	}
	if err != nil {
		return nil, err
	}
	byID := make(map[string]Workspace)
	for _, entry := range entries {
		if !entry.IsDir() || !isWorkspaceID(entry.Name()) {
			continue
		}
		files, err := os.ReadDir(filepath.Join(s.DialogsDir(), entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read workspace directory %q: %w", entry.Name(), err)
		}
		for _, file := range files {
			if file.IsDir() || filepath.Ext(file.Name()) != ".json" || strings.HasPrefix(file.Name(), ".") {
				continue
			}
			path := filepath.Join(s.DialogsDir(), entry.Name(), file.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("read session metadata %q: %w", path, err)
			}
			var metadata sessionMetadata
			if err := json.Unmarshal(data, &metadata); err != nil {
				return nil, fmt.Errorf("parse session metadata %q: %w", path, err)
			}
			if metadata.FormatVersion != FormatVersion || metadata.SessionID == "" || metadata.Workspace == "" || metadata.CreatedAt.IsZero() || metadata.LastAccessedAt.IsZero() {
				return nil, fmt.Errorf("session metadata %q is invalid", path)
			}
			id := s.hash(metadata.Workspace)
			if id != entry.Name() {
				return nil, fmt.Errorf("session %q belongs to workspace hash %q, found in %q", path, id, entry.Name())
			}
			workspace, exists := byID[id]
			if exists && workspace.Dir != metadata.Workspace {
				return nil, fmt.Errorf("workspace hash collision: %s identifies both %q and %q", id, workspace.Dir, metadata.Workspace)
			}
			if !exists || metadata.LastAccessedAt.After(workspace.Last) {
				byID[id] = Workspace{ID: id, Dir: metadata.Workspace, Last: metadata.LastAccessedAt}
			}
		}
	}
	registry := make([]Workspace, 0, len(byID))
	for _, workspace := range byID {
		registry = append(registry, workspace)
	}
	sort.Slice(registry, func(i, j int) bool { return registry[i].ID < registry[j].ID })
	return registry, nil
}

func isWorkspaceID(value string) bool {
	if len(value) != md5HexLength {
		return false
	}
	for _, char := range value {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return false
		}
	}
	return true
}

const md5HexLength = 32
