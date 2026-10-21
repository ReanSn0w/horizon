package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ReanSn0w/horizon/internal/buildinfo"
	catalog "github.com/ReanSn0w/horizon/plugins"
	flags "github.com/umputun/go-flags"
)

func TestVersionWithoutHomeOrPlugins(t *testing.T) {
	home := filepath.Join(t.TempDir(), "absent")
	for _, args := range [][]string{{"--version"}, {"--home", home, "--verbose", "--version"}, {"--version", "--home", home}} {
		code, out, errout := runApp(t, "", args...)
		if code != 0 || !strings.Contains(out, buildinfo.Version) || errout != "" {
			t.Fatalf("%v: %d %s %s", args, code, out, errout)
		}
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("home created: %v", err)
	}
	for _, args := range [][]string{{"--version", "resume"}, {"--version", "--bad"}, {"--version", "--home"}} {
		code, _, _ := runApp(t, "", args...)
		if code != ExitUsage {
			t.Fatalf("%v: %d", args, code)
		}
	}
	if versionRequest([]string{"--home", "--version"}) || versionRequest([]string{"update", "--version", "v1.0.0"}) {
		t.Fatal("consumed non-global version")
	}
}
func TestCatalogReservedCommands(t *testing.T) {
	a := New(nil, nil, nil)
	o := globalOptions{}
	p := flags.NewParser(&o, flags.None)
	if err := a.addCommands(p, &o); err != nil {
		t.Fatal(err)
	}
	actual := commandNames(p)
	expected := slices.Clone(catalog.Reserved)
	slices.Sort(actual)
	slices.Sort(expected)
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("reserved commands: %v != %v", actual, expected)
	}
}
