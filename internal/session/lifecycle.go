package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

func (s *Store) Fork(workspace Workspace, sourceID string) (*Session, error) {
	found, err := s.findSessionWorkspace(sourceID)
	if err != nil {
		return nil, err
	}
	if found.Dir != workspace.Dir {
		return nil, &WrongWorkspaceError{SessionID: sourceID, Expected: found.Dir, Actual: workspace.Dir}
	}

	var forked *Session
	err = s.withWorkspaceLock(workspace, func() error {
		sourceLock, err := s.LockSession(workspace, sourceID)
		if err != nil {
			return err
		}
		defer sourceLock.Close()
		source, err := sourceLock.Load()
		if err != nil {
			return err
		}

		id, err := NewID()
		if err != nil {
			return err
		}
		targetLock, err := s.LockSession(workspace, id)
		if err != nil {
			return err
		}
		defer targetLock.Close()

		forked, err = cloneForFork(source)
		if err != nil {
			return err
		}
		now, err := s.nextAccessTime(workspace)
		if err != nil {
			return err
		}
		forked.SessionID = id
		forked.Workspace = workspace.Dir
		forked.Title = source.Title + " (форк)"
		forked.CreatedAt = now
		forked.LastAccessedAt = now
		forked.Fork.ForkedAt = now

		cleanup, err := s.copyForkArtifacts(workspace, sourceID, forked)
		if err != nil {
			return err
		}
		published := false
		defer func() {
			if !published {
				cleanup()
			}
		}()
		if err := targetLock.Save(forked); err != nil {
			return err
		}
		published = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, err := s.TouchWorkspace(workspace); err != nil {
		return nil, err
	}
	return forked, nil
}

func cloneForFork(source *Session) (*Session, error) {
	data, err := json.Marshal(source)
	if err != nil {
		return nil, err
	}
	var target Session
	if err := json.Unmarshal(data, &target); err != nil {
		return nil, err
	}
	last, ok := source.LastCompletedTurn()
	boundaryID := ""
	if !ok {
		target.Turns = []Turn{}
		target.Checkpoints = nil
		target.Compactions = nil
	} else {
		boundaryID = source.Turns[last].ID
		target.Turns = target.Turns[:last+1]
		included := make(map[string]bool, last+1)
		for _, turn := range target.Turns {
			included[turn.ID] = true
		}
		target.Checkpoints = filterCheckpoints(target.Checkpoints, included)
		target.Compactions = filterCompactions(target.Compactions, included)
	}
	target.Fork = &ForkOrigin{SessionID: source.SessionID, TurnID: boundaryID}
	return &target, nil
}

func filterCheckpoints(values []ContextCheckpoint, included map[string]bool) []ContextCheckpoint {
	result := values[:0]
	for _, value := range values {
		if included[value.TurnID] {
			result = append(result, value)
		}
	}
	return result
}

func filterCompactions(values []Compaction, included map[string]bool) []Compaction {
	result := values[:0]
	for _, value := range values {
		if included[value.BoundaryTurnID] {
			result = append(result, value)
		}
	}
	return result
}

func (s *Store) copyForkArtifacts(workspace Workspace, sourceID string, target *Session) (func(), error) {
	sourceDir := s.ArtifactsDir(workspace, sourceID)
	targetDir := s.ArtifactsDir(workspace, target.SessionID)
	var references []*Artifact
	for turnIndex := range target.Turns {
		for callIndex := range target.Turns[turnIndex].ToolCalls {
			for artifactIndex := range target.Turns[turnIndex].ToolCalls[callIndex].Artifacts {
				references = append(references, &target.Turns[turnIndex].ToolCalls[callIndex].Artifacts[artifactIndex])
			}
		}
	}
	cleanup := func() { _ = os.RemoveAll(targetDir) }
	if len(references) == 0 {
		return cleanup, nil
	}
	staging, err := os.MkdirTemp(s.WorkspaceDir(workspace.ID), "."+target.SessionID+".artifacts.tmp-")
	if err != nil {
		return cleanup, err
	}
	stagingCleanup := func() {
		_ = os.RemoveAll(staging)
		_ = os.RemoveAll(targetDir)
	}
	for _, reference := range references {
		sourcePath := filepath.Clean(reference.Path)
		if !filepath.IsAbs(sourcePath) || filepath.Dir(sourcePath) != sourceDir {
			stagingCleanup()
			return cleanup, fmt.Errorf("artifact path %q is outside source artifact directory", reference.Path)
		}
		info, err := os.Lstat(sourcePath)
		if err != nil {
			stagingCleanup()
			return cleanup, fmt.Errorf("inspect artifact %q: %w", sourcePath, err)
		}
		if !info.Mode().IsRegular() {
			stagingCleanup()
			return cleanup, fmt.Errorf("artifact %q is not a regular file", sourcePath)
		}
		name := filepath.Base(sourcePath)
		if err := copyRegularFile(sourcePath, filepath.Join(staging, name)); err != nil {
			stagingCleanup()
			return cleanup, err
		}
		reference.Path = filepath.Join(targetDir, name)
	}
	if err := syncDirectory(staging); err != nil {
		stagingCleanup()
		return cleanup, err
	}
	if err := os.Rename(staging, targetDir); err != nil {
		stagingCleanup()
		return cleanup, err
	}
	if err := syncDirectory(s.WorkspaceDir(workspace.ID)); err != nil {
		cleanup()
		return cleanup, err
	}
	return cleanup, nil
}

func copyRegularFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		out.Close()
		if !ok {
			_ = os.Remove(destination)
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (s *Store) Delete(workspace Workspace, id string) error {
	if _, err := s.ReadForWorkspace(workspace, id); err != nil {
		return err
	}
	return s.withWorkspaceLock(workspace, func() error {
		locked, err := s.LockSession(workspace, id)
		if err != nil {
			return err
		}
		defer locked.Close()
		if _, err := locked.Load(); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if err := os.RemoveAll(s.ArtifactsDir(workspace, id)); err != nil {
			return fmt.Errorf("delete session artifacts: %w", err)
		}
		path, err := s.SessionPath(workspace, id)
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("delete session file: %w", err)
		}
		return syncDirectory(s.WorkspaceDir(workspace.ID))
	})
}
