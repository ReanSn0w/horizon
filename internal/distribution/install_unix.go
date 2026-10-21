//go:build unix

package distribution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// Manager holds permanent OS locks throughout recovery and mutation.
type Manager struct {
	Home       string
	Executable string
	locks      []*os.File
	// afterReplace injects a failure/crash at durable boundaries in tests.
	afterReplace func(int) error
}
type operation struct {
	Target  string `json:"target"`
	Backup  string `json:"backup,omitempty"`
	Next    string `json:"next,omitempty"`
	Existed bool   `json:"existed"`
	Mode    uint32 `json:"mode"`
}
type journal struct {
	SchemaVersion int         `json:"schema_version"`
	Operations    []operation `json:"operations"`
}

func canonicalDirectory(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(abs, 0700); err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}
func OpenManager(home, executable string) (*Manager, error) {
	home, err := canonicalDirectory(home)
	if err != nil {
		return nil, err
	}
	if filepath.Base(executable) != "horizon" {
		return nil, fmt.Errorf("executable must be named horizon")
	}
	bin, err := canonicalDirectory(filepath.Dir(executable))
	if err != nil {
		return nil, err
	}
	m := &Manager{Home: home, Executable: filepath.Join(bin, "horizon")}
	success := false
	defer func() {
		if !success {
			m.Close()
		}
	}()
	for _, dir := range []string{filepath.Join(home, stateDirectory), filepath.Join(home, "plugins")} {
		if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
			return nil, err
		}
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("installation directory must be a real directory: %s", dir)
		}
	}
	for _, path := range []string{filepath.Join(home, stateDirectory, "install.lock"), m.Executable + ".lock"} {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return nil, err
		}
		if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			f.Close()
			return nil, fmt.Errorf("installation is busy: %w", err)
		}
		m.locks = append(m.locks, f)
	}
	// A binary path is owned by one home; check before recovering home-local state.
	if data, err := os.ReadFile(m.ownerPath()); err == nil {
		var owner string
		if json.Unmarshal(data, &owner) != nil || owner != home {
			return nil, fmt.Errorf("executable belongs to another home: %s", m.Executable)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err = m.recover(); err != nil {
		return nil, fmt.Errorf("recover interrupted installation: %w", err)
	}
	success = true
	return m, nil
}
func (m *Manager) Close() {
	for i := len(m.locks) - 1; i >= 0; i-- {
		_ = syscall.Flock(int(m.locks[i].Fd()), syscall.LOCK_UN)
		_ = m.locks[i].Close()
	}
	m.locks = nil
}
func (m *Manager) ownerPath() string { return m.Executable + ".installation.json" }
func (m *Manager) journalPath() string {
	return filepath.Join(m.Home, stateDirectory, "transaction.json")
}
func (m *Manager) state() (State, error) {
	s, err := LoadState(m.Home)
	if err == nil && s.Executable != m.Executable {
		return s, fmt.Errorf("home belongs to executable %s", s.Executable)
	}
	return s, err
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func stage(path, prefix string, data []byte, mode os.FileMode) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(path), prefix)
	if err != nil {
		return "", err
	}
	name := f.Name()
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(name)
		}
	}()
	if err = f.Chmod(mode); err != nil {
		return "", err
	}
	if _, err = f.Write(data); err != nil {
		return "", err
	}
	if err = f.Sync(); err != nil {
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	ok = true
	return name, nil
}
func atomicFile(path string, data []byte, mode os.FileMode) error {
	name, err := stage(path, ".horizon-next-", data, mode)
	if err != nil {
		return err
	}
	defer os.Remove(name)
	if err = os.Rename(name, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

type replacement struct {
	path   string
	data   []byte
	mode   os.FileMode
	remove bool
}

func (m *Manager) allowedTarget(path string) bool {
	if path == m.Executable || path == m.ownerPath() || path == StatePath(m.Home) {
		return true
	}
	return filepath.Dir(path) == filepath.Join(m.Home, "plugins") && strings.HasPrefix(filepath.Base(path), "horizon-") && validComponent(strings.TrimPrefix(filepath.Base(path), "horizon-"))
}
func validComponent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if !(r >= 'a' && r <= 'z') && !(i > 0 && (r >= '0' && r <= '9' || r == '-')) {
			return false
		}
	}
	return true
}
func (m *Manager) validateJournal(j journal) error {
	if j.SchemaVersion != 1 || len(j.Operations) == 0 {
		return fmt.Errorf("invalid transaction journal")
	}
	seen := map[string]bool{}
	for _, op := range j.Operations {
		if !m.allowedTarget(op.Target) || seen[op.Target] {
			return fmt.Errorf("invalid journal target %s", op.Target)
		}
		seen[op.Target] = true
		for _, temp := range []struct{ path, prefix string }{{op.Backup, ".horizon-backup-"}, {op.Next, ".horizon-next-"}} {
			if temp.path != "" && (filepath.Dir(temp.path) != filepath.Dir(op.Target) || !strings.HasPrefix(filepath.Base(temp.path), temp.prefix)) {
				return fmt.Errorf("invalid transaction temporary path")
			}
		}
		if op.Existed != (op.Backup != "") {
			return fmt.Errorf("invalid backup record")
		}
	}
	return nil
}
func cleanup(j journal) {
	for _, op := range j.Operations {
		if op.Backup != "" {
			_ = os.Remove(op.Backup)
		}
		if op.Next != "" {
			_ = os.Remove(op.Next)
		}
	}
}
func (m *Manager) recover() error {
	data, err := os.ReadFile(m.journalPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var j journal
	if err = json.Unmarshal(data, &j); err != nil {
		return err
	}
	if err = m.validateJournal(j); err != nil {
		return err
	}
	// Backups are copied, not consumed, so recovery can itself be interrupted.
	for i := len(j.Operations) - 1; i >= 0; i-- {
		op := j.Operations[i]
		if op.Existed {
			info, err := os.Lstat(op.Backup)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("invalid backup %s", op.Backup)
			}
			data, err := os.ReadFile(op.Backup)
			if err != nil {
				return err
			}
			if err = atomicFile(op.Target, data, os.FileMode(op.Mode)); err != nil {
				return err
			}
		} else {
			if err := os.Remove(op.Target); err != nil && !os.IsNotExist(err) {
				return err
			}
			if err := syncDir(filepath.Dir(op.Target)); err != nil {
				return err
			}
		}
	}
	if err = os.Remove(m.journalPath()); err != nil {
		return err
	}
	if err = syncDir(filepath.Dir(m.journalPath())); err != nil {
		return err
	}
	cleanup(j)
	return nil
}
func (m *Manager) apply(ctx context.Context, changes []replacement) error {
	if len(changes) == 0 {
		return nil
	}
	j := journal{SchemaVersion: 1}
	journalWritten := false
	defer func() {
		if !journalWritten {
			cleanup(j)
		}
	}()
	for _, change := range changes {
		if !m.allowedTarget(change.path) {
			return fmt.Errorf("invalid installation destination %s", change.path)
		}
		op := operation{Target: change.path}
		info, err := os.Lstat(change.path)
		if err == nil {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("refusing non-regular destination %s", change.path)
			}
			op.Existed = true
			op.Mode = uint32(info.Mode().Perm())
			data, err := os.ReadFile(change.path)
			if err != nil {
				return err
			}
			op.Backup, err = stage(change.path, ".horizon-backup-", data, info.Mode().Perm())
			if err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		j.Operations = append(j.Operations, op)
		if !change.remove {
			next, err := stage(change.path, ".horizon-next-", change.data, change.mode)
			if err != nil {
				return err
			}
			j.Operations[len(j.Operations)-1].Next = next
		}
		if err := syncDir(filepath.Dir(change.path)); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	// Once rename has succeeded, keep backups even if directory fsync fails.
	journalWritten = true
	if err = atomicFile(m.journalPath(), data, 0600); err != nil {
		return fmt.Errorf("save transaction journal (rerun installer to recover): %w", err)
	}
	fail := func(cause error) error {
		if err := m.recover(); err != nil {
			return fmt.Errorf("%v; recovery failed: %w; rerun install.sh", cause, err)
		}
		return cause
	}
	for i, op := range j.Operations {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if op.Next != "" {
			err = os.Rename(op.Next, op.Target)
		} else {
			err = os.Remove(op.Target)
			if os.IsNotExist(err) {
				err = nil
			}
		}
		if err != nil {
			return fail(err)
		}
		if err = syncDir(filepath.Dir(op.Target)); err != nil {
			return fail(err)
		}
		if m.afterReplace != nil {
			if err = m.afterReplace(i); err != nil {
				return fail(err)
			}
		}
	}
	// The durable journal removal is the commit point. Leftover backups are harmless.
	if err = os.Remove(m.journalPath()); err != nil {
		return fail(err)
	}
	if err = syncDir(filepath.Dir(m.journalPath())); err != nil {
		return fmt.Errorf("commit durability uncertain; retain backups and rerun installer: %w", err)
	}
	cleanup(j)
	return nil
}

// Install validates the complete bundle before touching any installed file.
func (m *Manager) Install(ctx context.Context, b *Bundle, ids []string, platform string, adopt bool) (State, error) {
	var zero State
	if err := b.Manifest.Validate(); err != nil {
		return zero, err
	}
	if !slices.Contains(Platforms, platform) {
		return zero, fmt.Errorf("unsupported platform %s", platform)
	}
	old, err := m.state()
	hasOld := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return zero, err
	}
	if hasOld {
		if err := NoDowngrade(old.Manifest.Tag, b.Manifest.Tag); err != nil {
			return zero, err
		}
		if old.Manifest.Tag == b.Manifest.Tag && !SameManifest(old.Manifest, b.Manifest) {
			return zero, fmt.Errorf("manifest changed for installed tag")
		}
	}
	if hasOld && old.Platform != platform {
		return zero, fmt.Errorf("installation platform mismatch")
	}
	selected := slices.Clone(ids)
	if adopt && !hasOld {
		for _, p := range b.Manifest.Catalog.Plugins {
			if _, err := os.Lstat(filepath.Join(m.Home, "plugins", p.Binary)); err == nil {
				selected = append(selected, p.ID)
			} else if !os.IsNotExist(err) {
				return zero, err
			}
		}
	}
	selected = append(selected, b.Manifest.Catalog.Required()...)
	selected, err = b.Manifest.Catalog.Closure(selected)
	if err != nil {
		return zero, err
	}
	slices.Sort(selected)
	if hasOld {
		for _, id := range old.Installed {
			if !slices.Contains(selected, id) {
				return zero, fmt.Errorf("update would remove %s; remove it explicitly first", id)
			}
		}
	}
	s := State{SchemaVersion: 1, Manifest: b.Manifest, Platform: platform, Home: m.Home, Executable: m.Executable, Installed: selected, Hashes: map[string]string{}}
	var changes []replacement
	for _, binary := range append([]string{"horizon"}, s.Binaries()...) {
		source := filepath.Join(b.Dir, binary)
		info, err := os.Lstat(source)
		if err != nil {
			return zero, err
		}
		if !info.Mode().IsRegular() || info.Size() > maxExpanded {
			return zero, fmt.Errorf("invalid bundle file %s", source)
		}
		data, err := os.ReadFile(source)
		if err != nil {
			return zero, err
		}
		hash := digest(data)
		if hash != b.Hashes[binary] {
			return zero, fmt.Errorf("bundle changed: %s", binary)
		}
		s.Hashes[binary] = hash
		target := s.Target(binary)
		info, err = os.Lstat(target)
		if err == nil {
			if !info.Mode().IsRegular() {
				return zero, fmt.Errorf("refusing symlink or non-regular target %s", target)
			}
			owned := hasOld && (binary == "horizon" || slices.Contains(old.Binaries(), binary))
			if !owned && !adopt {
				return zero, fmt.Errorf("unmanaged file %s; use install.sh --adopt explicitly", target)
			}
			current, err := FileDigest(target)
			if err != nil {
				return zero, err
			}
			if current == hash && info.Mode().Perm() == 0700 {
				continue
			}
		} else if !os.IsNotExist(err) {
			return zero, err
		}
		changes = append(changes, replacement{path: target, data: data, mode: 0700})
	}
	owner, _ := json.Marshal(m.Home)
	state, _ := json.MarshalIndent(s, "", "  ")
	for _, r := range []replacement{{path: m.ownerPath(), data: owner, mode: 0600}, {path: StatePath(m.Home), data: state, mode: 0600}} {
		existing, err := os.ReadFile(r.path)
		if err == nil && string(existing) == string(r.data) {
			continue
		}
		changes = append(changes, r)
	}
	if err = s.Validate(m.Home); err != nil {
		return zero, err
	}
	if err = m.apply(ctx, changes); err != nil {
		return zero, err
	}
	return s, nil
}
func (m *Manager) Remove(ctx context.Context, id string) (State, error) {
	s, err := m.state()
	if err != nil {
		return s, err
	}
	p, ok := s.Manifest.Catalog.Find(id)
	if !ok {
		return s, fmt.Errorf("unknown plugin %q", id)
	}
	if p.Required {
		return s, fmt.Errorf("%s is required by the installation", id)
	}
	if !slices.Contains(s.Installed, id) {
		return s, nil
	}
	var dependents []string
	for _, other := range s.Installed {
		if other == id {
			continue
		}
		closure, _ := s.Manifest.Catalog.Closure([]string{other})
		if slices.Contains(closure, id) {
			dependents = append(dependents, other)
		}
	}
	if len(dependents) > 0 {
		return s, fmt.Errorf("%s is required by: %s", id, strings.Join(dependents, ", "))
	}
	s.Installed = slices.DeleteFunc(s.Installed, func(v string) bool { return v == id })
	delete(s.Hashes, p.Binary)
	data, _ := json.MarshalIndent(s, "", "  ")
	err = m.apply(ctx, []replacement{{path: s.Target(p.Binary), remove: true}, {path: StatePath(m.Home), data: data, mode: 0600}})
	return s, err
}
