package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ReanSn0w/horizon/internal/plugins"
)

type browserLifecycle struct {
	state browserState
	api   browserAPI
	s     settings
}

func newBrowserLifecycle(home string, settings settings) browserLifecycle {
	return browserLifecycle{state: newBrowserState(home), api: newBrowserAPI(settings), s: settings}
}

func (b browserLifecycle) attemptPath(turnID string) (string, error) {
	if _, err := b.state.recordPath(turnID); err != nil {
		return "", err
	}
	return filepath.Join(b.state.dir, "attempt-"+turnID), nil
}

func (b browserLifecycle) open(ctx context.Context, request plugins.Request) (browserRecord, error) {
	var record browserRecord
	err := b.state.withLock(func() error {
		if request.TurnID == "" || request.SessionID == "" || request.WorkspaceID == "" {
			return fmt.Errorf("browser navigation requires a turn")
		}
		previous, err := b.state.load(request.TurnID)
		if err == nil {
			if previous.SessionID != request.SessionID || previous.WorkspaceID != request.WorkspaceID {
				return fmt.Errorf("browser belongs to another turn")
			}
			record = previous
			return nil
		}
		if !os.IsNotExist(err) {
			return err
		}
		homeID, err := b.state.homeID()
		if err != nil {
			return err
		}
		remote, err := b.api.list(ctx, homeID)
		if err != nil {
			return err
		}
		// Recover a creation whose response was received but state persistence failed.
		for _, item := range remote {
			if item.Metadata["horizon_turn"] == request.TurnID && item.Metadata["horizon_session"] == request.SessionID && item.Metadata["horizon_workspace"] == request.WorkspaceID {
				if item.CDPURL == "" {
					item, err = b.api.get(ctx, item.ID)
					if err != nil {
						return err
					}
					if item.CDPURL == "" {
						return fmt.Errorf("existing browser has no CDP URL")
					}
				}
				record = browserRecord{ID: item.ID, CDPURL: item.CDPURL, SessionID: request.SessionID, TurnID: request.TurnID, WorkspaceID: request.WorkspaceID}
				return b.state.save(record)
			}
		}
		if err := b.state.cleanupStale(ctx, b.api, homeID); err != nil {
			return err
		}
		attempt, err := b.attemptPath(request.TurnID)
		if err != nil {
			return err
		}
		if _, err := os.Stat(attempt); err == nil {
			return fmt.Errorf("previous browser creation outcome is unknown; inspect browser list before retrying")
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.WriteFile(attempt, []byte("pending\n"), 0600); err != nil {
			return err
		}
		created, err := b.api.create(ctx, homeID, request.TurnID, request.SessionID, request.WorkspaceID, b.s.TimeoutMinutes)
		if err != nil {
			if created.ID != "" {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = b.api.stop(cleanupCtx, created.ID)
			}
			return err
		}
		record = browserRecord{ID: created.ID, CDPURL: created.CDPURL, SessionID: request.SessionID, TurnID: request.TurnID, WorkspaceID: request.WorkspaceID}
		if err := b.state.save(record); err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = b.api.stop(cleanupCtx, record.ID)
			return err
		}
		return os.Remove(attempt)
	})
	return record, err
}

func (b browserLifecycle) finish(ctx context.Context, request plugins.Request) error {
	return b.state.withLock(func() error {
		if request.TurnID == "" || request.SessionID == "" {
			return fmt.Errorf("browser finalization requires a turn")
		}
		record, err := b.state.load(request.TurnID)
		if os.IsNotExist(err) {
			attempt, pathErr := b.attemptPath(request.TurnID)
			if pathErr != nil {
				return pathErr
			}
			if _, statErr := os.Stat(attempt); os.IsNotExist(statErr) {
				return nil
			} else if statErr != nil {
				return statErr
			}
			homeID, err := b.state.homeID()
			if err != nil {
				return err
			}
			remote, err := b.api.list(ctx, homeID)
			if err != nil {
				return err
			}
			for _, item := range remote {
				if item.Metadata["horizon_turn"] == request.TurnID && item.Metadata["horizon_session"] == request.SessionID && item.Metadata["horizon_workspace"] == request.WorkspaceID {
					if err := b.api.stop(ctx, item.ID); err != nil {
						return err
					}
					return os.Remove(attempt)
				}
			}
			return fmt.Errorf("browser creation outcome is unknown; inspect browser list and retry cleanup later")
		}
		if err != nil {
			return err
		}
		if record.SessionID != request.SessionID || record.WorkspaceID != request.WorkspaceID {
			return fmt.Errorf("browser belongs to another turn")
		}
		if err := b.api.stop(ctx, record.ID); err != nil {
			return err
		}
		return b.state.remove(request.TurnID)
	})
}
