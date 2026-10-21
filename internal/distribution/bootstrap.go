package distribution

import (
	"fmt"
	"os"
	"path/filepath"
)

// ReadBundle validates a directory extracted by the checksum-verifying bootstrap script.
// The caller owns the directory; it must not call Bundle.Close on this borrowed bundle.
func ReadBundle(dir string) (*Bundle, error) {
	info, err := os.Lstat(filepath.Join(dir, "release.json"))
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxManifest {
		return nil, fmt.Errorf("invalid bootstrap manifest")
	}
	data, err := os.ReadFile(filepath.Join(dir, "release.json"))
	if err != nil {
		return nil, err
	}
	m, err := DecodeManifest(data)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{"horizon": true, "release.json": true}
	for _, p := range m.Catalog.Plugins {
		allowed[p.Binary] = true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	if len(entries) != len(allowed) {
		return nil, fmt.Errorf("incomplete bootstrap bundle")
	}
	b := &Bundle{Manifest: m, Dir: dir, Hashes: map[string]string{}}
	var total int64
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return nil, fmt.Errorf("unexpected bootstrap file %s", entry.Name())
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("non-regular bootstrap file %s", entry.Name())
		}
		total += info.Size()
		if total > maxExpanded {
			return nil, fmt.Errorf("bootstrap bundle too large")
		}
		sum, err := FileDigest(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		b.Hashes[entry.Name()] = sum
	}
	return b, nil
}
