// release-manifest prepares and verifies the complete set of release assets.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/ReanSn0w/horizon/internal/distribution"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	tag := flag.String("tag", "", "release tag")
	commit := flag.String("commit", "", "commit SHA")
	out := flag.String("out", ".release/release.json", "manifest path")
	dist := flag.String("verify-dist", "", "verify archives and prepare publishable assets")
	flag.Parse()
	if *dist != "" {
		return verify(*out, *dist)
	}
	m, err := distribution.NewManifest(*tag, *commit)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(*out), 0700); err != nil {
		return err
	}
	return os.WriteFile(*out, data, 0600)
}
func verify(manifestPath, dir string) error {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	m, err := distribution.DecodeManifest(data)
	if err != nil {
		return err
	}
	names := []string{"release.json", "install.sh"}
	for _, platform := range distribution.Platforms {
		name := m.Archives[platform]
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		bundle, err := distribution.Extract(f, m)
		f.Close()
		if err != nil {
			return fmt.Errorf("verify %s: %w", name, err)
		}
		bundle.Close()
		names = append(names, name)
	}
	if err = os.WriteFile(filepath.Join(dir, "release.json"), data, 0600); err != nil {
		return err
	}
	script, err := os.ReadFile("install.sh")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "install.sh"), script, 0700); err != nil {
		return err
	}
	sort.Strings(names)
	var sums string
	for _, name := range names {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return err
		}
		sums += fmt.Sprintf("%x  %s\n", h.Sum(nil), name)
	}
	return os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(sums), 0600)
}
