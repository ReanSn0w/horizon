package distribution

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestManifestRoundTrip(t *testing.T) {
	m, err := NewManifest("v0.1.0", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "./plugins/") {
		t.Fatal("source paths leaked")
	}
	parsed, err := DecodeManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Archives["linux_arm64"] != "horizon_0.1.0_linux_arm64.tar.gz" {
		t.Fatal(parsed)
	}
}
