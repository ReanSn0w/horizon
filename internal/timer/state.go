package timer

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ReanSn0w/horizon/internal/maintenance"
)

type Submission struct {
	At        time.Time `json:"at"`
	Revision  [32]byte  `json:"revision"`
	Workspace string    `json:"workspace"`
	TaskID    string    `json:"task_id,omitempty"`
	TaskState string    `json:"task_state,omitempty"`
}

func (s Submission) OperationID(id string) string {
	return "timer:" + id + ":" + s.At.UTC().Format(time.RFC3339Nano)
}

type Record struct {
	Through    time.Time   `json:"through"`
	Pending    *Submission `json:"pending,omitempty"`
	Block      string      `json:"blocked,omitempty"`
	LastEvent  string      `json:"last_event,omitempty"`
	LastTaskID string      `json:"last_task_id,omitempty"`
}

type stateFile struct {
	Version int               `json:"version"`
	Timers  map[string]Record `json:"timers"`
}

type stateStore struct {
	home string
	save func(string, []byte) error
}

func newStateStore(home string) stateStore {
	return stateStore{home: home, save: saveState}
}

func (s stateStore) directory() string { return filepath.Join(s.home, "timer") }

func (s stateStore) read() (map[string]Record, error) {
	empty := map[string]Record{}
	if err := s.checkDirectory(); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return empty, nil
		}
		return nil, err
	}
	path := filepath.Join(s.directory(), "state.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, errors.New("timer state must be a private regular file (0600)")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state stateFile
	if err := json.Unmarshal(data, &state); err != nil || state.Version != 1 || state.Timers == nil {
		return nil, errors.New("invalid timer state; inspect it before restarting")
	}
	for id, record := range state.Timers {
		if !ValidID(id) || record.Through.IsZero() || record.Pending != nil &&
			(record.Pending.At.IsZero() || record.Pending.At.After(record.Through) || !filepath.IsAbs(record.Pending.Workspace)) {
			return nil, errors.New("invalid timer record; inspect timer state before restarting")
		}
	}
	return state.Timers, nil
}

func (s stateStore) checkDirectory() error {
	info, err := os.Lstat(s.directory())
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("timer state directory must be a private directory (0700)")
	}
	return nil
}

func (s stateStore) ensureDirectory() error {
	if err := os.Mkdir(s.directory(), 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return s.checkDirectory()
}

func (s stateStore) update(id string, update func(*Record) bool) error {
	if err := s.ensureDirectory(); err != nil {
		return err
	}
	unlock, err := maintenance.Lock(filepath.Join(s.directory(), "state.lock"), true)
	if err != nil {
		return err
	}
	defer unlock()
	timers, err := s.read()
	if err != nil {
		return err
	}
	record := timers[id]
	if !update(&record) {
		return nil
	}
	timers[id] = record
	data, err := json.Marshal(stateFile{Version: 1, Timers: timers})
	if err != nil {
		return err
	}
	return s.save(filepath.Join(s.directory(), "state.json"), data)
}

func saveState(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".state-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(append(data, '\n'))
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

// Status reads a snapshot of durable timer records without creating any files.
func Status(home string) (map[string]Record, error) { return newStateStore(home).read() }

// Unblock allows future firings and retains the high-water mark and last task.
// It never submits or replays the task whose outcome was unknown.
func Unblock(home, id string) error {
	if !ValidID(id) {
		return invalidID(id)
	}
	store := newStateStore(home)
	records, err := store.read()
	if err != nil {
		return err
	}
	if records[id].Block == "" {
		return fmt.Errorf("timer %q is not blocked", id)
	}
	unblocked := false
	err = store.update(id, func(record *Record) bool {
		if record.Block == "" {
			return false
		}
		record.Block = ""
		record.Pending = nil
		record.LastEvent = "unblocked for future slots"
		unblocked = true
		return true
	})
	if err == nil && !unblocked {
		return fmt.Errorf("timer %q is not blocked", id)
	}
	return err
}
