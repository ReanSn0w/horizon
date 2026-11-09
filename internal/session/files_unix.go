//go:build unix

package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

var ErrBusy = errors.New("session is busy")

type fileLock struct {
	file *os.File
}

func acquireFileLock(path string, nonblocking bool) (*fileLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	operation := syscall.LOCK_EX
	if nonblocking {
		operation |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(file.Fd()), operation); err != nil {
		file.Close()
		if nonblocking && (errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)) {
			return nil, ErrBusy
		}
		return nil, err
	}
	return &fileLock{file: file}, nil
}

func (lock *fileLock) Close() error {
	unlockErr := syscall.Flock(int(lock.file.Fd()), syscall.LOCK_UN)
	closeErr := lock.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

func (s *Store) withRegistryLock(action func() error) error {
	if err := os.MkdirAll(s.DialogsDir(), 0o700); err != nil {
		return fmt.Errorf("create dialogs directory: %w", err)
	}
	lockPath := filepath.Join(s.DialogsDir(), "workspaces.lock")
	lock, err := acquireFileLock(lockPath, false)
	if err != nil {
		return fmt.Errorf("open registry lock: %w", err)
	}
	defer lock.Close() //nolint:errcheck
	return action()
}

func (s *Store) withWorkspaceLock(workspace Workspace, action func() error) error {
	lock, err := acquireFileLock(filepath.Join(s.WorkspaceDir(workspace.ID), ".workspace.lock"), false)
	if err != nil {
		return fmt.Errorf("lock workspace %q: %w", workspace.Dir, err)
	}
	defer lock.Close() //nolint:errcheck
	return action()
}

func writeJSONAtomic(path string, value any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	keep = true
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) {
		return err
	}
	return nil
}
