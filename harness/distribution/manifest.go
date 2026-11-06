// Package distribution implements the official Horizon release format and installation.
package distribution

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	catalog "github.com/ReanSn0w/horizon/plugins"
)

const Repository = "ReanSn0w/horizon"

var Platforms = []string{"linux_amd64", "linux_arm64", "darwin_amd64", "darwin_arm64"}
var tagPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

type Manifest struct {
	SchemaVersion int               `json:"schema_version"`
	Tag           string            `json:"tag"`
	Commit        string            `json:"commit"`
	Archives      map[string]string `json:"archives"`
	Catalog       catalog.Catalog   `json:"catalog"`
}

func ValidTag(tag string) bool {
	if !tagPattern.MatchString(tag) {
		return false
	}
	_, pre, _ := strings.Cut(tag, "-")
	for _, part := range strings.Split(pre, ".") {
		if numeric(part) && len(part) > 1 && part[0] == '0' {
			return false
		}
	}
	return true
}
func ArchiveName(tag, platform string) string {
	return "horizon_" + strings.TrimPrefix(tag, "v") + "_" + platform + ".tar.gz"
}
func NewManifest(tag, commit string) (Manifest, error) {
	m := Manifest{1, tag, commit, map[string]string{}, catalog.Embedded()}
	for i := range m.Catalog.Plugins {
		m.Catalog.Plugins[i].Source = ""
	}
	for _, p := range Platforms {
		m.Archives[p] = ArchiveName(tag, p)
	}
	return m, m.Validate()
}
func (m Manifest) Validate() error {
	if m.SchemaVersion != 1 || !ValidTag(m.Tag) || m.Commit == "" {
		return fmt.Errorf("invalid release manifest version/tag/commit")
	}
	if err := m.Catalog.Validate(); err != nil {
		return err
	}
	for _, id := range []string{"update", "plugins"} {
		p, ok := m.Catalog.Find(id)
		if !ok || !p.Required {
			return fmt.Errorf("missing required component %s", id)
		}
	}
	if len(m.Archives) != len(Platforms) {
		return fmt.Errorf("incomplete release platforms")
	}
	for platform, name := range m.Archives {
		if !slices.Contains(Platforms, platform) || name != ArchiveName(m.Tag, platform) {
			return fmt.Errorf("invalid release archive %q", name)
		}
	}
	return nil
}
func DecodeManifest(data []byte) (Manifest, error) {
	var m Manifest
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return m, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return m, fmt.Errorf("extra manifest data")
	}
	return m, m.Validate()
}

// SameManifest ignores source-only catalog fields and map iteration order.
func SameManifest(a, b Manifest) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
