package telegram

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const AttachmentAttempts = 3

// Path is exposed only after a complete file has been published. Local and Temp
// are private preparation state and never included in model-facing metadata.
type Attachment struct {
	Kind     string `json:"kind"`
	FileID   string `json:"file_id"`
	UniqueID string `json:"file_unique_id,omitempty"`
	Name     string `json:"name"`
	MIME     string `json:"mime_type,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Checksum string `json:"sha256,omitempty"`
	State    string `json:"state"`
	Path     string `json:"path,omitempty"`
	Error    string `json:"error,omitempty"`
	Attempts int    `json:"attempts,omitempty"`
	Local    string `json:"local,omitempty"`
	Temp     string `json:"temp,omitempty"`
}

func MessageAttachments(m *Message) []Attachment {
	if m.Document != nil {
		d := m.Document
		return []Attachment{{Kind: "document", FileID: d.ID, UniqueID: d.UniqueID, Name: d.Name, MIME: d.MIME, Size: d.Size, State: "pending"}}
	}
	if len(m.Photos) > 0 {
		best := m.Photos[0]
		for _, p := range m.Photos[1:] {
			if int64(p.Width)*int64(p.Height) > int64(best.Width)*int64(best.Height) || p.Width == best.Width && p.Height == best.Height && p.Size > best.Size {
				best = p
			}
		}
		return []Attachment{{Kind: "photo", FileID: best.ID, UniqueID: best.UniqueID, Name: "photo.jpg", MIME: "image/jpeg", Size: best.Size, State: "pending"}}
	}
	return nil
}

// PublicAttachments strips preparation details and treats local paths as claims
// only for completed downloads. Untriggered history stays metadata-only.
func PublicAttachments(items []Attachment) []Attachment {
	result := append([]Attachment(nil), items...)
	for i := range result {
		a := &result[i]
		a.Local = ""
		a.Temp = ""
		a.Attempts = 0
		if a.State != "ready" {
			a.Path = ""
		}
	}
	return result
}

type AttachmentFetcher func(context.Context, string, io.Writer) (int64, error)

func (c *Client) FetchAttachment(ctx context.Context, id string, w io.Writer) (int64, error) {
	f, err := c.GetFile(ctx, id)
	if err != nil {
		return 0, err
	}
	return c.Download(ctx, f, w)
}

func attachmentPaths(p *PendingInput, a Attachment, index int) (string, string) {
	digest := sha256.Sum256([]byte(a.Kind + "\x00" + a.FileID + "\x00" + a.UniqueID))
	ext := strings.ToLower(filepath.Ext(a.Name))
	if len(ext) > 12 || strings.IndexFunc(ext, func(r rune) bool { return r != '.' && (r < 'a' || r > 'z') && (r < '0' || r > '9') }) >= 0 {
		ext = ""
	}
	if a.Kind == "photo" {
		ext = ".jpg"
	}
	name := fmt.Sprintf("%d-%x%s", index, digest[:12], ext)
	dir := filepath.Join("attachments", strconv.FormatInt(p.Origin, 10), strconv.FormatInt(p.Message.ID, 10))
	return filepath.Join(dir, name), filepath.Join(dir, ".horizon-"+name+".partial")
}

func attachmentRoot(workspace, local string) (*os.Root, error) {
	if !filepath.IsLocal(local) {
		return nil, errors.New("invalid attachment path")
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*os.Root, error) { root.Close(); return nil, err }
	current := ""
	for _, part := range strings.Split(filepath.Dir(local), string(os.PathSeparator)) {
		current = filepath.Join(current, part)
		if err := root.Mkdir(current, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return fail(err)
		}
		info, err := root.Lstat(current)
		if err != nil {
			return fail(err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fail(errors.New("attachment directory must not be a symlink"))
		}
	}
	return root, nil
}

func checksumFile(root *os.Root, name string, limit int64) (string, int64, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() {
		return "", 0, errors.New("attachment is not a regular file")
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscallNoFollow, 0)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, limit+1))
	if err != nil {
		return "", n, err
	}
	if n > limit {
		return "", n, ErrFileLimit
	}
	return hex.EncodeToString(hash.Sum(nil)), n, nil
}

func (s Store) saveAttachment(update int64, index int, a Attachment) error {
	return s.Update(func(r *Registry) error {
		if r.Pending == nil || r.Pending.UpdateID != update || r.Pending.Message == nil || index >= len(r.Pending.Message.Attachments) {
			return errors.New("pending attachment changed")
		}
		r.Pending.Message.Attachments[index] = a
		return nil
	})
}

// PrepareAttachments is called under the adapter's singleton poll ownership.
// Store locks cover only journal updates; HTTP and disk copies happen outside.
func (s Store) PrepareAttachments(ctx context.Context, fetch AttachmentFetcher) error {
	r, err := s.Snapshot()
	if err != nil || r.Pending == nil || r.Pending.Message == nil {
		return err
	}
	p := r.Pending
	for index, a := range p.Message.Attachments {
		local, temp := attachmentPaths(p, a, index)
		a.Local, a.Temp = local, temp
		if a.State == "failed" {
			continue
		}
		root, err := attachmentRoot(p.Workspace, local)
		if err != nil {
			a.State = "failed"
			a.Error = "attachment directory unavailable"
			a.Path = ""
			if err := s.saveAttachment(p.UpdateID, index, a); err != nil {
				return err
			}
			continue
		}
		err = s.prepareAttachment(ctx, fetch, p, index, &a, root)
		root.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s Store) prepareAttachment(ctx context.Context, fetch AttachmentFetcher, p *PendingInput, index int, a *Attachment, root *os.Root) error {
	fail := func(message string) error {
		a.State = "failed"
		a.Path = ""
		a.Error = message
		return s.saveAttachment(p.UpdateID, index, *a)
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch a.State {
		case "ready", "staged":
			sum, size, err := checksumFile(root, a.Local, DownloadLimit)
			if errors.Is(err, os.ErrNotExist) && a.State == "staged" {
				staged, n, e := checksumFile(root, a.Temp, DownloadLimit)
				if e != nil || staged != a.Checksum || n != a.Size {
					return fail("staged attachment changed or missing")
				}
				if err = root.Link(a.Temp, a.Local); err != nil && !errors.Is(err, os.ErrExist) {
					return fail("cannot publish attachment")
				}
				sum, size, err = checksumFile(root, a.Local, DownloadLimit)
			}
			if err != nil || sum != a.Checksum || size != a.Size {
				return fail("saved attachment changed or missing")
			}
			if a.State == "staged" {
				_ = root.Remove(a.Temp)
			}
			if err := syncAttachmentDirectory(root, a.Local); err != nil {
				return err
			}
			a.State = "ready"
			a.Path = filepath.Join(p.Workspace, a.Local)
			a.Error = ""
			return s.saveAttachment(p.UpdateID, index, *a)
		case "pending", "downloading":
			// A final file without a committed checksum is not ours to overwrite.
			if _, err := root.Lstat(a.Local); !errors.Is(err, os.ErrNotExist) {
				return fail("attachment destination already exists")
			}
			if a.Attempts >= AttachmentAttempts {
				if a.State == "downloading" {
					_ = root.Remove(a.Temp)
				}
				return fail("attachment download failed after three attempts")
			}
			if a.FileID == "" || a.Size < 0 || a.Size > DownloadLimit {
				return fail("attachment metadata or size is unsupported")
			}
			// Temp names are deterministic and recorded before writing, so only our
			// own interrupted partial is removed; an initial collision is an error.
			if a.State == "downloading" {
				if err := root.Remove(a.Temp); err != nil && !errors.Is(err, os.ErrNotExist) {
					return fail("cannot remove interrupted partial")
				}
			}
			a.State = "downloading"
			a.Attempts++
			if err := s.saveAttachment(p.UpdateID, index, *a); err != nil {
				return err
			}
			f, err := root.OpenFile(a.Temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return fail("cannot create attachment partial")
			}
			attempt, cancel := context.WithTimeout(ctx, 45*time.Second)
			hash := sha256.New()
			n, downloadErr := fetch(attempt, a.FileID, io.MultiWriter(f, hash))
			cancel()
			if downloadErr == nil {
				downloadErr = f.Sync()
			}
			closeErr := f.Close()
			if downloadErr == nil {
				downloadErr = closeErr
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if downloadErr != nil {
				var api *APIError
				if errors.Is(downloadErr, ErrFileLimit) || errors.Is(downloadErr, ErrFilePath) || errors.As(downloadErr, &api) && api.Code != 429 {
					_ = root.Remove(a.Temp)
					return fail("attachment download rejected")
				}
				continue
			}
			if n < 0 || n > DownloadLimit {
				return fail("attachment download exceeded limit")
			}
			if err := syncAttachmentDirectory(root, a.Temp); err != nil {
				return err
			}
			a.Size = n
			a.Checksum = hex.EncodeToString(hash.Sum(nil))
			a.State = "staged"
			if err := s.saveAttachment(p.UpdateID, index, *a); err != nil {
				return err
			}
		default:
			return fail("invalid attachment preparation state")
		}
	}
}

func syncAttachmentDirectory(root *os.Root, name string) error {
	dir, err := root.Open(filepath.Dir(name))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
