// Package plugins discovers installed executable commands and validates their protocol.
package plugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
)

const MetadataTimeout = 2 * time.Second
const OutputLimit = 64 * 1024

var ErrMetadataTimeout = errors.New("metadata command timed out")

var validName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

type Metadata struct {
	ProtocolVersion int    `json:"protocol_version"`
	Version         string `json:"version"`
	Description     string `json:"description"`
}

type Entry struct {
	Name     string
	Path     string
	Metadata Metadata
	Err      error
}

func directory(home string) string { return filepath.Join(home, "plugins") }

// Candidates returns installed executable paths without starting them.
func Candidates(home string, reserved []string) ([]Entry, error) {
	entries, err := os.ReadDir(directory(home))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read plugins directory: %w", err)
	}
	result := make([]Entry, 0, len(entries))
	for _, item := range entries {
		if !strings.HasPrefix(item.Name(), "horizon-") {
			continue
		}
		name := strings.TrimPrefix(item.Name(), "horizon-")
		if !validName.MatchString(name) {
			continue
		}
		path := filepath.Join(directory(home), item.Name())
		info, err := os.Stat(path)
		if err != nil {
			result = append(result, Entry{Name: name, Path: path, Err: err})
			continue
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			continue
		}
		entry := Entry{Name: name, Path: path}
		if slices.Contains(reserved, name) {
			entry.Err = fmt.Errorf("name %q conflicts with a built-in command", name)
		}
		result = append(result, entry)
	}
	return result, nil
}

func Inspect(home, name string, reserved []string) Entry {
	return InspectTimeout(home, name, reserved, MetadataTimeout)
}

func InspectTimeout(home, name string, reserved []string, timeout time.Duration) Entry {
	entry := Entry{Name: name, Path: filepath.Join(directory(home), "horizon-"+name)}
	if !validName.MatchString(name) {
		entry.Err = fmt.Errorf("invalid plugin name %q", name)
		return entry
	}
	if slices.Contains(reserved, name) {
		entry.Err = fmt.Errorf("name %q conflicts with a built-in command", name)
		return entry
	}
	info, err := os.Stat(entry.Path)
	if err != nil {
		entry.Err = err
		return entry
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		entry.Err = fmt.Errorf("plugin %q is not an executable file", name)
		return entry
	}
	entry.Metadata, entry.Err = readMetadata(entry.Path, home, timeout)
	return entry
}

func Discover(home string, reserved []string) ([]Entry, error) {
	entries, err := Candidates(home, reserved)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if entries[i].Err == nil {
			entries[i].Metadata, entries[i].Err = readMetadata(entries[i].Path, home, MetadataTimeout)
		}
	}
	return entries, nil
}

type cappedBuffer struct {
	buffer   bytes.Buffer
	exceeded chan struct{}
	once     bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > OutputLimit {
		if !b.once {
			b.once = true
			close(b.exceeded)
		}
		return 0, fmt.Errorf("metadata output exceeds %d byte limit", OutputLimit)
	}
	return b.buffer.Write(p)
}

func readMetadata(path, home string, timeout time.Duration) (Metadata, error) {
	cmd := exec.Command(path, "horizon-plugin-metadata")
	cmd.Stdin = nil
	executable, err := os.Executable()
	if err != nil {
		return Metadata{}, fmt.Errorf("locate Horizon executable: %w", err)
	}
	cmd.Env = Environment(os.Environ(), home, executable)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout := &cappedBuffer{exceeded: make(chan struct{})}
	stderr := &cappedBuffer{exceeded: make(chan struct{})}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return Metadata{}, fmt.Errorf("start metadata command: %w", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err = <-waited:
	case <-timer.C:
		killGroup(cmd)
		<-waited
		return Metadata{}, fmt.Errorf("%w after %s", ErrMetadataTimeout, timeout)
	case <-stdout.exceeded:
		killGroup(cmd)
		<-waited
		return Metadata{}, fmt.Errorf("metadata stdout exceeds %d byte limit", OutputLimit)
	case <-stderr.exceeded:
		killGroup(cmd)
		<-waited
		return Metadata{}, fmt.Errorf("metadata stderr exceeds %d byte limit", OutputLimit)
	}
	if stdout.once || stderr.once {
		return Metadata{}, fmt.Errorf("metadata output exceeds %d byte limit", OutputLimit)
	}
	if err != nil {
		return Metadata{}, fmt.Errorf("metadata command failed: %w: %s", err, stderr.buffer.String())
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout.buffer.Bytes()))
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return Metadata{}, fmt.Errorf("invalid metadata JSON: %w", err)
	}
	if raw == nil {
		return Metadata{}, fmt.Errorf("metadata must be a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Metadata{}, fmt.Errorf("extra metadata output")
	}
	var metadata Metadata
	if err := json.Unmarshal(stdout.buffer.Bytes(), &metadata); err != nil {
		return Metadata{}, fmt.Errorf("invalid metadata fields: %w", err)
	}
	if metadata.ProtocolVersion != 1 {
		return Metadata{}, fmt.Errorf("unsupported protocol version %d", metadata.ProtocolVersion)
	}
	if strings.TrimSpace(metadata.Version) == "" || strings.TrimSpace(metadata.Description) == "" {
		return Metadata{}, fmt.Errorf("metadata version and description must be nonempty")
	}
	return metadata, nil
}

func killGroup(cmd *exec.Cmd) { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }

// Environment replaces supported Horizon values without duplicating them.
func Environment(base []string, home, executable string) []string {
	values := map[string]string{"HORIZON_HOME": home, "HORIZON_EXECUTABLE": executable}
	result := make([]string, 0, len(base)+2)
	for _, item := range base {
		name, _, ok := strings.Cut(item, "=")
		if ok {
			if _, replace := values[name]; replace {
				continue
			}
		}
		result = append(result, item)
	}
	return append(result, "HORIZON_HOME="+home, "HORIZON_EXECUTABLE="+executable)
}
