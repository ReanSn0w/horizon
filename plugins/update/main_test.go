package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/ReanSn0w/horizon/internal/buildinfo"
	"github.com/ReanSn0w/horizon/internal/distribution"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestHelpMetadataWithoutConfiguration(t *testing.T) {
	home := filepath.Join(t.TempDir(), "absent")
	t.Setenv("HORIZON_HOME", home)
	for _, args := range [][]string{{"--help"}, {"horizon-plugin-metadata"}, {"bootstrap", "--help"}} {
		var out, diag bytes.Buffer
		if code := run(context.Background(), args, &out, &diag, distribution.NewClient()); code != 0 {
			t.Fatalf("%v: %d %s", args, code, diag.String())
		}
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("created home")
	}
	var out, diag bytes.Buffer
	if code := run(context.Background(), []string{"--unknown"}, &out, &diag, nil); code != 2 {
		t.Fatal(code)
	}
}

func TestBootstrapUpdateAndDowngrade(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	oldVersion, oldCommit := buildinfo.Version, buildinfo.Commit
	t.Cleanup(func() { buildinfo.Version = oldVersion; buildinfo.Commit = oldCommit })
	buildinfo.Version, buildinfo.Commit = "v0.1.0", "abc"
	bundle := testBundle(t, "v0.1.0")
	var out, diag bytes.Buffer
	args := []string{"bootstrap", "--bundle", bundle.Dir, "--home", home, "--bin-dir", bin}
	wrong := append(slices.Clone(args), "--expected-tag", "v9.0.0")
	if code := run(context.Background(), wrong, &out, &diag, nil); code != 1 || !strings.Contains(diag.String(), "differs from requested") {
		t.Fatalf("wrong tag: %d %s", code, diag.String())
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("wrong tag created installation home")
	}
	out.Reset()
	diag.Reset()

	if code := run(context.Background(), args, &out, &diag, nil); code != 0 {
		t.Fatalf("%d %s", code, diag.String())
	}
	m, err := distribution.OpenManager(home, filepath.Join(bin, "horizon"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Install(context.Background(), bundle, []string{"telegram"}, runtime.GOOS+"_"+runtime.GOARCH, false); err != nil {
		t.Fatal(err)
	}
	m.Close()
	next := testBundle(t, "v0.2.0")
	for i := range next.Manifest.Catalog.Plugins {
		if next.Manifest.Catalog.Plugins[i].ID == "telegram" {
			next.Manifest.Catalog.Plugins[i].Requires = append(next.Manifest.Catalog.Plugins[i].Requires, "memory")
		}
	}
	data, _ := json.Marshal(next.Manifest)
	_ = os.WriteFile(filepath.Join(next.Dir, "release.json"), data, 0600)
	t.Setenv("HORIZON_HOME", home)
	t.Setenv("HORIZON_EXECUTABLE", filepath.Join(bin, "horizon"))
	client := testClient(t, next)
	out.Reset()
	diag.Reset()
	if code := run(context.Background(), nil, &out, &diag, client); code != 0 {
		t.Fatalf("%d %s", code, diag.String())
	}
	state, err := distribution.LoadState(home)
	if err != nil || state.Manifest.Tag != "v0.2.0" || !slices.Contains(state.Installed, "memory") {
		t.Fatalf("%v %v", state, err)
	}
	buildinfo.Version = "v0.2.0"
	out.Reset()
	diag.Reset()
	if code := run(context.Background(), []string{"--version", "v0.1.0"}, &out, &diag, testClient(t, bundle)); code != 1 || !strings.Contains(diag.String(), "downgrade") {
		t.Fatalf("%d %s", code, diag.String())
	}
	buildinfo.Version = "v0.1.0"
	out.Reset()
	diag.Reset()
	if code := run(context.Background(), args, &out, &diag, nil); code != 1 || !strings.Contains(diag.String(), "downgrade") {
		t.Fatalf("bootstrap downgrade %d %s", code, diag.String())
	}
}
func testBundle(t *testing.T, tag string) *distribution.Bundle {
	t.Helper()
	m, err := distribution.NewManifest(tag, "abc")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	b := &distribution.Bundle{Manifest: m, Dir: dir, Hashes: map[string]string{}}
	names := []string{"horizon"}
	for _, p := range m.Catalog.Plugins {
		names = append(names, p.Binary)
	}
	for _, name := range names {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(tag+name), 0700); err != nil {
			t.Fatal(err)
		}
		b.Hashes[name], err = distribution.FileDigest(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	data, _ := json.Marshal(m)
	_ = os.WriteFile(filepath.Join(dir, "release.json"), data, 0600)
	return b
}

type rt func(*http.Request) (*http.Response, error)

func (f rt) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func testClient(t *testing.T, b *distribution.Bundle) *distribution.Client {
	t.Helper()
	manifest, _ := json.Marshal(b.Manifest)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	entries, _ := os.ReadDir(b.Dir)
	for _, entry := range entries {
		data, _ := os.ReadFile(filepath.Join(b.Dir, entry.Name()))
		if entry.Name() == "release.json" {
			data = manifest
		}
		_ = tw.WriteHeader(&tar.Header{Name: entry.Name(), Mode: 0700, Size: int64(len(data))})
		_, _ = tw.Write(data)
	}
	_ = tw.Close()
	_ = gz.Close()
	archive := buf.Bytes()
	md := sha256.Sum256(manifest)
	ad := sha256.Sum256(archive)
	archiveName := b.Manifest.Archives[runtime.GOOS+"_"+runtime.GOARCH]
	return &distribution.Client{HTTP: &http.Client{Transport: rt(func(r *http.Request) (*http.Response, error) {
		var body []byte
		switch filepath.Base(r.URL.Path) {
		case "latest", b.Manifest.Tag:
			body = []byte(`{"tag_name":"` + b.Manifest.Tag + `"}`)
		case "release.json":
			body = manifest
		case "checksums.txt":
			body = []byte(fmt.Sprintf("%x  release.json\n%x  %s\n", md, ad, archiveName))
		case archiveName:
			body = archive
		default:
			return nil, fmt.Errorf("unexpected URL %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})}}
}
