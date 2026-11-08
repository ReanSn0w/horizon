package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ReanSn0w/horizon/config"
	"github.com/ReanSn0w/horizon/session"
)

const Instructions = memoryInstructions
const AddDescription = memorySavingGuidance

type Service struct {
	store     *memoryStore
	summarize summarizer
}

func New(home, workspace string, cfg config.Config) (*Service, error) {
	home, err := session.NormalizeWorkspace(home)
	if err != nil {
		return nil, err
	}
	workspace, err = session.NormalizeWorkspace(workspace)
	if err != nil {
		return nil, err
	}
	settings, err := loadSettings(cfg)
	if err != nil {
		return nil, err
	}
	return &Service{store: newStore(home, workspace, settings), summarize: apiSummarizer(cfg, settings)}, nil
}

func (s *Service) Context() ([]Block, error)          { return s.store.context() }
func (s *Service) Read(scope string) ([]Block, error) { return s.store.contextScopes([]string{scope}) }
func (s *Service) Show(scope string) (any, error)     { return s.store.read(scope) }
func (s *Service) Compact(ctx context.Context, scope string, manual bool) (any, error) {
	return s.store.compact(ctx, scope, manual, s.summarize)
}

func (s *Service) Add(ctx context.Context, scope, text, operationID string) (any, error) {
	note, duplicate, err := s.store.add(ctx, scope, text, operationID)
	if err != nil {
		return nil, err
	}
	status, compactErr := s.store.compact(ctx, scope, false, s.summarize)
	if compactErr != nil {
		status = compaction{Status: "error", Error: compactErr.Error()}
	}
	return map[string]any{"saved": true, "id": note.ID, "duplicate": duplicate, "compaction": status}, nil
}

func (s *Service) Clear(ctx context.Context, scope string) (any, error) {
	err := s.store.clear(ctx, scope)
	return map[string]any{"cleared": err == nil, "scope": scope}, err
}

func (s *Service) Maintain(ctx context.Context) (map[string]any, error) {
	result := map[string]any{}
	var failures []error
	for _, scope := range []string{"agent", "user", "workspace"} {
		status, err := s.store.compact(ctx, scope, false, s.summarize)
		if err != nil {
			status = compaction{Status: "error", Error: err.Error()}
			failures = append(failures, fmt.Errorf("%s: %w", scope, err))
		}
		result[scope] = status
	}
	return result, errors.Join(failures...)
}

func (s *Service) Watch(ctx context.Context, interval time.Duration, diagnostics io.Writer) error {
	if interval < time.Second || interval > 24*time.Hour {
		return fmt.Errorf("period must be between 1s and 24h")
	}
	return watch(ctx, s.store, interval, s.summarize, diagnostics)
}

func (m *memoryStore) existing() ([]*memoryStore, error) {
	result := []*memoryStore{m}
	var failures []error
	paths, err := filepath.Glob(filepath.Join(m.home, "memory", "workspaces", "*.json"))
	if err != nil {
		return result, err
	}
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		data, err := io.ReadAll(io.LimitReader(file, maxStateBytes+1))
		file.Close()
		if err != nil || len(data) > maxStateBytes {
			failures = append(failures, fmt.Errorf("read memory state %s", path))
			continue
		}
		var state state
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		decodeErr := decoder.Decode(&state)
		if decodeErr == nil {
			if trailing := decoder.Decode(&struct{}{}); !errors.Is(trailing, io.EOF) {
				decodeErr = errors.New("trailing memory state data")
			}
		}
		if decodeErr != nil || state.Workspace == "" || filepath.Base(path) != session.WorkspaceID(state.Workspace)+".json" {
			failures = append(failures, fmt.Errorf("invalid stored workspace identity"))
			continue
		}
		if state.Workspace != m.workspace {
			other := newStore(m.home, state.Workspace, m.settings)
			other.now = m.now
			result = append(result, other)
		}
	}
	return result, errors.Join(failures...)
}

func watch(ctx context.Context, store *memoryStore, interval time.Duration, summarize summarizer, diagnostics io.Writer) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	nextWarning := time.Time{}
	for {
		warn := func(err error) {
			if !store.now().Before(nextWarning) {
				fmt.Fprintf(diagnostics, "memory watch: %s\n", err)
				nextWarning = store.now().Add(store.settings.retryInterval)
			}
		}
		stores, err := store.existing()
		if err != nil {
			warn(err)
		}
		for index, current := range stores {
			scopes := []string{"workspace"}
			if index == 0 {
				scopes = []string{"agent", "user", "workspace"}
			}
			for _, scope := range scopes {
				if _, err := current.compact(ctx, scope, false, summarize); err != nil {
					warn(err)
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func NewForCLI(home, workspace string, cfg config.Config) (*Service, error) {
	if workspace == "" {
		var err error
		workspace, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	return New(home, workspace, cfg)
}
