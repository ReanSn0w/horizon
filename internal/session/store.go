package session

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Store struct {
	Home      string
	now       func() time.Time
	hash      func(string) string
	writeJSON func(string, any) error
}

type Workspace struct {
	ID   string    `json:"id"`
	Dir  string    `json:"dir"`
	Last time.Time `json:"last"`
}

func NewStore(home string) *Store {
	return &Store{Home: home, now: time.Now, hash: WorkspaceID, writeJSON: writeJSONAtomic}
}

func NormalizeWorkspace(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("make workspace path absolute: %w", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve workspace path %q: %w", abs, err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("inspect workspace path %q: %w", real, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace path %q is not a directory", real)
	}
	return filepath.Clean(real), nil
}

func WorkspaceID(normalizedPath string) string {
	sum := md5.Sum([]byte(normalizedPath))
	return hex.EncodeToString(sum[:])
}

func (s *Store) DialogsDir() string { return filepath.Join(s.Home, "dialogs") }

func (s *Store) WorkspaceDir(id string) string { return filepath.Join(s.DialogsDir(), id) }

func (s *Store) ArtifactsDir(workspace Workspace, id string) string {
	return filepath.Join(s.WorkspaceDir(workspace.ID), id+".artifacts")
}

func (s *Store) ResolveWorkspace(path string) (Workspace, error) {
	dir, err := NormalizeWorkspace(path)
	if err != nil {
		return Workspace{}, err
	}
	workspace := Workspace{ID: s.hash(dir), Dir: dir}
	registry, err := s.loadOrRecoverRegistry()
	if err != nil {
		return Workspace{}, err
	}
	for _, saved := range registry {
		if saved.ID == workspace.ID && saved.Dir != workspace.Dir {
			return Workspace{}, fmt.Errorf("workspace hash collision: %s identifies both %q and %q", workspace.ID, saved.Dir, workspace.Dir)
		}
	}
	return workspace, nil
}

// TouchWorkspace records accepted activity. The timestamp is strictly newer
// than the previous value even when the system clock has not advanced.
func (s *Store) TouchWorkspace(workspace Workspace) (Workspace, error) {
	err := s.withRegistryLock(func() error {
		registry, err := s.readOrRecoverRegistryUnlocked()
		if err != nil {
			return err
		}
		now := s.now().UTC()
		found := false
		for index := range registry {
			entry := &registry[index]
			if entry.ID != workspace.ID {
				continue
			}
			if entry.Dir != workspace.Dir {
				return fmt.Errorf("workspace hash collision: %s identifies both %q and %q", workspace.ID, entry.Dir, workspace.Dir)
			}
			if !now.After(entry.Last) {
				now = entry.Last.Add(time.Nanosecond)
			}
			entry.Last = now
			workspace.Last = now
			found = true
			break
		}
		if !found {
			workspace.Last = now
			registry = append(registry, workspace)
		}
		return s.writeRegistryUnlocked(registry)
	})
	return workspace, err
}
