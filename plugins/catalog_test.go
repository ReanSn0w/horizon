package catalog

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestCatalogMatchesSources(t *testing.T) {
	c := Embedded()
	files, err := filepath.Glob("*/main.go")
	if err != nil {
		t.Fatal(err)
	}
	var actual []string
	for _, f := range files {
		actual = append(actual, filepath.Dir(f))
	}
	expected := c.IDs()
	slices.Sort(expected)
	slices.Sort(actual)
	if !reflect.DeepEqual(expected, actual) {
		t.Fatalf("catalog %v, sources %v", expected, actual)
	}
	for _, p := range c.Plugins {
		if p.Source != "./plugins/"+p.ID {
			t.Fatalf("source %q", p.Source)
		}
		if _, err := os.Stat(filepath.Join("..", p.Source, "main.go")); err != nil {
			t.Fatal(err)
		}
	}
}
func TestDependencyValidation(t *testing.T) {
	c := Embedded()
	got, err := c.Closure([]string{"telegram", "decision"})
	if err != nil || !reflect.DeepEqual(got, []string{"decision", "telegram"}) {
		t.Fatalf("%v %v", got, err)
	}
	for _, tc := range []struct{ old, new string }{
		{"requires: [decision]", "requires: [missing]"},
		{"requires: [decision]", "requires: [telegram]"},
		{"id: telegram", "id: decision"},
		{"id: telegram", "id: resume"},
		{"binary: horizon-telegram", "binary: horizon-decision"},
	} {
		if _, err := Parse([]byte(strings.Replace(string(source), tc.old, tc.new, 1))); err == nil {
			t.Fatalf("accepted %s", tc.new)
		}
	}
}
