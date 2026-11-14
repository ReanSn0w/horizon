// Package maintenance coordinates running commands with installation changes.
package maintenance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Acquire returns a shared runtime or exclusive maintenance lock. Unmanaged
// homes have no gate until installation creates .manager. Lock files persist.
func Acquire(home string, exclusive bool) (func(), error) {
	return acquire(home, exclusive, true)
}

// AcquireReadOnly takes the shared lock without initializing maintenance files.
// A managed installation already has its permanent lock.
func AcquireReadOnly(home string) (func(), error) {
	return acquire(home, false, false)
}

func acquire(home string, exclusive, create bool) (func(), error) {
	dir := filepath.Join(home, ".manager")
	if info, err := os.Lstat(dir); os.IsNotExist(err) && !exclusive {
		return func() {}, nil
	} else if err != nil {
		return nil, err
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf(".manager must be a directory")
	}
	path := filepath.Join(dir, "maintenance.lock")
	if create {
		return Lock(path, exclusive)
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		if _, stateErr := os.Lstat(filepath.Join(dir, "state.json")); errors.Is(stateErr, os.ErrNotExist) {
			return func() {}, nil
		}
	}
	if err != nil {
		return nil, err
	}
	return flock(f, false)
}
func Lock(path string, exclusive bool) (func(), error) {
	_, release, err := LockHandle(path, exclusive)
	return release, err
}

func flock(f *os.File, exclusive bool) (func(), error) {
	operation := syscall.LOCK_SH
	if exclusive {
		operation = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(f.Fd()), operation|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("Horizon is busy; finish active turns and stop plugin services before maintenance (or retry after maintenance)")
		}
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

// LockHandle exposes the locked descriptor for inheritance by a maintenance
// subprocess. The caller retains the lock until that subprocess has exited.
func LockHandle(path string, exclusive bool) (*os.File, func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, nil, err
	}
	release, err := flock(f, exclusive)
	if err != nil {
		return nil, nil, err
	}
	return f, release, nil
}
