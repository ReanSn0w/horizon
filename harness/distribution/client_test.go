package distribution

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fixture(t *testing.T, tag string) (Manifest, []byte, []byte) {
	t.Helper()
	m, err := NewManifest(tag, "abc")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(m)
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	files := map[string][]byte{"release.json": data, "horizon": []byte("core")}
	for _, p := range m.Catalog.Plugins {
		files[p.Binary] = []byte(p.ID)
	}
	for n, b := range files {
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0700, Size: int64(len(b))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return m, data, out.Bytes()
}
func fixtureClient(t *testing.T, tag string) (*Client, *[]string) {
	t.Helper()
	m, data, archive := fixture(t, tag)
	paths := []string{}
	c := &Client{HTTP: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.String())
		var body []byte
		status := 200
		switch r.URL.Path {
		case "/repos/" + Repository + "/releases/latest", "/repos/" + Repository + "/releases/tags/" + tag:
			body = []byte(`{"tag_name":"` + tag + `"}`)
		case "/" + Repository + "/releases/download/" + tag + "/release.json":
			body = data
		case "/" + Repository + "/releases/download/" + tag + "/checksums.txt":
			body = []byte(fmt.Sprintf("%s  release.json\n%s  %s\n", digest(data), digest(archive), m.Archives["linux_amd64"]))
		case "/" + Repository + "/releases/download/" + tag + "/" + m.Archives["linux_amd64"]:
			body = archive
		default:
			status = 404
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})}}
	return c, &paths
}
func TestExactReleaseBundle(t *testing.T) {
	c, paths := fixtureClient(t, "v0.1.0")
	b, err := c.Bundle(context.Background(), "v0.1.0", "linux_amd64")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.Hashes["horizon-telegram"] != digest([]byte("telegram")) {
		t.Fatal(b.Hashes)
	}
	for _, p := range *paths {
		if strings.Contains(p, "latest") || strings.Contains(p, "master") {
			t.Fatal(p)
		}
	}
	if _, err = c.Bundle(context.Background(), "v0.2.0", "linux_amd64"); err == nil {
		t.Fatal("accepted missing release")
	}
}
func TestBadChecksumAndHTTPStatus(t *testing.T) {
	for _, status := range []int{200, 403, 404, 429} {
		c := &Client{HTTP: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(strings.Repeat("0", 64) + "  release.json\n"))}, nil
		})}}
		if _, _, err := c.Manifest(context.Background(), "v0.1.0"); err == nil {
			t.Fatalf("accepted %d", status)
		}
	}
}
func TestExtractRejectsPathsLinksAndDuplicates(t *testing.T) {
	m, _, _ := fixture(t, "v0.1.0")
	for _, h := range []*tar.Header{{Name: "../escape", Size: 0, Mode: 0700}, {Name: "horizon", Typeflag: tar.TypeSymlink, Linkname: "/tmp/escape"}, {Name: "/horizon", Mode: 0700}, {Name: "horizon", Mode: 0700}} {
		var out bytes.Buffer
		gz := gzip.NewWriter(&out)
		tw := tar.NewWriter(gz)
		_ = tw.WriteHeader(h)
		_ = tw.WriteHeader(h)
		_ = tw.Close()
		_ = gz.Close()
		if b, err := Extract(&out, m); err == nil {
			b.Close()
			t.Fatalf("accepted %v", h)
		}
	}
}

func TestCancelledAndTruncatedDownloads(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &Client{HTTP: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() })}}
	if _, _, err := client.Manifest(ctx, "v0.1.0"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	m, _, archive := fixture(t, "v0.1.0")
	if b, err := Extract(bytes.NewReader(archive[:len(archive)-4]), m); err == nil {
		b.Close()
		t.Fatal("accepted truncated gzip")
	}
	if _, err := parseChecksums([]byte(strings.Repeat("0", 64) + "  release.json\n" + strings.Repeat("1", 64) + "  release.json\n")); err == nil {
		t.Fatal("accepted duplicate checksum")
	}
}
