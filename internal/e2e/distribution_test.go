package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ReanSn0w/horizon/internal/distribution"
	catalog "github.com/ReanSn0w/horizon/plugins"
)

func releaseFixture(t *testing.T, tag string, mutate ...func(*distribution.Manifest)) (string, string) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	manifest, err := distribution.NewManifest(tag, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range mutate {
		change(&manifest)
	}
	builds := map[string]string{"horizon": "."}
	for _, p := range catalog.Embedded().Plugins {
		builds[p.Binary] = p.Source
	}
	for name, source := range builds {
		command := exec.Command("go", "build", "-tags", "horizon_distribution_test", "-ldflags", "-X github.com/ReanSn0w/horizon/internal/buildinfo.Version="+tag+" -X github.com/ReanSn0w/horizon/internal/buildinfo.Commit=fixture", "-o", filepath.Join(dir, name), source)
		command.Dir = root
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v %s", name, err, out)
		}
	}
	data, _ := json.Marshal(manifest)
	if err = os.WriteFile(filepath.Join(dir, "release.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	assets := t.TempDir()
	archiveName := manifest.Archives[runtime.GOOS+"_"+runtime.GOARCH]
	f, err := os.Create(filepath.Join(assets, archiveName))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	gz := gzip.NewWriter(io.MultiWriter(f, hash))
	tw := tar.NewWriter(gz)
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		b, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err = tw.WriteHeader(&tar.Header{Name: entry.Name(), Mode: 0700, Size: int64(len(b))}); err != nil {
			t.Fatal(err)
		}
		if _, err = tw.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err = tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	md := sha256.Sum256(data)
	sums := fmt.Sprintf("%x  %s\n%x  release.json\n", hash.Sum(nil), archiveName, md)
	if err = os.WriteFile(filepath.Join(assets, "checksums.txt"), []byte(sums), 0600); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(assets, "release.json"), data, 0600)
	return dir, assets
}
func installerEnvironment(t *testing.T, assets string) []string {
	t.Helper()
	shim := t.TempDir()
	curl := `#!/bin/sh
output=
while [ "$#" -gt 0 ]; do
 case "$1" in -o) output=$2; shift 2;; *) url=$1; shift;; esac
done
if [ "${HORIZON_TEST_DOWNLOAD_FAIL:-}" = yes ]; then exit 22; fi
case "$url" in */releases/latest) printf '%s' 'https://github.com/ReanSn0w/horizon/releases/tag/v0.1.0'; exit 0;; esac
cp "$HORIZON_TEST_ASSETS/${url##*/}" "$output"
`
	if err := os.WriteFile(filepath.Join(shim, "curl"), []byte(curl), 0700); err != nil {
		t.Fatal(err)
	}
	return append(os.Environ(), "PATH="+shim+":"+os.Getenv("PATH"), "HORIZON_TEST_ASSETS="+assets)
}
func TestBuiltReleaseInstaller(t *testing.T) {
	_, assets := releaseFixture(t, "v0.1.0")
	root, _ := filepath.Abs("../..")
	base := t.TempDir()
	home := filepath.Join(base, "home with spaces")
	bin := filepath.Join(base, "bin with spaces")
	env := installerEnvironment(t, assets)
	install := func(extra ...string) (string, error) {
		args := append([]string{filepath.Join(root, "install.sh"), "--version", "v0.1.0", "--home", home, "--bin-dir", bin}, extra...)
		cmd := exec.Command("sh", args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := install(); err != nil {
		t.Fatalf("install: %v %s", err, out)
	}
	state, err := distribution.LoadState(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Installed) != 2 {
		t.Fatal(state.Installed)
	}
	if _, err = os.Stat(filepath.Join(home, "config.yaml")); !os.IsNotExist(err) {
		t.Fatal("installer initialized config")
	}
	cmd := exec.Command(filepath.Join(bin, "horizon"), "--version")
	out, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(out, []byte("v0.1.0")) {
		t.Fatalf("version %v %s", err, out)
	}
	cmd = exec.Command(filepath.Join(bin, "horizon"), "--home", home, "init")
	if out, err = cmd.CombinedOutput(); err != nil {
		t.Fatalf("init %v %s", err, out)
	}
	before, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
	info, _ := os.Stat(filepath.Join(bin, "horizon"))
	if out, err := install(); err != nil {
		t.Fatalf("reinstall: %v %s", err, out)
	}
	after, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
	afterInfo, _ := os.Stat(filepath.Join(bin, "horizon"))
	if !bytes.Equal(before, after) || !info.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("reinstall changed user config or identical binary")
	}
	env = append(env, "HORIZON_TEST_DOWNLOAD_FAIL=yes")
	if out, err := install(); err == nil || !strings.Contains(out, "download failed") {
		t.Fatalf("failed download: %v %s", err, out)
	}
	if !state.Healthy("horizon") {
		t.Fatal("failed download changed installation")
	}
}

func TestBuiltDistributionLifecycle(t *testing.T) {
	bundle, assets1 := releaseFixture(t, "v0.1.0")
	_, assets2 := releaseFixture(t, "v0.2.0", func(m *distribution.Manifest) {
		for i := range m.Catalog.Plugins {
			if m.Catalog.Plugins[i].ID == "telegram" {
				m.Catalog.Plugins[i].Requires = append(m.Catalog.Plugins[i].Requires, "memory")
			}
		}
	})
	var mu sync.Mutex
	paths := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/repos/" + distribution.Repository + "/releases/latest":
			fmt.Fprint(w, `{"tag_name":"v0.2.0"}`)
			return
		case "/repos/" + distribution.Repository + "/releases/tags/v0.1.0":
			fmt.Fprint(w, `{"tag_name":"v0.1.0"}`)
			return
		}
		prefix := "/" + distribution.Repository + "/releases/download/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.NotFound(w, r)
			return
		}
		relative := strings.TrimPrefix(r.URL.Path, prefix)
		tag, name, ok := strings.Cut(relative, "/")
		if !ok || filepath.Base(name) != name {
			http.NotFound(w, r)
			return
		}
		var dir string
		switch tag {
		case "v0.1.0":
			dir = assets1
		case "v0.2.0":
			dir = assets2
		default:
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(dir, name))
	}))
	defer server.Close()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	exe := filepath.Join(bin, "horizon")
	env := append(os.Environ(), "HORIZON_TEST_RELEASE_SERVER="+server.URL)
	command := func(binary string, args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := command(filepath.Join(bundle, "horizon-update"), "bootstrap", "--bundle", bundle, "--home", home, "--bin-dir", bin); err != nil {
		t.Fatalf("bootstrap %v %s", err, out)
	}
	run := func(args ...string) string {
		t.Helper()
		out, err := command(exe, append([]string{"--home", home}, args...)...)
		if err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
		return out
	}
	run("init")
	configuration, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
	preserved := map[string][]byte{"AGENTS.md": []byte("personal instructions"), "dialogs/sentinel.json": []byte("personal session"), "plugins/horizon-private": []byte("personal binary")}
	for name, data := range preserved {
		path := filepath.Join(home, name)
		_ = os.MkdirAll(filepath.Dir(path), 0700)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if out := run("plugins", "list"); !strings.Contains(out, "telegram") {
		t.Fatal(out)
	}
	run("plugins", "install", "telegram")
	state, err := distribution.LoadState(home)
	if err != nil || !slices.Contains(state.Installed, "decision") {
		t.Fatalf("dependency missing %v %v", state, err)
	}
	mu.Lock()
	beforeUpdate := slices.Clone(paths)
	mu.Unlock()
	for _, path := range beforeUpdate {
		if strings.Contains(path, "latest") || strings.Contains(path, "v0.2.0") {
			t.Fatalf("installed from wrong release: %s", path)
		}
	}
	firstInfo, _ := os.Stat(filepath.Join(home, "plugins", "horizon-telegram"))
	run("plugins", "install", "telegram")
	secondInfo, _ := os.Stat(filepath.Join(home, "plugins", "horizon-telegram"))
	if !firstInfo.ModTime().Equal(secondInfo.ModTime()) {
		t.Fatal("idempotent install rewrote binary")
	}
	run("update")
	if out := run("--version"); !strings.Contains(out, "v0.2.0") {
		t.Fatal(out)
	}
	state, err = distribution.LoadState(home)
	if err != nil || !slices.Contains(state.Installed, "memory") {
		t.Fatalf("new dependency missing %v %v", state, err)
	}
	out, err := command(exe, "--home", home, "plugins", "remove", "decision")
	if err == nil || !strings.Contains(out, "required by: telegram") {
		t.Fatalf("removed required dependency: %v %s", err, out)
	}
	run("plugins", "remove", "telegram")
	run("plugins", "remove", "decision")
	out, err = command(exe, "--home", home, "update", "--version", "v0.1.0")
	if err == nil || !strings.Contains(out, "downgrade") {
		t.Fatalf("downgrade %v %s", err, out)
	}
	for name, expected := range preserved {
		got, err := os.ReadFile(filepath.Join(home, name))
		if err != nil || !bytes.Equal(got, expected) {
			t.Fatalf("changed %s: %v", name, err)
		}
	}
	after, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
	if !bytes.Equal(after, configuration) {
		t.Fatal("changed config")
	}
}
