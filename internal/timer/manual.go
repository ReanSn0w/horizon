package timer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ReanSn0w/horizon/internal/daemon"
	"github.com/ReanSn0w/horizon/internal/maintenance"
)

type manualIntent struct {
	OperationID string   `json:"operation_id"`
	Revision    [32]byte `json:"revision"`
	Workspace   string   `json:"workspace"`
	Access      string   `json:"access"`
	Message     string   `json:"message"`
}

type daemonCall func(context.Context, string, daemon.Request) (daemon.Response, error)

// RunNow submits a fresh timer task. Its private intent survives an uncertain
// RPC outcome; the next invocation looks up the operation before retrying it.
func RunNow(ctx context.Context, home, id string) (string, error) {
	return runNow(ctx, home, id, daemon.Call)
}

func runNow(ctx context.Context, home, id string, call daemonCall) (string, error) {
	if !ValidID(id) {
		return "", invalidID(id)
	}
	store := newStateStore(home)
	if err := store.ensureDirectory(); err != nil {
		return "", err
	}
	unlock, err := maintenance.Lock(filepath.Join(store.directory(), "manual-"+id+".lock"), true)
	if err != nil {
		return "", err
	}
	defer unlock()
	path := filepath.Join(store.directory(), "manual-"+id+".json")
	var intent manualIntent
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return "", errors.New("manual timer intent must be a private regular file (0600)")
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return "", readErr
		}
		if err := json.Unmarshal(data, &intent); err != nil || !strings.HasPrefix(intent.OperationID, "timer:manual:"+id+":") || intent.Workspace == "" || intent.Message == "" {
			return "", errors.New("invalid manual timer intent; inspect it before retrying")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	caller := daemon.Caller{Kind: "operator"}
	if intent.OperationID != "" {
		response, lookupErr := call(ctx, home, daemon.Request{Method: "timer_lookup", Caller: caller, OperationID: intent.OperationID})
		if lookupErr == nil {
			var task daemon.Task
			if err := json.Unmarshal(response.Data, &task); err != nil || task.ID == "" || task.SourceKey != id {
				return "", errors.New("unexpected timer lookup result")
			}
			if err := os.Remove(path); err != nil {
				return "", err
			}
			return task.ID, nil
		}
		var failure *daemon.Failure
		if !errors.As(lookupErr, &failure) || failure.Code != "not_found" {
			return "", fmt.Errorf("lookup %s: %w", intent.OperationID, lookupErr)
		}
	}
	def, err := LoadFile(home, id)
	if err != nil {
		return "", err
	}
	if !def.Enabled {
		return "", fmt.Errorf("timer %q is disabled", id)
	}
	schedule, err := Compile(def.Schedule)
	if err != nil {
		return "", err
	}
	if _, err := schedule.Next(time.Now()); err != nil {
		return "", err
	}
	settings, err := LoadSettings(home)
	if err != nil {
		return "", err
	}
	if settings.Access == "read" {
		return "", errors.New("manual timer run requires write or full timer access")
	}
	if intent.OperationID == "" {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", err
		}
		intent = manualIntent{OperationID: "timer:manual:" + id + ":" + hex.EncodeToString(random[:]), Revision: def.Revision, Workspace: def.Workspace, Access: settings.Access, Message: def.Message}
		if err := saveManualIntent(path, intent); err != nil {
			return "", err
		}
	} else if intent.Revision != def.Revision || intent.Workspace != def.Workspace {
		return "", errors.New("timer changed since uncertain submission; inspect manual intent before retrying")
	}
	response, err := call(ctx, home, daemon.Request{Method: "timer_run", Caller: daemon.Caller{Kind: "operator", Workspace: intent.Workspace}, OperationID: intent.OperationID, SourceKey: id, Access: intent.Access, Message: intent.Message})
	if err != nil {
		var failure *daemon.Failure
		if errors.As(err, &failure) && (failure.Code == "timer_busy" || failure.Code == "queue_full" || failure.Code == "access_denied" || failure.Code == "invalid_request") {
			_ = os.Remove(path)
		}
		return "", fmt.Errorf("timer run %s: %w", intent.OperationID, err)
	}
	var task daemon.Task
	if err := json.Unmarshal(response.Data, &task); err != nil || task.ID == "" || task.SourceKey != id {
		return "", fmt.Errorf("unexpected timer result for %s", intent.OperationID)
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return task.ID, nil
}

func saveManualIntent(path string, intent manualIntent) error {
	data, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".manual-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
