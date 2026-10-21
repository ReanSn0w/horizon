package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/ReanSn0w/horizon/internal/buildinfo"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReanSn0w/horizon/internal/distribution"
)

func TestHelpMetadataAndArgumentsWithoutHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "absent")
	t.Setenv("HORIZON_HOME", home)
	for _, args := range [][]string{{"--help"}, {"horizon-plugin-metadata"}} {
		var out, diag bytes.Buffer
		if code := run(context.Background(), args, &out, &diag, distribution.NewClient()); code != 0 {
			t.Fatalf("%v: %d %s", args, code, diag.String())
		}
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("created home")
	}
	var out, diag bytes.Buffer
	if code := run(context.Background(), []string{"install"}, &out, &diag, nil); code != 2 {
		t.Fatal(code)
	}
}
func TestActivePluginsWithoutProvider(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("agent_plugins: [memory]\nprovider: {key: ''}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	active, err := activePlugin(home, "memory")
	if err != nil || !active {
		t.Fatalf("%v %v", active, err)
	}
	active, err = activePlugin(home, "telegram")
	if err != nil || active {
		t.Fatalf("%v %v", active, err)
	}
}

func TestManagedListNoopAndDependencyRemoval(t *testing.T) {
	home := t.TempDir()
	exe := filepath.Join(t.TempDir(), "horizon")
	m, err := distribution.OpenManager(home, exe)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := distribution.NewManifest("v0.1.0", "abc")
	if err != nil {
		t.Fatal(err)
	}
	b := &distribution.Bundle{Manifest: manifest, Dir: t.TempDir(), Hashes: map[string]string{}}
	names := []string{"horizon"}
	for _, p := range manifest.Catalog.Plugins {
		names = append(names, p.Binary)
	}
	for _, name := range names {
		path := filepath.Join(b.Dir, name)
		if err := os.WriteFile(path, []byte(name), 0700); err != nil {
			t.Fatal(err)
		}
		b.Hashes[name], err = distribution.FileDigest(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = m.Install(context.Background(), b, []string{"telegram", "memory"}, "linux_amd64", false); err != nil {
		t.Fatal(err)
	}
	m.Close()
	if err = os.WriteFile(filepath.Join(home, "config.yaml"), []byte("agent_plugins: [memory]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HORIZON_HOME", home)
	t.Setenv("HORIZON_EXECUTABLE", exe)
	previous := buildinfo.Version
	buildinfo.Version = "v0.1.0"
	t.Cleanup(func() { buildinfo.Version = previous })
	offline := &distribution.Client{HTTP: &http.Client{Transport: offlineTransport{}}}
	for _, tc := range []struct {
		args     []string
		code     int
		contains string
	}{
		{[]string{"list"}, 0, "telegram"},
		{[]string{"install", "telegram"}, 0, "already installed"},
		{[]string{"remove", "decision"}, 1, "required by: telegram"},
		{[]string{"remove", "memory"}, 1, "disable memory"},
		{[]string{"remove", "plugins"}, 1, "required by the installation"},
		{[]string{"remove", "telegram"}, 0, "removed"},
	} {
		var out, diag bytes.Buffer
		code := run(context.Background(), tc.args, &out, &diag, offline)
		if code != tc.code || !strings.Contains(out.String()+diag.String(), tc.contains) {
			t.Fatalf("%v: %d %s %s", tc.args, code, out.String(), diag.String())
		}
	}
}

type offlineTransport struct{}

func (offlineTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("offline fixture")
}
