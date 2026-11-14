package telegram

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func pendingAttachment(t *testing.T) (Store, *PendingInput) {
	t.Helper()
	s := NewStore(t.TempDir(), 7)
	p := &PendingInput{UpdateID: 10, Origin: 42, ChatID: 42, Workspace: t.TempDir(), Message: &Entry{ID: 20, Attachments: []Attachment{{Kind: "document", FileID: "f", UniqueID: "u", Name: "../../report.txt", State: "pending"}}}}
	if err := s.Update(func(r *Registry) error { r.Pending = p; return nil }); err != nil {
		t.Fatal(err)
	}
	return s, p
}
func TestAttachmentPersistsAndRecovers(t *testing.T) {
	s, p := pendingAttachment(t)
	calls := 0
	fetch := func(ctx context.Context, id string, w io.Writer) (int64, error) {
		calls++
		if err := s.Update(func(r *Registry) error { return nil }); err != nil {
			t.Fatal("store lock held during fetch", err)
		}
		return io.Copy(w, bytes.NewBufferString("content"))
	}
	if err := s.PrepareAttachments(context.Background(), fetch); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Snapshot()
	a := r.Pending.Message.Attachments[0]
	if a.State != "ready" || !inside(p.Workspace, a.Path) || a.Checksum == "" || a.Size != 7 {
		t.Fatal(a)
	}
	info, err := os.Stat(a.Path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	if err := s.PrepareAttachments(context.Background(), fetch); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
	// Simulate crash after final publication but before ready was journaled.
	a.State = "staged"
	a.Path = ""
	if err := s.saveAttachment(p.UpdateID, 0, a); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareAttachments(context.Background(), fetch); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
	// A changed published file is reported, never silently replaced.
	if err := os.WriteFile(filepath.Join(p.Workspace, a.Local), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareAttachments(context.Background(), fetch); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Snapshot()
	if r.Pending.Message.Attachments[0].State != "failed" || r.Pending.Message.Attachments[0].Path != "" {
		t.Fatal(r.Pending)
	}
}
func TestAttachmentRecoveryBeforePublication(t *testing.T) {
	s, p := pendingAttachment(t)
	a := p.Message.Attachments[0]
	a.Local, a.Temp = attachmentPaths(p, a, 0)
	root, err := attachmentRoot(p.Workspace, a.Local)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.WriteFile(a.Temp, []byte("ready data"), 0600); err != nil {
		t.Fatal(err)
	}
	a.Checksum, a.Size, err = checksumFile(root, a.Temp, DownloadLimit)
	if err != nil {
		t.Fatal(err)
	}
	a.State = "staged"
	if err := s.saveAttachment(p.UpdateID, 0, a); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareAttachments(context.Background(), func(context.Context, string, io.Writer) (int64, error) { t.Fatal("redownload"); return 0, nil }); err != nil {
		t.Fatal(err)
	}
	data, err := root.ReadFile(a.Local)
	if err != nil || string(data) != "ready data" {
		t.Fatal(string(data), err)
	}
}
func TestAttachmentFailureRetryAndCancellation(t *testing.T) {
	s, p := pendingAttachment(t)
	calls := 0
	fail := func(context.Context, string, io.Writer) (int64, error) {
		calls++
		return 0, errors.New("disk/network failure")
	}
	if err := s.PrepareAttachments(context.Background(), fail); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Snapshot()
	if calls != 3 || r.Pending.Message.Attachments[0].State != "failed" {
		t.Fatal(calls, r.Pending)
	}
	s, p = pendingAttachment(t)
	ctx, cancel := context.WithCancel(context.Background())
	if err := s.PrepareAttachments(ctx, func(_ context.Context, _ string, w io.Writer) (int64, error) { cancel(); return 0, ctx.Err() }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := s.PrepareAttachments(context.Background(), func(_ context.Context, _ string, w io.Writer) (int64, error) {
		return io.Copy(w, bytes.NewBufferString("retry"))
	}); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Snapshot()
	if r.Pending.UpdateID != p.UpdateID || r.Pending.Message.Attachments[0].State != "ready" {
		t.Fatal(r.Pending)
	}
}
func TestAttachmentRejectsDestinationAndSymlink(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		s, p := pendingAttachment(t)
		a := p.Message.Attachments[0]
		local, _ := attachmentPaths(p, a, 0)
		if symlink {
			if err := os.Symlink(t.TempDir(), filepath.Join(p.Workspace, "attachments")); err != nil {
				t.Fatal(err)
			}
		} else {
			root, err := attachmentRoot(p.Workspace, local)
			if err != nil {
				t.Fatal(err)
			}
			if err := root.WriteFile(local, []byte("user file"), 0600); err != nil {
				t.Fatal(err)
			}
			root.Close()
		}
		if err := s.PrepareAttachments(context.Background(), func(context.Context, string, io.Writer) (int64, error) { t.Fatal("unexpected fetch"); return 0, nil }); err != nil {
			t.Fatal(err)
		}
		r, _ := s.Snapshot()
		if r.Pending.Message.Attachments[0].State != "failed" {
			t.Fatal(r.Pending)
		}
	}
}
func TestLargestPhotoAndPublicMetadata(t *testing.T) {
	items := MessageAttachments(&Message{Photos: []PhotoSize{{File: File{ID: "small"}, Width: 10, Height: 10}, {File: File{ID: "big"}, Width: 20, Height: 20}}})
	if len(items) != 1 || items[0].FileID != "big" {
		t.Fatal(items)
	}
	items[0].Temp = "private"
	items[0].Local = "private"
	items[0].Path = "not ready"
	public := PublicAttachments(items)
	if public[0].Temp != "" || public[0].Local != "" || public[0].Path != "" {
		t.Fatal(public)
	}
}
