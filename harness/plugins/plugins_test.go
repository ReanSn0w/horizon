package plugins

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func candidate(t *testing.T, home, name, script string) string {
	t.Helper()
	dir := filepath.Join(home, "plugins")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "horizon-"+name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDiscoverAndInspect(t *testing.T) {
	home := t.TempDir()
	if got, err := Discover(home, nil); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	good := candidate(t, home, "good", "printf '%s' '{\"protocol_version\":1,\"version\":\"1.2\",\"description\":\"Useful\",\"extra\":true}'\n")
	if err := os.Symlink(good, filepath.Join(home, "plugins", "horizon-link")); err != nil {
		t.Fatal(err)
	}
	candidate(t, home, "bad-json", "echo nope\n")
	candidate(t, home, "bad-version", "echo '{\"protocol_version\":2,\"version\":\"1\",\"description\":\"x\"}'\n")
	candidate(t, home, "extra", "echo '{\"protocol_version\":1,\"version\":\"1\",\"description\":\"x\"} trailing'\n")
	candidate(t, home, "slow", "sleep 3\n")
	candidate(t, home, "huge", "yes a | head -c 70000\n")
	candidate(t, home, "resume", "echo forbidden\n")
	candidate(t, home, "UPPER", "echo forbidden\n")
	if err := os.Mkdir(filepath.Join(home, "plugins", "horizon-dir"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "plugins", "horizon-noexec"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := Discover(home, []string{"resume"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 8 {
		t.Fatalf("candidates = %+v", got)
	}
	for _, item := range got {
		switch item.Name {
		case "good", "link":
			if item.Err != nil || item.Metadata.Version != "1.2" {
				t.Errorf("%s: %+v", item.Name, item)
			}
		default:
			if item.Err == nil {
				t.Errorf("%s unexpectedly valid", item.Name)
			}
		}
	}
}

func TestMetadataLimits(t *testing.T) {
	home := t.TempDir()
	candidate(t, home, "slow", "sleep 3\n")
	start := time.Now()
	item := Inspect(home, "slow", nil)
	if item.Err == nil || time.Since(start) > 4*time.Second {
		t.Fatalf("timeout: %+v, elapsed %v", item, time.Since(start))
	}
	candidate(t, home, "stderr", "printf '%70000s' x >&2\n")
	if item := Inspect(home, "stderr", nil); item.Err == nil || !strings.Contains(item.Err.Error(), "limit") {
		t.Fatalf("stderr: %+v", item)
	}
	candidate(t, home, "stdout", "printf '%70000s' x\n")
	if item := Inspect(home, "stdout", nil); item.Err == nil || !strings.Contains(item.Err.Error(), "limit") {
		t.Fatalf("stdout: %+v", item)
	}
}

func TestMetadataTimeoutKillsChildHoldingOutputPipe(t *testing.T) {
	home := t.TempDir()
	ready := filepath.Join(home, "child.ready")
	marker := filepath.Join(home, "child.marker")
	path := candidate(t, home, "child", "( echo ready > '"+ready+"'; sleep 2; echo survived > '"+marker+"' ) &\nwait\n")
	// Allow process startup under parallel builds; still expire before the child writes.
	start := time.Now()
	_, err := readMetadata(path, home, time.Second)
	if !errors.Is(err, ErrMetadataTimeout) {
		t.Fatalf("expected metadata timeout, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("metadata wait held by child pipe for %s", elapsed)
	}
	if _, err := os.Stat(ready); err != nil {
		t.Fatalf("child was not started: %v", err)
	}
	time.Sleep(2100 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("managed metadata child survived timeout")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestDiscoveryDirectoryError(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "plugins"), []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(home, nil); err == nil {
		t.Fatal("expected directory read error")
	}
}
