// release-smoke executes only the archive matching the current native platform.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/ReanSn0w/horizon/internal/distribution"
	"github.com/ReanSn0w/horizon/internal/plugins"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	dir := flag.String("dist", "dist", "release asset directory")
	flag.Parse()
	data, err := os.ReadFile(filepath.Join(*dir, "release.json"))
	if err != nil {
		return err
	}
	m, err := distribution.DecodeManifest(data)
	if err != nil {
		return err
	}
	platform := runtime.GOOS + "_" + runtime.GOARCH
	file, err := os.Open(filepath.Join(*dir, m.Archives[platform]))
	if err != nil {
		return err
	}
	defer file.Close()
	b, err := distribution.Extract(file, m)
	if err != nil {
		return err
	}
	defer b.Close()
	call := func(name string, args ...string) ([]byte, error) {
		cmd := exec.Command(name, args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("%s: %w: %s", name, err, out)
		}
		return out, nil
	}
	out, err := call(filepath.Join(b.Dir, "horizon"), "--version")
	if err != nil {
		return err
	}
	if !strings.Contains(string(out), m.Tag) || !strings.Contains(string(out), m.Commit) {
		return fmt.Errorf("core build identity mismatch: %s", out)
	}
	for _, p := range m.Catalog.Plugins {
		out, err = call(filepath.Join(b.Dir, p.Binary), "horizon-plugin-metadata")
		if err != nil {
			return err
		}
		var metadata plugins.Metadata
		if err = json.Unmarshal(out, &metadata); err != nil {
			return err
		}
		if metadata.Version != m.Tag || metadata.Description != p.Description {
			return fmt.Errorf("metadata mismatch: %s", p.ID)
		}
	}
	root, err := os.MkdirTemp("", "horizon-release-smoke-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	home, bin := filepath.Join(root, "home"), filepath.Join(root, "bin")
	if _, err = call(filepath.Join(b.Dir, "horizon-update"), "bootstrap", "--bundle", b.Dir, "--home", home, "--bin-dir", bin); err != nil {
		return err
	}
	if _, err = call(filepath.Join(bin, "horizon"), "--home", home, "init"); err != nil {
		return err
	}
	state, err := distribution.LoadState(home)
	if err != nil {
		return err
	}
	if len(state.Installed) != 2 {
		return fmt.Errorf("bootstrap installed unexpected plugins")
	}
	fmt.Printf("Native smoke passed: %s, %s (%s)\n", platform, m.Tag, m.Commit)
	return nil
}
