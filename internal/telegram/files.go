package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Conservative decimal MB limits for the cloud Bot API, checked 2026-11-14.
const (
	DownloadLimit     int64 = 20_000_000
	DocumentLimit     int64 = 50_000_000
	PhotoLimit        int64 = 10_000_000
	CaptionLimit            = 1024
	PhotoDimensionSum       = 10000
	PhotoAspectRatio        = 20
)

var ErrFileLimit = errors.New("Telegram file exceeds size limit")
var ErrFilePath = errors.New("invalid Telegram download path")

func (t *Client) GetFile(ctx context.Context, id string) (File, error) {
	var f File
	if strings.TrimSpace(id) == "" {
		return f, errors.New("empty Telegram file ID")
	}
	err := t.call(ctx, "getFile", map[string]string{"file_id": id}, &f)
	if err == nil && (f.ID != id || f.Size < 0 || f.Path == "") {
		err = errors.New("invalid Telegram file metadata")
	}
	return f, err
}

func downloadPath(value string) bool {
	if value == "" || strings.ContainsAny(value, "\\%?#\x00\r\n") || path.IsAbs(value) || path.Clean(value) != value {
		return false
	}
	u, err := url.Parse(value)
	if err != nil || u.IsAbs() || u.Host != "" {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." || part == "." {
			return false
		}
	}
	return true
}

func (t *Client) Download(ctx context.Context, f File, dest io.Writer) (int64, error) {
	if !downloadPath(f.Path) {
		return 0, ErrFilePath
	}
	if f.Size < 0 || f.Size > DownloadLimit {
		return 0, ErrFileLimit
	}
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(t.base, "/")+"/file/bot"+t.token+"/"+f.Path, nil)
	if err != nil {
		return 0, ErrFilePath
	}
	resp, err := t.http.Do(req)
	if err != nil {
		return 0, errors.New("Telegram file download failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("Telegram file download HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > DownloadLimit {
		return 0, ErrFileLimit
	}
	// The extra byte is read only to detect overflow, never written to dest.
	n, err := io.Copy(dest, io.LimitReader(resp.Body, DownloadLimit))
	if err != nil {
		return n, errors.New("Telegram file could not be saved completely")
	}
	var extra [1]byte
	count, end := resp.Body.Read(extra[:])
	if count > 0 {
		return n, ErrFileLimit
	}
	if end != io.EOF {
		return n, errors.New("Telegram file download incomplete")
	}
	if f.Size > 0 && n != f.Size {
		return n, errors.New("Telegram file size mismatch")
	}
	return n, nil
}

func ValidateUpload(f *os.File, caption string, photo bool) (int64, error) {
	if !utf8.ValidString(caption) || utf8.RuneCountInString(caption) > CaptionLimit {
		return 0, errors.New("Telegram caption must be UTF-8 and at most 1024 characters")
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return 0, errors.New("Telegram upload must be a regular file")
	}
	limit := DocumentLimit
	if photo {
		limit = PhotoLimit
	}
	if info.Size() <= 0 || info.Size() > limit {
		return 0, ErrFileLimit
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	if photo {
		cfg, format, err := image.DecodeConfig(f)
		if err != nil || (format != "jpeg" && format != "png") || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > PhotoDimensionSum || cfg.Height > PhotoDimensionSum || cfg.Width+cfg.Height > PhotoDimensionSum || cfg.Width > cfg.Height*PhotoAspectRatio || cfg.Height > cfg.Width*PhotoAspectRatio {
			return 0, errors.New("photo must be JPEG/PNG with width+height <= 10000 and aspect ratio <= 20; send other formats as a document")
		}
	}
	_, err = f.Seek(0, io.SeekStart)
	return info.Size(), err
}

func (t *Client) SendFile(ctx context.Context, chat int64, f *os.File, name, caption string, photo bool) (int64, error) {
	size, err := ValidateUpload(f, caption, photo)
	if err != nil {
		return 0, err
	}
	if name == "" || strings.ContainsAny(name, "\r\n\x00/\\") {
		return 0, errors.New("invalid upload filename")
	}
	method, field := "sendDocument", "document"
	if photo {
		method, field = "sendPhoto", "photo"
	}
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	if err := writer.WriteField("chat_id", strconv.FormatInt(chat, 10)); err != nil {
		return 0, err
	}
	if err := writer.WriteField("caption", caption); err != nil {
		return 0, err
	}
	if _, err := writer.CreateFormFile(field, name); err != nil {
		return 0, err
	}
	prefix := append([]byte(nil), buffer.Bytes()...)
	buffer.Reset()
	if err := writer.Close(); err != nil {
		return 0, err
	}
	suffix := buffer.Bytes()
	body := io.MultiReader(bytes.NewReader(prefix), io.LimitReader(f, size), bytes.NewReader(suffix))
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(t.base, "/")+"/bot"+t.token+"/"+method, body)
	if err != nil {
		return 0, errors.New("invalid Telegram endpoint")
	}
	req.ContentLength = int64(len(prefix)+len(suffix)) + size
	req.Header.Set("Content-Type", writer.FormDataContentType())
	var msg Message
	if err := t.request(req, &msg); err != nil {
		return 0, err
	}
	if msg.ID <= 0 || msg.Chat.ID != 0 && msg.Chat.ID != chat {
		return 0, errors.New("Telegram did not confirm file message identity")
	}
	return msg.ID, nil
}
