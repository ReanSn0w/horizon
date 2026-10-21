package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ReanSn0w/horizon/internal/distribution"
	catalog "github.com/ReanSn0w/horizon/plugins"
)

func releaseFixture(t *testing.T, tag string) (string, string) {
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
	builds := map[string]string{"horizon": "."}
	for _, p := range catalog.Embedded().Plugins {
		builds[p.Binary] = p.Source
	}
	for name, source := range builds {
		command := exec.Command("go", "build", "-ldflags", "-X github.com/ReanSn0w/horizon/internal/buildinfo.Version="+tag+" -X github.com/ReanSn0w/horizon/internal/buildinfo.Commit=fixture", "-o", filepath.Join(dir, name), source)
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
