package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ReanSn0w/horizon/internal/config"
	"gopkg.in/yaml.v3"
)

func TestBrowserSettings(t *testing.T) {
	for _, tc := range []struct {
		name, yaml string
		good       bool
	}{
		{"valid", "api_key: sentinel-secret-123\n", true},
		{"missing key", "timeout_minutes: 10\n", false},
		{"unknown", "api_key: sentinel-secret-123\nextra: value\n", false},
		{"bad timeout", "api_key: sentinel-secret-123\ntimeout_minutes: 241\n", false},
		{"bad URL", "api_key: sentinel-secret-123\napi_url: file:///tmp/a\n", false},
		{"insecure remote URL", "api_key: sentinel-secret-123\napi_url: http://example.com/api/v4\n", false},
		{"local test URL", "api_key: sentinel-secret-123\napi_url: http://127.0.0.1:1234/api/v4\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var node yaml.Node
			if err := yaml.Unmarshal([]byte(tc.yaml), &node); err != nil {
				t.Fatal(err)
			}
			_, err := loadSettings(config.Config{Plugins: map[string]yaml.Node{"browser": node}})
			if (err == nil) != tc.good {
				t.Fatal(err)
			}
			if err != nil && strings.Contains(err.Error(), "sentinel-secret-123") {
				t.Fatalf("diagnostic leaked token: %v", err)
			}
		})
	}
}

func TestBrowserLocalMetadataAndHelp(t *testing.T) {
	for _, args := range [][]string{{"horizon-plugin-metadata"}, {"--help"}} {
		var out, diagnostics bytes.Buffer
		if code := run(context.Background(), args, &bytes.Buffer{}, &out, &diagnostics); code != 0 || out.Len() == 0 || diagnostics.Len() != 0 {
			t.Fatalf("args=%v code=%d stdout=%q stderr=%q", args, code, out.String(), diagnostics.String())
		}
	}
}
