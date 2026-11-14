package telegram

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ReanSn0w/horizon/internal/maintenance"
)

type SentFile struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Checksum string `json:"sha256"`
	AsPhoto  bool   `json:"as_photo"`
}
type FileSender func(context.Context, int64, *os.File, string, string, bool) (int64, error)

func uploadPath(workspace, name string) (string, string, error) {
	if strings.TrimSpace(name) == "" {
		return "", "", errors.New("upload path is required")
	}
	if !filepath.IsAbs(workspace) {
		return "", "", errors.New("workspace must be absolute")
	}
	absolute := name
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(workspace, absolute)
	}
	absolute = filepath.Clean(absolute)
	rel, err := filepath.Rel(workspace, absolute)
	if err != nil || !filepath.IsLocal(rel) || rel == "." {
		return "", "", errors.New("upload must be inside the current workspace")
	}
	return absolute, rel, nil
}

// Open inside an anchored root so a concurrently changed symlink cannot escape
// workspace. Nonblocking/no-follow final open also refuses FIFOs and devices
// before reading any bytes. Relative symlinks inside the root are resolved first.
func openUpload(workspace, name string) (*os.File, error) {
	absolute, _, err := uploadPath(workspace, name)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	rootPath, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return nil, err
	}
	_, rel, err := uploadPath(rootPath, resolved)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.OpenFile(rel, os.O_RDONLY|syscallNoFollow, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("upload must be a regular file")
	}
	return f, nil
}

func snapshotUpload(workspace, source, directory string, photo bool, caption string) (*SentFile, string, error) {
	f, err := openUpload(workspace, source)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	if _, err := ValidateUpload(f, caption, photo); err != nil {
		return nil, "", err
	}
	// A fresh, private snapshot is never reused unless its journal was committed.
	// An interrupted pre-journal copy is harmless and is not a delivery attempt.
	snapshot, err := os.CreateTemp(directory, ".file-*")
	if err != nil {
		return nil, "", err
	}
	keep := false
	defer func() {
		snapshot.Close()
		if !keep {
			os.Remove(snapshot.Name())
		}
	}()
	limit := DocumentLimit
	if photo {
		limit = PhotoLimit
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(snapshot, hash), io.LimitReader(f, limit+1))
	if err != nil {
		return nil, "", err
	}
	if n > limit || n == 0 {
		return nil, "", ErrFileLimit
	}
	if _, err := ValidateUpload(snapshot, caption, photo); err != nil {
		return nil, "", err
	}
	if err := snapshot.Sync(); err != nil {
		return nil, "", err
	}
	if err := snapshot.Chmod(0400); err != nil {
		return nil, "", err
	}
	name := filepath.Base(source)
	if strings.ContainsAny(name, "\r\n\x00\\") {
		return nil, "", errors.New("invalid upload filename")
	}
	keep = true
	return &SentFile{Path: source, Name: name, Size: n, Checksum: hex.EncodeToString(hash.Sum(nil)), AsPhoto: photo}, filepath.Base(snapshot.Name()), nil
}

func (s Store) SendFile(ctx context.Context, send FileSender, chat Conversation, sessionID, op, workspace, name, caption string, photo bool) (SendResult, error) {
	source, _, err := uploadPath(workspace, name)
	if err != nil {
		return SendResult{}, err
	}
	path, err := s.operationPath(op)
	if err != nil {
		return SendResult{}, err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return SendResult{}, err
	}
	unlock, err := maintenance.Lock(path+".lock", true)
	if err != nil {
		return SendResult{}, errors.New("cannot acquire file operation lock; check read_send")
	}
	defer unlock()
	kind := "document"
	if photo {
		kind = "photo"
	}
	var value delivery
	err = loadJSON(path, &value)
	switch {
	case errors.Is(err, os.ErrNotExist):
		file, snapshot, err := snapshotUpload(workspace, source, dir, photo, caption)
		if err != nil {
			return SendResult{}, err
		}
		value = delivery{OperationID: op, Origin: chat.Origin, ChatID: chat.ID, Session: sessionID, Created: time.Now().UTC(), Kind: kind, File: file, Snapshot: snapshot, Parts: []Part{{Text: caption, State: "pending"}}}
		if err := saveJSON(path, value); err != nil {
			return SendResult{}, err
		}
	case err != nil:
		return SendResult{}, err
	default:
		if value.File == nil || value.Kind != kind || value.Origin != chat.Origin || value.Session != sessionID || value.OperationID != op || len(value.Parts) != 1 || value.Parts[0].Text != caption || value.File.Path != source || value.File.AsPhoto != photo {
			return SendResult{}, errors.New("Telegram operation_id already exists with different contents")
		}
	}
	// Terminal and unknown outcomes are observable even when the source or
	// snapshot is no longer accessible. They must never cause another upload.
	if len(value.Parts) != 1 {
		return SendResult{}, errors.New("invalid file delivery")
	}
	switch value.Parts[0].State {
	case "confirmed", "sending", "rejected":
		return value.result(), nil
	case "pending":
	default:
		return SendResult{}, errors.New("invalid file delivery state")
	}
	if filepath.Base(value.Snapshot) != value.Snapshot || !strings.HasPrefix(value.Snapshot, ".file-") {
		return SendResult{}, errors.New("invalid file snapshot")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return SendResult{}, err
	}
	defer root.Close()
	f, err := root.OpenFile(value.Snapshot, os.O_RDONLY|syscallNoFollow, 0)
	if err != nil {
		return SendResult{}, err
	}
	defer f.Close()
	if _, err := ValidateUpload(f, caption, photo); err != nil {
		return SendResult{}, err
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, DocumentLimit+1))
	if err != nil || n != value.File.Size || hex.EncodeToString(hash.Sum(nil)) != value.File.Checksum {
		return SendResult{}, errors.New("file snapshot changed; upload refused")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return SendResult{}, err
	}
	return deliver(ctx, path, &value, chat, func(text string) (int64, error) { return send(ctx, value.ChatID, f, value.File.Name, text, photo) })
}
