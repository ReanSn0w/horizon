package distribution

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

const maxManifest = 1 << 20
const maxArchive = 256 << 20
const maxExpanded = 512 << 20

// Client always addresses the official repository. HTTP is injectable for tests.
type Client struct{ HTTP *http.Client }

func NewClient() *Client { return &Client{HTTP: &http.Client{Timeout: 5 * time.Minute}} }
func (c *Client) get(ctx context.Context, url string, limit int64, out io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Horizon-distribution")
	req.Header.Set("Accept", "application/vnd.github+json")
	client := c.HTTP
	if client == nil {
		client = NewClient().HTTP
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d (check release availability or GitHub rate limits)", url, resp.StatusCode)
	}
	if resp.ContentLength > limit {
		return fmt.Errorf("download exceeds size limit")
	}
	n, err := io.Copy(out, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return fmt.Errorf("download exceeds size limit")
	}
	return nil
}
func (c *Client) bytes(ctx context.Context, url string, limit int64) ([]byte, error) {
	var b bytes.Buffer
	err := c.get(ctx, url, limit, &b)
	return b.Bytes(), err
}
func (c *Client) Resolve(ctx context.Context, tag string) (string, error) {
	endpoint := "latest"
	if tag != "" {
		if !ValidTag(tag) {
			return "", fmt.Errorf("invalid release tag %q", tag)
		}
		endpoint = "tags/" + tag
	}
	data, err := c.bytes(ctx, "https://api.github.com/repos/"+Repository+"/releases/"+endpoint, maxManifest)
	if err != nil {
		return "", err
	}
	var release struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err = json.Unmarshal(data, &release); err != nil {
		return "", err
	}
	if !ValidTag(release.Tag) || release.Draft || (tag == "" && (release.Prerelease || strings.Contains(release.Tag, "-"))) || (tag != "" && release.Tag != tag) {
		return "", fmt.Errorf("unexpected release identity")
	}
	return release.Tag, nil
}
func assetURL(tag, name string) string {
	return "https://github.com/" + Repository + "/releases/download/" + tag + "/" + name
}
func parseChecksums(data []byte) (map[string]string, error) {
	result := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("invalid checksum line")
		}
		name := strings.TrimPrefix(fields[1], "*")
		digest, err := hex.DecodeString(fields[0])
		if err != nil || len(digest) != sha256.Size || filepath.Base(name) != name || name == "." || result[name] != "" {
			return nil, fmt.Errorf("invalid checksum entry %q", name)
		}
		result[name] = strings.ToLower(fields[0])
	}
	return result, nil
}
func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func (c *Client) Manifest(ctx context.Context, tag string) (Manifest, map[string]string, error) {
	var m Manifest
	if !ValidTag(tag) {
		return m, nil, fmt.Errorf("release version required; install with install.sh first")
	}
	sums, err := c.bytes(ctx, assetURL(tag, "checksums.txt"), 64<<10)
	if err != nil {
		return m, nil, err
	}
	checks, err := parseChecksums(sums)
	if err != nil {
		return m, nil, err
	}
	data, err := c.bytes(ctx, assetURL(tag, "release.json"), maxManifest)
	if err != nil {
		return m, nil, err
	}
	if checks["release.json"] == "" || digest(data) != checks["release.json"] {
		return m, nil, fmt.Errorf("release.json checksum mismatch")
	}
	m, err = DecodeManifest(data)
	if err != nil {
		return m, nil, err
	}
	if m.Tag != tag {
		return m, nil, fmt.Errorf("manifest tag mismatch")
	}
	return m, checks, nil
}

type Bundle struct {
	Manifest Manifest
	Dir      string
	Hashes   map[string]string
}

func (b *Bundle) Close() {
	if b != nil {
		_ = os.RemoveAll(b.Dir)
	}
}
func (c *Client) Bundle(ctx context.Context, tag, platform string) (*Bundle, error) {
	m, checks, err := c.Manifest(ctx, tag)
	if err != nil {
		return nil, err
	}
	name, ok := m.Archives[platform]
	if !ok {
		return nil, fmt.Errorf("unsupported platform %s", platform)
	}
	if checks[name] == "" {
		return nil, fmt.Errorf("missing checksum for %s", name)
	}
	temp, err := os.CreateTemp("", "horizon-archive-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	hash := sha256.New()
	if err = c.get(ctx, assetURL(tag, name), maxArchive, io.MultiWriter(temp, hash)); err != nil {
		return nil, err
	}
	if hex.EncodeToString(hash.Sum(nil)) != checks[name] {
		return nil, fmt.Errorf("archive checksum mismatch")
	}
	if _, err = temp.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return Extract(temp, m)
}

// Extract accepts exactly the flat, regular-file release format, never archive paths.
func Extract(r io.Reader, m Manifest) (*Bundle, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "horizon-bundle-*")
	if err != nil {
		return nil, err
	}
	b := &Bundle{m, dir, map[string]string{}}
	success := false
	defer func() {
		if !success {
			b.Close()
		}
	}()
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	expanded := &io.LimitedReader{R: gz, N: maxExpanded + 1}
	tr := tar.NewReader(expanded)
	allowed := map[string]bool{"horizon": true, "release.json": true}
	for _, p := range m.Catalog.Plugins {
		allowed[p.Binary] = true
	}
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if !allowed[h.Name] || b.Hashes[h.Name] != "" || (h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA) || h.Size < 0 {
			return nil, fmt.Errorf("unexpected archive entry %q", h.Name)
		}
		total += h.Size
		if total > maxExpanded || (h.Name == "release.json" && h.Size > maxManifest) {
			return nil, fmt.Errorf("expanded archive exceeds size limit")
		}
		mode := os.FileMode(0700)
		if h.Name == "release.json" {
			mode = 0600
		}
		f, err := os.OpenFile(filepath.Join(dir, h.Name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return nil, err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(io.MultiWriter(f, hash), tr)
		closeErr := f.Close()
		if copyErr != nil {
			return nil, copyErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		b.Hashes[h.Name] = hex.EncodeToString(hash.Sum(nil))
	}
	// Consume the gzip trailer, including its integrity check.
	if _, err = io.Copy(io.Discard, expanded); err != nil {
		return nil, err
	}
	if expanded.N <= 0 {
		return nil, fmt.Errorf("expanded archive exceeds size limit")
	}
	if len(b.Hashes) != len(allowed) {
		return nil, fmt.Errorf("incomplete archive")
	}
	data, err := os.ReadFile(filepath.Join(dir, "release.json"))
	if err != nil {
		return nil, err
	}
	inside, err := DecodeManifest(data)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(inside, m) {
		return nil, fmt.Errorf("archive manifest mismatch")
	}
	success = true
	return b, nil
}
