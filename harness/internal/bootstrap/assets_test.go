package bootstrap

import (
	"bytes"
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestEmbeddedResources(t *testing.T) {
	example, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(example, configTemplate) {
		t.Fatal("example differs from bundled config")
	}
	if !bytes.HasPrefix(agentsTemplate, []byte("# Global instructions for the Horizon agent\n")) {
		t.Fatal("bundled agent instructions are missing")
	}
	var config struct {
		Mode     string                    `yaml:"mode"`
		Provider struct{ URL, Key string } `yaml:"provider"`
	}
	if err := yaml.Unmarshal(configTemplate, &config); err != nil {
		t.Fatal(err)
	}
	if config.Mode != "unit" || config.Provider.URL != "" || config.Provider.Key != "" {
		t.Fatalf("unexpected initial configuration: %+v", config)
	}
	parts := bytes.SplitN(skillTemplate, []byte("---"), 3)
	if len(parts) != 3 {
		t.Fatal("missing skill frontmatter")
	}
	var metadata struct{ Name, Description string }
	if err := yaml.Unmarshal(parts[1], &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Name != "skill-creator" || metadata.Description == "" {
		t.Fatalf("metadata=%+v", metadata)
	}
}
