package manager

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type replacement struct {
	Path    string `json:"path"`
	Existed bool   `json:"existed"`
	Remove  bool   `json:"remove,omitempty"`
}
type journal struct {
	Version      int           `json:"version"`
	Done         bool          `json:"done"`
	ConfigTarget string        `json:"config_target"`
	Entries      []replacement `json:"entries"`
}

func transactionDir(home string) string { return filepath.Join(home, ".manager", "transaction") }
func validReplacement(path string) bool {
	if path == "source" || path == "config.yaml" || path == ".manager/state.json" || path == "bin/horizon" {
		return true
	}
	return strings.HasPrefix(path, "bin/horizon-") && namePattern.MatchString(strings.TrimPrefix(path, "bin/horizon-"))
}
func backupPath(tx string, i int) string { return filepath.Join(tx, "backup", fmt.Sprint(i)) }
func configTarget(home string) (string, error) {
	return filepath.EvalSymlinks(filepath.Join(home, "config.yaml"))
}
func copyPrivate(source, dest string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	return atomicWrite(dest, data, info.Mode().Perm())
}

// recoverTransaction is idempotent, including a crash while restoring a backup.
// Backups are copied for files and retained until the journal is removed.
func recoverTransaction(home string) error {
	tx := transactionDir(home)
	data, err := os.ReadFile(filepath.Join(tx, "journal.json"))
	if os.IsNotExist(err) {
		return os.RemoveAll(tx)
	}
	if err != nil {
		return err
	}
	var j journal
	if json.Unmarshal(data, &j) != nil || j.Version != 1 {
		return errors.New("invalid installation journal; retain .manager/transaction for manual recovery")
	}
	if j.Done {
		return os.RemoveAll(tx)
	}
	target, err := configTarget(home)
	if err != nil || target != j.ConfigTarget {
		return errors.New("configuration target changed during interrupted installation; restore its symlink before recovery")
	}
	for i := len(j.Entries) - 1; i >= 0; i-- {
		entry := j.Entries[i]
		if !validReplacement(entry.Path) {
			return errors.New("invalid replacement in installation journal")
		}
		dest := filepath.Join(home, entry.Path)
		if entry.Path == "config.yaml" {
			dest = target
		}
		backup := backupPath(tx, i)
		if entry.Existed {
			if _, err := os.Lstat(backup); os.IsNotExist(err) {
				continue
			} else if err != nil {
				return err
			}
			if entry.Path == "source" {
				// Record rollback intent before moving a directory backup back. The marker
				// avoids deleting a restored checkout if recovery itself is interrupted.
				marker := backup + ".restoring"
				if _, err := os.Stat(marker); os.IsNotExist(err) {
					if err = os.RemoveAll(dest); err != nil {
						return err
					}
					if err = atomicWrite(marker, []byte("restore"), 0600); err != nil {
						return err
					}
				}
				if err = os.Rename(backup, dest); err != nil {
					return err
				}
			} else if err = copyPrivate(backup, dest); err != nil {
				return err
			}
		} else if err = os.RemoveAll(dest); err != nil {
			return err
		}
	}
	return os.RemoveAll(tx)
}
func (e *Engine) deploy(home, stage string, paths []string, remove map[string]bool) (err error) {
	tx := transactionDir(home)
	if _, statErr := os.Stat(tx); statErr == nil {
		return errors.New("unfinished transaction directory; recover before installing")
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	if err = os.MkdirAll(filepath.Join(tx, "backup"), 0700); err != nil {
		return err
	}
	j := journal{Version: 1}
	j.ConfigTarget, err = configTarget(home)
	if err != nil {
		os.RemoveAll(tx)
		return err
	}
	for _, path := range paths {
		if !validReplacement(path) {
			os.RemoveAll(tx)
			return fmt.Errorf("invalid replacement %s", path)
		}
		info, statErr := os.Lstat(filepath.Join(home, path))
		exists := statErr == nil
		if statErr != nil && !os.IsNotExist(statErr) {
			os.RemoveAll(tx)
			return statErr
		}
		if exists && path != "source" && path != "config.yaml" && (!info.Mode().IsRegular()) {
			os.RemoveAll(tx)
			return fmt.Errorf("managed destination %s must be a regular file", path)
		}
		j.Entries = append(j.Entries, replacement{Path: path, Existed: exists, Remove: remove[path]})
	}
	if err = writeJSON(filepath.Join(tx, "journal.json"), j); err != nil {
		os.RemoveAll(tx)
		return err
	}
	defer func() {
		if err != nil {
			if rollbackErr := recoverTransaction(home); rollbackErr != nil {
				err = fmt.Errorf("%w; recovery failed: %v; run the downloaded manager again", err, rollbackErr)
			}
		}
	}()
	for i, entry := range j.Entries {
		dest := filepath.Join(home, entry.Path)
		if entry.Path == "config.yaml" {
			dest = j.ConfigTarget
		}
		if entry.Existed {
			if entry.Path == "source" {
				err = os.Rename(dest, backupPath(tx, i))
			} else {
				err = copyPrivate(dest, backupPath(tx, i))
			}
			if err != nil {
				return err
			}
		}
		if entry.Remove {
			err = os.Remove(dest)
			if os.IsNotExist(err) {
				err = nil
			}
		} else if entry.Path == "config.yaml" {
			err = copyPrivate(filepath.Join(stage, entry.Path), dest)
		} else {
			err = os.Rename(filepath.Join(stage, entry.Path), dest)
		}
		if err != nil {
			return err
		}
		if e.afterReplace != nil {
			if err = e.afterReplace(i); err != nil {
				return err
			}
		}
	}
	j.Done = true
	if err = writeJSON(filepath.Join(tx, "journal.json"), j); err != nil {
		return err
	}
	return os.RemoveAll(tx)
}
