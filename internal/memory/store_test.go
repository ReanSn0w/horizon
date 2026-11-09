package memory

import (
	"context"
	"github.com/ReanSn0w/horizon/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testSettings(t *testing.T) settings {
	t.Helper()
	s, err := loadSettings(config.Config{DefaultModel: "test", Models: map[string]config.Model{"test": {Model: "model", CompactThreshold: 1000}}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestStoreIsolationDedupAndPermissions(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	m := newStore(home, t.TempDir(), testSettings(t))
	s, err := m.read("user")
	if err != nil || len(s.Pending) != 0 {
		t.Fatal(s, err)
	}
	if _, err := os.Stat(filepath.Join(home, "memory")); !os.IsNotExist(err) {
		t.Fatal("read created memory")
	}
	if _, _, err := m.add(ctx, "user", "prefers concise replies", "id"); err != nil {
		t.Fatal(err)
	}
	other := newStore(home, t.TempDir(), testSettings(t))
	shared, err := other.read("user")
	if err != nil || len(shared.Pending) != 1 {
		t.Fatal(shared, err)
	}
	_, duplicate, err := other.add(ctx, "user", "prefers concise replies", "id")
	if err != nil || !duplicate {
		t.Fatal(duplicate, err)
	}
	if _, _, err := m.add(ctx, "user", "different", "id"); err == nil {
		t.Fatal("conflict accepted")
	}
	if _, _, err := m.add(ctx, "workspace", "local fact", "local"); err != nil {
		t.Fatal(err)
	}
	s, err = other.read("workspace")
	if err != nil || len(s.Pending) != 0 {
		t.Fatal("workspace leaked", s, err)
	}
	another := newStore(t.TempDir(), m.workspace, testSettings(t))
	s, err = another.read("user")
	if err != nil || len(s.Pending) != 0 {
		t.Fatal("home leaked", s, err)
	}
	for _, p := range []string{filepath.Join(home, "memory"), filepath.Join(home, "memory", "user.json"), filepath.Join(home, "memory", "user.json.lock")} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0600)
		if info.IsDir() {
			want = 0700
		}
		if info.Mode().Perm() != want {
			t.Fatal(p, info.Mode())
		}
	}
}
func TestStoreCorruptionAndBounds(t *testing.T) {
	m := newStore(t.TempDir(), t.TempDir(), testSettings(t))
	m.settings.MaxPendingNotes = 1
	ctx := context.Background()
	if _, _, err := m.add(ctx, "agent", "one", "one"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.add(ctx, "agent", "two", "two"); err == nil {
		t.Fatal("overflow accepted")
	}
	s, err := m.read("agent")
	if err != nil || len(s.Pending) != 1 {
		t.Fatal(s, err)
	}
	path, _ := m.path("agent")
	if err := os.WriteFile(path, []byte(`{"format_version":2}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.read("agent"); err == nil {
		t.Fatal("corruption became empty memory")
	}
}
func TestStateLockCancellation(t *testing.T) {
	m := newStore(t.TempDir(), t.TempDir(), testSettings(t))
	path, _ := m.path("agent")
	held, err := lockFile(context.Background(), path+".lock", false)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, _, err = m.add(ctx, "agent", "blocked", "id")
	if err == nil {
		t.Fatal("blocked write ran")
	}
}

func TestLegacyPluginStateContinuesWithoutMigration(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "memory")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	legacy := `{"format_version":1,"scope":"user","revision":3,"generation":"` + strings.Repeat("a", 32) + `","summary":"retained summary","pending":[{"id":"old","at":"` + at + `","text":"retained note"}],"operations":[{"id":"old","digest":"` + strings.Repeat("0", 64) + `","at":"` + at + `"}]}`
	path := filepath.Join(dir, "user.json")
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	store := newStore(home, t.TempDir(), testSettings(t))
	before, err := store.read("user")
	if err != nil || before.Summary != "retained summary" || len(before.Pending) != 1 {
		t.Fatalf("before=%+v error=%v", before, err)
	}
	if _, _, err := store.add(context.Background(), "user", "new note", "new"); err != nil {
		t.Fatal(err)
	}
	after, err := store.read("user")
	if err != nil || after.Summary != before.Summary || len(after.Pending) != 2 || len(after.Operations) != 2 {
		t.Fatalf("after=%+v error=%v", after, err)
	}
}
