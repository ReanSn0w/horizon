package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/ReanSn0w/horizon/internal/config"
)

func browserCLI(ctx context.Context, command string, args []string, stale bool, stdout, stderr io.Writer) int {
	fail := func(code int, err error) int { fmt.Fprintf(stderr, "browser: %s\n", err); return code }
	if command == "list" && (len(args) != 0 || stale) || command == "close" && (stale && len(args) != 0 || !stale && len(args) != 1) {
		return fail(2, fmt.Errorf("invalid arguments"))
	}
	if command == "close" && os.Getenv("HORIZON_INHERITED_ACCESS") != "" && os.Getenv("HORIZON_INHERITED_ACCESS") != "full" {
		return fail(2, fmt.Errorf("closing browsers requires full access"))
	}
	home, err := config.ResolveHome("")
	if err != nil {
		return fail(2, err)
	}
	settings, err := configuration(home)
	if err != nil {
		return fail(2, err)
	}
	b := newBrowserLifecycle(home, settings)
	err = b.state.withLock(func() error {
		homeID, err := b.state.homeID()
		if err != nil {
			return err
		}
		switch command {
		case "list":
			return b.listCLI(ctx, homeID, stdout)
		case "close":
			if stale {
				return b.closeStaleCLI(ctx, homeID, stdout)
			}
			return b.closeOneCLI(ctx, homeID, args[0], stdout)
		}
		return fmt.Errorf("unknown browser command")
	})
	if err != nil {
		if command == "list" {
			fmt.Fprintln(stderr, "browser: состояние не проверено")
		}
		return fail(1, err)
	}
	return 0
}

func (b browserLifecycle) listCLI(ctx context.Context, homeID string, out io.Writer) error {
	remote, err := b.api.list(ctx, homeID)
	if err != nil {
		return err
	}
	records, err := b.state.records()
	if err != nil {
		return err
	}
	sort.Slice(remote, func(i, j int) bool { return remote[i].StartedAt < remote[j].StartedAt })
	seen := map[string]bool{}
	for _, item := range remote {
		seen[item.ID] = true
		status, err := b.remoteState(item, records)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\n", item.ID, item.StartedAt, item.TimeoutAt, item.Metadata["horizon_turn"], status)
	}
	for _, record := range records {
		if !seen[record.ID] {
			fmt.Fprintf(out, "%s\t-\t-\t%s\tлокальная запись без активного браузера\n", record.ID, record.TurnID)
		}
	}
	return nil
}

func (b browserLifecycle) remoteState(item browserSession, records []browserRecord) (string, error) {
	record := b.state.findRemoteRecord(item, records)
	sessionID, workspaceID := item.Metadata["horizon_session"], item.Metadata["horizon_workspace"]
	if record != nil {
		sessionID, workspaceID = record.SessionID, record.WorkspaceID
	}
	if sessionID == "" || workspaceID == "" {
		return "состояние хода не определено", nil
	}
	turnID := item.Metadata["horizon_turn"]
	if record != nil {
		turnID = record.TurnID
	}
	busy, err := b.state.turnBusy(sessionID, workspaceID, turnID)
	if err != nil {
		return "", err
	}
	if busy {
		return "выполняющийся ход", nil
	}
	return "оставшийся после сбоя", nil
}

func (b browserLifecycle) closeOneCLI(ctx context.Context, homeID, id string, out io.Writer) error {
	item, err := b.api.get(ctx, id)
	if err != nil {
		return err
	}
	if item.ID != id || item.Metadata["horizon_home"] != homeID {
		return fmt.Errorf("browser does not belong to this Horizon home")
	}
	if item.Status == "active" {
		if err := b.api.stop(ctx, id); err != nil {
			return err
		}
	}
	records, err := b.state.records()
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.ID == id {
			if err := b.state.remove(record.TurnID); err != nil {
				return err
			}
		}
	}
	fmt.Fprintf(out, "closed %s\n", id)
	return nil
}

func (b browserLifecycle) closeStaleCLI(ctx context.Context, homeID string, out io.Writer) error {
	remote, err := b.api.list(ctx, homeID)
	if err != nil {
		return err
	}
	records, err := b.state.records()
	if err != nil {
		return err
	}
	closed := 0
	seen := map[string]bool{}
	for _, item := range remote {
		seen[item.ID] = true
		status, err := b.remoteState(item, records)
		if err != nil {
			return err
		}
		if status != "оставшийся после сбоя" {
			continue
		}
		if err := b.api.stop(ctx, item.ID); err != nil {
			return err
		}
		if record := b.state.findRemoteRecord(item, records); record != nil {
			if err := b.state.remove(record.TurnID); err != nil {
				return err
			}
		}
		closed++
	}
	removed := 0
	for _, record := range records {
		if seen[record.ID] {
			continue
		}
		busy, err := b.state.turnBusy(record.SessionID, record.WorkspaceID, record.TurnID)
		if err != nil {
			return err
		}
		if !busy {
			if err := b.state.remove(record.TurnID); err != nil {
				return err
			}
			removed++
		}
	}
	fmt.Fprintf(out, "closed %d stale browsers, removed %d local records\n", closed, removed)
	return nil
}
