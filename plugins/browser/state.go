package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ReanSn0w/horizon/internal/plugins"
	"github.com/ReanSn0w/horizon/internal/session"
)

type browserRecord struct {
	ID          string `json:"id"`
	CDPURL      string `json:"cdp_url"`
	TargetID    string `json:"target_id,omitempty"`
	SessionID   string `json:"session_id"`
	TurnID      string `json:"turn_id"`
	WorkspaceID string `json:"workspace_id"`
}

type browserState struct {
	home string
	dir  string
}

func newBrowserState(home string) browserState {
	return browserState{home: home, dir: filepath.Join(home, "browser")}
}

func (s browserState) withLock(action func() error) error {
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.dir, "state.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return action()
}

func (s browserState) homeID() (string, error) {
	path := filepath.Join(s.dir, "home-id")
	data, err := os.ReadFile(path)
	if err == nil {
		id := strings.TrimSpace(string(data))
		if id == "" || len(id) > 128 {
			return "", fmt.Errorf("invalid browser home ID")
		}
		return id, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	id, err := session.NewID()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(id+"\n"), 0600); err != nil {
		return "", err
	}
	return id, nil
}

func (s browserState) recordPath(turnID string) (string, error) {
	if turnID == "" || len(turnID) > 128 || strings.ContainsAny(turnID, `/\\.`) {
		return "", fmt.Errorf("invalid browser turn ID")
	}
	return filepath.Join(s.dir, "turn-"+turnID+".json"), nil
}

func (s browserState) load(turnID string) (browserRecord, error) {
	path, err := s.recordPath(turnID)
	if err != nil {
		return browserRecord{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return browserRecord{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil || len(data) > 16384 {
		return browserRecord{}, fmt.Errorf("invalid browser state")
	}
	var record browserRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return browserRecord{}, fmt.Errorf("invalid browser state")
	}
	if record.ID == "" || record.TurnID != turnID || record.SessionID == "" || record.WorkspaceID == "" {
		return browserRecord{}, fmt.Errorf("invalid browser state")
	}
	return record, nil
}

func (s browserState) save(record browserRecord) error {
	path, err := s.recordPath(record.TurnID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, ".turn-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (s browserState) remove(turnID string) error {
	path, err := s.recordPath(turnID)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s browserState) records() ([]browserRecord, error) {
	paths, err := filepath.Glob(filepath.Join(s.dir, "turn-*.json"))
	if err != nil {
		return nil, err
	}
	result := make([]browserRecord, 0, len(paths))
	for _, path := range paths {
		turnID := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "turn-"), ".json")
		record, err := s.load(turnID)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, nil
}

func (s browserState) turnBusy(sessionID, workspaceID string) (bool, error) {
	if sessionID == "" || workspaceID == "" || strings.ContainsAny(sessionID, `/\\.`) || strings.ContainsAny(workspaceID, `/\\.`) {
		return false, fmt.Errorf("invalid browser session reference")
	}
	path := filepath.Join(s.home, "dialogs", workspaceID, sessionID+".lock")
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return true, nil
		}
		return false, err
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false, nil
}

func (s browserState) findRemoteRecord(remote browserSession, records []browserRecord) *browserRecord {
	for i := range records {
		if records[i].ID == remote.ID {
			return &records[i]
		}
	}
	return nil
}

func (s browserState) cleanupStale(ctx context.Context, api browserAPI, homeID string) error {
	remote, err := api.list(ctx, homeID)
	if err != nil {
		return err
	}
	records, err := s.records()
	if err != nil {
		return err
	}
	for _, item := range remote {
		record := s.findRemoteRecord(item, records)
		sessionID, workspaceID := item.Metadata["horizon_session"], item.Metadata["horizon_workspace"]
		if record != nil {
			sessionID, workspaceID = record.SessionID, record.WorkspaceID
		}
		if sessionID == "" || workspaceID == "" {
			continue
		}
		busy, err := s.turnBusy(sessionID, workspaceID)
		if err != nil {
			return err
		}
		if busy {
			continue
		}
		if err := api.stop(ctx, item.ID); err != nil {
			return err
		}
		if record != nil {
			if err := s.remove(record.TurnID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s browserState) recordFor(request plugins.Request) (browserRecord, error) {
	if request.SessionID == "" || request.TurnID == "" {
		return browserRecord{}, fmt.Errorf("browser tool requires a turn")
	}
	record, err := s.load(request.TurnID)
	if err != nil {
		return record, err
	}
	if record.SessionID != request.SessionID || record.WorkspaceID != request.WorkspaceID {
		return browserRecord{}, fmt.Errorf("browser belongs to a different turn or workspace")
	}
	return record, nil
}
