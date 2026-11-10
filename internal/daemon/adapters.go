package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

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
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out := &cappedWriter{limit: 64 * 1024}
	_, e := Process(ctx, path, []string{command}, childEnv(home, "read"), home, "", out)
	return out.Bytes(), e
}

// SuperviseAdapters validates every configured adapter before starting any.
func (q *Queue) SuperviseAdapters(ctx context.Context) (func(), error) {
	seen := map[string]bool{}
	for _, name := range q.settings.Adapters {
		if seen[name] {
			return nil, errors.New("duplicate adapter")
		}
		seen[name] = true
		if _, e := AdapterInspect(ctx, q.home, name); e != nil {
			return nil, e
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
			for ctx.Err() == nil {
				credential := newID()
				q.mu.Lock()
				q.adapters[name] = credential
				q.mu.Unlock()
				env := append(childEnv(q.home, "write"), "HORIZON_DAEMON_SOCKET="+Socket(q.home), "HORIZON_ADAPTER_NAME="+name, "HORIZON_ADAPTER_CREDENTIAL="+credential)
				_, _ = Process(ctx, path, []string{"agent-run"}, env, q.home, "", io.Discard)
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
