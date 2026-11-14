package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testUpload(t *testing.T, data []byte) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "upload")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	return f
}
func TestFileTransport(t *testing.T) {
	content := []byte{0, 1, 2, 255, 10}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/botsecret/getFile":
			fmt.Fprint(w, `{"ok":true,"result":{"file_id":"f","file_path":"documents/f.bin","file_size":5}}`)
		case "/file/botsecret/documents/f.bin":
			w.Write(content)
		case "/botsecret/sendDocument", "/botsecret/sendPhoto":
			if err := r.ParseMultipartForm(1024); err != nil {
				t.Error(err)
				return
			}
			defer r.MultipartForm.RemoveAll()
			field := "document"
			if strings.HasSuffix(r.URL.Path, "sendPhoto") {
				field = "photo"
			}
			f, header, err := r.FormFile(field)
			if err != nil {
				t.Error(err)
				return
			}
			defer f.Close()
			data, _ := io.ReadAll(f)
			if field == "document" && !bytes.Equal(data, content) {
				t.Errorf("wrong bytes: %v", data)
			}
			if header.Filename != "file.bin" || r.FormValue("chat_id") != "42" || r.FormValue("caption") != "caption <literal>" || r.FormValue("parse_mode") != "" {
				t.Error("wrong multipart fields")
			}
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":17,"chat":{"id":42}}}`)
		default:
			t.Error(r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client := New("secret")
	client.base = server.URL
	f, err := client.GetFile(context.Background(), "f")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	n, err := client.Download(context.Background(), f, &output)
	if err != nil || n != 5 || !bytes.Equal(output.Bytes(), content) {
		t.Fatal(n, err, output.Bytes())
	}
	for _, photo := range []bool{false, true} {
		data := content
		if photo {
			var b bytes.Buffer
			png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2)))
			data = b.Bytes()
		}
		id, err := client.SendFile(context.Background(), 42, testUpload(t, data), "file.bin", "caption <literal>", photo)
		if err != nil || id != 17 {
			t.Fatal(id, err)
		}
	}
}
func TestDownloadBoundsAndFailures(t *testing.T) {
	for _, value := range []string{"https://evil/x", "//evil/x", "../x", "a/../x", "x?token=y", "x#y", "%2e%2e/x", "x\\y", "/absolute"} {
		if downloadPath(value) {
			t.Fatal(value)
		}
	}
	for _, scenario := range []string{"overflow", "truncated", "redirect", "status", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch scenario {
				case "overflow":
					w.(http.Flusher).Flush()
					io.CopyN(w, strings.NewReader(strings.Repeat("x", int(DownloadLimit+1))), DownloadLimit+1)
				case "truncated":
					w.Header().Set("Content-Length", "10")
					fmt.Fprint(w, "x")
				case "redirect":
					http.Redirect(w, r, "https://example.invalid/secret", 302)
				default:
					w.WriteHeader(503)
				}
			}))
			defer server.Close()
			c := New("secret")
			c.base = server.URL
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "cancel" {
				cancel()
			}
			n, err := c.Download(ctx, File{Path: "file"}, io.Discard)
			if err == nil || strings.Contains(err.Error(), "secret") || n > DownloadLimit {
				t.Fatal(n, err)
			}
		})
	}
}
func TestUploadValidation(t *testing.T) {
	f := testUpload(t, []byte("x"))
	if _, err := ValidateUpload(f, strings.Repeat("a", CaptionLimit), false); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateUpload(f, strings.Repeat("a", CaptionLimit+1), false); err == nil {
		t.Fatal("caption overflow")
	}
	if _, err := ValidateUpload(f, "", true); err == nil {
		t.Fatal("non-image photo accepted")
	}
	for _, limit := range []int64{DocumentLimit, PhotoLimit} {
		if err := f.Truncate(limit + 1); err != nil {
			t.Fatal(err)
		}
		if _, err := ValidateUpload(f, "", limit == PhotoLimit); !errors.Is(err, ErrFileLimit) {
			t.Fatal(err)
		}
	}
	if err := f.Truncate(DocumentLimit); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateUpload(f, "", false); err != nil {
		t.Fatal(err)
	}

	for _, size := range [][2]int{{21, 1}, {1, 21}, {10000, 1}} {
		var b bytes.Buffer
		if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, size[0], size[1]))); err != nil {
			t.Fatal(err)
		}
		if _, err := ValidateUpload(testUpload(t, b.Bytes()), "", true); err == nil {
			t.Fatal("invalid photo dimensions accepted", size)
		}
	}

	dir, err := os.Open(filepath.Dir(f.Name()))
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if _, err := ValidateUpload(dir, "", false); err == nil {
		t.Fatal("directory accepted")
	}
}
func TestFileSendUnknownAndRejected(t *testing.T) {
	for _, status := range []int{400, 500} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			fmt.Fprintf(w, `{"ok":false,"error_code":%d,"description":"secret"}`, status)
		}))
		c := New("secret")
		c.base = s.URL
		_, err := c.SendFile(context.Background(), 42, testUpload(t, []byte("x")), "x.txt", "", false)
		var api *APIError
		if err == nil || errors.As(err, &api) != (status == 400) || strings.Contains(err.Error(), "secret") {
			t.Fatal(err)
		}
		s.Close()
	}
}
