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
	dir := filepath.Join(home, ".manager")
	if info, err := os.Lstat(dir); os.IsNotExist(err) && !exclusive {
		return func() {}, nil
	} else if err != nil {
		return nil, err
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf(".manager must be a directory")
	}
	return Lock(filepath.Join(dir, "maintenance.lock"), exclusive)
}
func Lock(path string, exclusive bool) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	operation := syscall.LOCK_SH
	if exclusive {
		operation = syscall.LOCK_EX
	}
	if err = syscall.Flock(int(f.Fd()), operation|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("Horizon is busy; finish active turns and stop plugin services before maintenance (or retry after maintenance)")
		}
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}
