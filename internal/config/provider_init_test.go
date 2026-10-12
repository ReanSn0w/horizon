package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeProviderAttributionFieldsPreservesExistingTitle(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "config.yaml")
	initial := "# keep\nmode: unit\nprovider:\n  url: https://openrouter.ai/api/v1\n  key: private-key\n  app_title: My Agent\n"
	if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := MergeProviderAttributionFields(home)
	data, readErr := os.ReadFile(path)
	if err != nil || readErr != nil || !changed || !strings.Contains(string(data), "# keep") || !strings.Contains(string(data), "app_title: My Agent") || !strings.Contains(string(data), "app_url: \"\"") {
		t.Fatalf("merge: changed=%t err=%v config=%q read=%v", changed, err, data, readErr)
	}
	changed, err = MergeProviderAttributionFields(home)
	again, _ := os.ReadFile(path)
	if err != nil || changed || string(data) != string(again) {
		t.Fatalf("second merge changed config: changed=%t err=%v", changed, err)
	}
}
