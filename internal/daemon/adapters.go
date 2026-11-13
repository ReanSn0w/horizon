package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

const AdapterAdminCLI = "admin-cli"

var adapterName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

type AdapterMetadata struct {
	ProtocolVersion       int      `json:"protocol_version"`
	Version               string   `json:"version"`
	Description           string   `json:"description"`
	Capabilities          []string `json:"capabilities"`
	ConfigProtocolVersion int      `json:"config_protocol_version,omitempty"`
}

func AdapterPath(home, name string) (string, error) {
	if !adapterName.MatchString(name) {
		return "", errors.New("invalid adapter name")
	}
	p := filepath.Join(home, "bin", "agent-"+name)
	i, e := os.Stat(p)
	if e != nil {
		return "", e
	}
	if !i.Mode().IsRegular() || i.Mode()&0111 == 0 {
		return "", errors.New("adapter is not executable")
	}
	return p, nil
}
func AdapterInspect(ctx context.Context, home, name string) (AdapterMetadata, error) {
	var m AdapterMetadata
	p, e := AdapterPath(home, name)
	if e != nil {
		return m, e
	}
	b, e := AdapterCommand(ctx, p, home, "agent-plugin-metadata")
	if e != nil {
		return m, e
	}
	d := json.NewDecoder(bytes.NewReader(b))
	if d.Decode(&m) != nil || d.Decode(new(any)) != io.EOF || m.ProtocolVersion != 1 || m.Version == "" || m.Description == "" || m.Capabilities == nil || m.ConfigProtocolVersion < 0 || m.ConfigProtocolVersion > 1 {
		return m, errors.New("invalid adapter metadata")
	}
	return m, nil
}
func AdapterCommand(ctx context.Context, path, home, command string) ([]byte, error) {
	return adapterCommandWithTimeout(ctx, path, home, command, 2*time.Second)
}

func adapterCommandWithTimeout(ctx context.Context, path, home, command string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out := &cappedWriter{limit: 64 * 1024}
	diag := &cappedWriter{limit: 4 * 1024}
	_, e := processWithStderr(ctx, path, []string{command}, childEnv(home, "read"), home, "", out, diag, nil)
	if e != nil && diag.Len() > 0 {
		return out.Bytes(), fmt.Errorf("%w: %s", e, strings.TrimSpace(diag.String()))
	}
	return out.Bytes(), e
}

const AdapterValidate = "validate"

func ValidateAdapters(ctx context.Context, home string, settings Settings) error {
	seen := map[string]bool{}
	for _, name := range settings.Adapters {
		if seen[name] {
			return fmt.Errorf("duplicate adapter %q", name)
		}
		seen[name] = true
		metadata, err := AdapterInspect(ctx, home, name)
		if err != nil {
			return fmt.Errorf("adapter %s: %w", name, err)
		}
		if slices.Contains(metadata.Capabilities, AdapterValidate) {
			if _, err := adapterCommandWithTimeout(ctx, filepath.Join(home, "bin", "agent-"+name), home, "agent-plugin-validate", 30*time.Second); err != nil {
				return fmt.Errorf("adapter %s: %w", name, err)
			}
		}
	}
	return nil
}

// SuperviseAdapters validates every configured adapter before starting any.
func (q *Queue) SuperviseAdapters(ctx context.Context) (func(), error) {
	if err := ValidateAdapters(ctx, q.home, q.settings); err != nil {
		return nil, err
	}
	for _, name := range q.settings.Adapters {
		if name == TimerSource {
			if access, ok := os.LookupEnv("HORIZON_INHERITED_ACCESS"); ok {
				if level(access) == 0 {
					return nil, errors.New("invalid inherited timer adapter access")
				}
				q.timerAccess = access
			}
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	for _, name := range q.settings.Adapters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path, _ := AdapterPath(q.home, name)
			delay := time.Second
			access := "write"
			var output io.Writer = io.Discard
			if name == TimerSource {
				access = q.timerAccess
				output = os.Stderr
			}
			for ctx.Err() == nil {
				credential := newID()
				q.mu.Lock()
				q.adapters[name] = credential
				q.mu.Unlock()
				env := append(childEnv(q.home, access), "HORIZON_DAEMON_SOCKET="+Socket(q.home), "HORIZON_ADAPTER_NAME="+name, "HORIZON_ADAPTER_CREDENTIAL="+credential)
				_, _ = processWithStderr(ctx, path, []string{"agent-run"}, env, q.home, "", output, os.Stderr, nil)
				q.mu.Lock()
				delete(q.adapters, name)
				q.mu.Unlock()
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				delay = min(delay*2, 30*time.Second)
			}
		}()
	}
	return func() { cancel(); wg.Wait() }, nil
}
