package distribution

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func testManager(t *testing.T) (*Manager, *Bundle) {
	t.Helper()
	root := t.TempDir()
	m, err := OpenManager(filepath.Join(root, "home"), filepath.Join(root, "bin", "horizon"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	manifest, _, archive := fixture(t, "v0.1.0")
	b, err := Extract(bytes.NewReader(archive), manifest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	return m, b
}
func TestInstallDependenciesRemovalAndPreservation(t *testing.T) {
	m, b := testManager(t)
	custom := filepath.Join(m.Home, "config.yaml")
	_ = os.WriteFile(custom, []byte("keep me"), 0600)
	s, err := m.Install(context.Background(), b, []string{"telegram"}, "linux_amd64", false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.Installed, []string{"decision", "plugins", "telegram", "update"}) {
		t.Fatal(s.Installed)
	}
	info, _ := os.Stat(s.Executable)
	if _, err = m.Install(context.Background(), b, s.Installed, s.Platform, false); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(s.Executable)
	if !info.ModTime().Equal(after.ModTime()) {
		t.Fatal("reinstalled identical binary")
	}
	if _, err = m.Remove(context.Background(), "decision"); err == nil {
		t.Fatal("removed dependency")
	}
	if _, err = m.Remove(context.Background(), "update"); err == nil {
		t.Fatal("removed required plugin")
	}
	if _, err = m.Remove(context.Background(), "telegram"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(m.Home, "plugins", "horizon-decision")); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(custom)
	if string(data) != "keep me" {
		t.Fatal("changed config")
	}
}
func TestRollbackEveryReplacementAndCrashRecovery(t *testing.T) {
	// First installation has core + 2 mandatory plugins + owner + state.
	for failAt := 0; failAt < 5; failAt++ {
		t.Run(string(rune('a'+failAt)), func(t *testing.T) {
			m, b := testManager(t)
			m.afterReplace = func(i int) error {
				if i == failAt {
					return errors.New("injected failure")
				}
				return nil
			}
			if _, err := m.Install(context.Background(), b, nil, "linux_amd64", false); err == nil {
				t.Fatal("no failure")
			}
			if _, err := os.Stat(m.Executable); !os.IsNotExist(err) {
				t.Fatalf("core remains: %v", err)
			}
			if _, err := os.Stat(StatePath(m.Home)); !os.IsNotExist(err) {
				t.Fatalf("state remains: %v", err)
			}
		})
	}
	m, b := testManager(t)
	s, err := m.Install(context.Background(), b, nil, "linux_amd64", false)
	if err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(s.Executable)
	// New bundle changes every binary; the simulated crash bypasses normal rollback.
	for name := range b.Hashes {
		if name == "release.json" {
			continue
		}
		data := []byte("new " + name)
		_ = os.WriteFile(filepath.Join(b.Dir, name), data, 0700)
		b.Hashes[name] = digest(data)
	}
	m.afterReplace = func(i int) error {
		if i == 1 {
			panic("power loss")
		}
		return nil
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("no crash")
			}
		}()
		_, _ = m.Install(context.Background(), b, s.Installed, s.Platform, false)
	}()
	m.Close()
	reopened, err := OpenManager(m.Home, m.Executable)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, _ := os.ReadFile(s.Executable)
	if !bytes.Equal(restored, original) {
		t.Fatal("failed to restore old core")
	}
	state, err := LoadState(m.Home)
	if err != nil || !reflect.DeepEqual(state.Hashes, s.Hashes) {
		t.Fatalf("state %v %v", state, err)
	}
}
func TestLocksOwnershipAndUnmanagedFiles(t *testing.T) {
	m, b := testManager(t)
	if other, err := OpenManager(m.Home, m.Executable); err == nil {
		other.Close()
		t.Fatal("parallel manager")
	}
	_ = os.WriteFile(m.Executable, []byte("local build"), 0700)
	if _, err := m.Install(context.Background(), b, nil, "linux_amd64", false); err == nil {
		t.Fatal("overwrote unmanaged file")
	}
	if _, err := m.Install(context.Background(), b, nil, "linux_amd64", true); err != nil {
		t.Fatal(err)
	}
	m.Close()
	if other, err := OpenManager(filepath.Join(t.TempDir(), "other"), m.Executable); err == nil {
		other.Close()
		t.Fatal("shared binary across homes")
	}
}

func TestUpdateFailureRestoresEveryOldFile(t *testing.T) {
	for failAt := 0; failAt < 4; failAt++ {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			m, b := testManager(t)
			old, err := m.Install(context.Background(), b, nil, "linux_amd64", false)
			if err != nil {
				t.Fatal(err)
			}
			manifest, _, archive := fixture(t, "v0.2.0")
			next, err := Extract(bytes.NewReader(archive), manifest)
			if err != nil {
				t.Fatal(err)
			}
			defer next.Close()
			for name := range next.Hashes {
				if name == "release.json" {
					continue
				}
				data := []byte("v2-" + name)
				_ = os.WriteFile(filepath.Join(next.Dir, name), data, 0700)
				next.Hashes[name] = digest(data)
			}
			m.afterReplace = func(i int) error {
				if i == failAt {
					return errors.New("replace failed")
				}
				return nil
			}
			if _, err = m.Install(context.Background(), next, old.Installed, old.Platform, false); err == nil {
				t.Fatal("failure not injected")
			}
			restored, err := LoadState(m.Home)
			if err != nil || !SameManifest(restored.Manifest, old.Manifest) {
				t.Fatalf("state %v %v", restored, err)
			}
			for _, name := range append([]string{"horizon"}, old.Binaries()...) {
				if !old.Healthy(name) {
					t.Fatalf("failed to restore %s", name)
				}
			}
		})
	}
}
func TestInstallRejectsSymlinkAndCancellation(t *testing.T) {
	m, b := testManager(t)
	outside := filepath.Join(t.TempDir(), "untouched")
	_ = os.WriteFile(outside, []byte("sentinel"), 0700)
	if err := os.Symlink(outside, m.Executable); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(context.Background(), b, nil, "linux_amd64", true); err == nil {
		t.Fatal("followed symlink")
	}
	data, _ := os.ReadFile(outside)
	if string(data) != "sentinel" {
		t.Fatal("changed symlink target")
	}
	_ = os.Remove(m.Executable)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Install(ctx, b, nil, "linux_amd64", false); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.Executable); !os.IsNotExist(err) {
		t.Fatal("cancelled install wrote executable")
	}
}
