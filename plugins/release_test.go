package catalog

import (
	"gopkg.in/yaml.v3"
	"os"
	"testing"
)

func TestReleaseBuildsMatchCatalog(t *testing.T) {
	data, err := os.ReadFile("../.goreleaser.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Builds []struct {
			ID     string   `yaml:"id"`
			Main   string   `yaml:"main"`
			Binary string   `yaml:"binary"`
			OS     []string `yaml:"goos"`
			Arch   []string `yaml:"goarch"`
		} `yaml:"builds"`
	}
	if err = yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{"horizon": "."}
	for _, p := range Embedded().Plugins {
		expected[p.Binary] = p.Source
	}
	if len(config.Builds) != len(expected) {
		t.Fatal("release build count differs from catalog")
	}
	for _, b := range config.Builds {
		source, ok := expected[b.Binary]
		if !ok || source != b.Main || len(b.OS) != 2 || len(b.Arch) != 2 {
			t.Fatalf("unexpected build %+v", b)
		}
		delete(expected, b.Binary)
	}
	if len(expected) != 0 {
		t.Fatalf("missing builds %v", expected)
	}
}
