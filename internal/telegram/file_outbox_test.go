package telegram

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileOutboxSnapshotAndReplay(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	store := NewStore(home, 99)
	path := filepath.Join(workspace, "result.txt")
	os.WriteFile(path, []byte("original"), 0600)
	chat := Conversation{ID: 42, Origin: 42, Available: false}
	calls := 0
	sender := func(ctx context.Context, id int64, f *os.File, name, caption string, photo bool) (int64, error) {
		calls++
		b, err := io.ReadAll(f)
		if err != nil || string(b) != "original" || name != "result.txt" || caption != "report" || photo || id != 42 {
			t.Fatal(string(b), err, name, caption, photo, id)
		}
		return 17, nil
	}
	result, err := store.SendFile(context.Background(), sender, chat, "session", "op", workspace, "result.txt", "report", false)
	if err != nil || result.State != "pending" || calls != 0 {
		t.Fatal(result, err, calls)
	}
	os.WriteFile(path, []byte("changed"), 0600)
	os.Remove(path)
	chat.Available = true
	result, err = NewStore(home, 99).SendFile(context.Background(), sender, chat, "session", "op", workspace, "result.txt", "report", false)
	if err != nil || result.State != "confirmed" || calls != 1 || result.File == nil || result.File.Size != 8 {
		t.Fatal(result, err, calls)
	}
	result, err = store.SendFile(context.Background(), sender, chat, "session", "op", workspace, "result.txt", "report", false)
	if err != nil || calls != 1 {
		t.Fatal(result, err, calls)
	}
	if _, err := store.SendFile(context.Background(), sender, chat, "session", "op", workspace, "result.txt", "other", false); err == nil {
		t.Fatal("argument conflict accepted")
	}
	if _, err := store.Send(context.Background(), nil, chat, "session", "op", "text"); err == nil {
		t.Fatal("file/text conflict accepted")
	}
	history, err := store.Confirmed(chat, nil, 5)
	if err != nil || len(history) != 1 || len(history[0].Attachments) != 1 || history[0].Attachments[0].Checksum == "" {
		t.Fatal(history, err)
	}
}
func TestFileOutboxUnknownNeverUploadsTwice(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		store := NewStore(t.TempDir(), 99)
		workspace := t.TempDir()
		os.WriteFile(filepath.Join(workspace, "x"), []byte("file"), 0600)
		chat := Conversation{ID: 42, Origin: 42, Available: true}
		calls := 0
		sender := func(context.Context, int64, *os.File, string, string, bool) (int64, error) {
			calls++
			if rejected {
				return 0, &APIError{Code: 400}
			}
			return 0, errors.New("lost reply")
		}
		result, err := store.SendFile(context.Background(), sender, chat, "session", "op", workspace, "x", "", false)
		want := "unknown"
		if rejected {
			want = "rejected"
		}
		if err != nil || result.State != want {
			t.Fatal(result, err)
		}
		result, err = store.SendFile(context.Background(), sender, chat, "session", "op", workspace, "x", "", false)
		if err != nil || result.State != want || calls != 1 {
			t.Fatal(result, err, calls)
		}
		if _, err := store.ReadSend("op", -42); err == nil {
			t.Fatal("cross-route read")
		}
	}
}
func TestFileOutboxConfirmationWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission failure requires non-root user")
	}
	store := NewStore(t.TempDir(), 99)
	workspace := t.TempDir()
	os.WriteFile(filepath.Join(workspace, "x"), []byte("file"), 0600)
	path, _ := store.operationPath("op")
	dir := filepath.Dir(path)
	sender := func(context.Context, int64, *os.File, string, string, bool) (int64, error) {
		if err := os.Chmod(dir, 0500); err != nil {
			t.Fatal(err)
		}
		return 17, nil
	}
	_, err := store.SendFile(context.Background(), sender, Conversation{ID: 42, Origin: 42, Available: true}, "session", "op", workspace, "x", "", false)
	os.Chmod(dir, 0700)
	if err == nil {
		t.Fatal("confirmation write error ignored")
	}
	result, err := store.ReadSend("op", 42)
	if err != nil || result.State != "unknown" {
		t.Fatal(result, err)
	}
}
func TestUploadWorkspaceBoundary(t *testing.T) {
	workspace, outside := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret"), []byte("outside"), 0600)
	os.Symlink(outside, filepath.Join(workspace, "escape"))
	for _, name := range []string{filepath.Join(outside, "secret"), "../secret", "escape/secret"} {
		if f, err := openUpload(workspace, name); err == nil {
			f.Close()
			t.Fatal("escaped", name)
		}
	}
	os.WriteFile(filepath.Join(workspace, "good"), []byte("good"), 0600)
	os.Symlink("good", filepath.Join(workspace, "alias"))
	f, err := openUpload(workspace, "alias")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, _ := io.ReadAll(f)
	if string(b) != "good" {
		t.Fatal(string(b))
	}
	if _, err := openUpload(workspace, "."); err == nil {
		t.Fatal("directory accepted")
	}
	// A corrupted pending snapshot cannot be sent as a replacement payload.
	store := NewStore(t.TempDir(), 99)
	chat := Conversation{ID: 42, Origin: 42}
	_, err = store.SendFile(context.Background(), nil, chat, "s", "op", workspace, "good", "", false)
	if err != nil {
		t.Fatal(err)
	}
	path, _ := store.operationPath("op")
	var saved delivery
	if err := loadJSON(path, &saved); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(filepath.Dir(path), saved.Snapshot)
	os.Chmod(snapshot, 0600)
	os.WriteFile(snapshot, []byte("evil"), 0600)
	chat.Available = true
	_, err = store.SendFile(context.Background(), func(context.Context, int64, *os.File, string, string, bool) (int64, error) {
		t.Fatal("sent changed snapshot")
		return 0, nil
	}, chat, "s", "op", workspace, "good", "", false)
	if err == nil || !strings.Contains(err.Error(), "snapshot changed") {
		t.Fatal(err)
	}
}
