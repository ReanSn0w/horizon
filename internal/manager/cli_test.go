package manager

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestCLIProtocolAndArguments(t *testing.T) {
	home := filepath.Join(t.TempDir(), "absent")
	t.Setenv("HORIZON_HOME", home)
	for _, args := range [][]string{{"--help"}, {"horizon-plugin-metadata"}} {
		var out bytes.Buffer
		if Run(context.Background(), args, nil, &out, &out, nil) != 0 {
			t.Fatal(out.String())
		}
		if _, err := os.Stat(home); !os.IsNotExist(err) {
			t.Fatal("created home")
		}
	}
	var got Options
	var out bytes.Buffer
	code := Run(context.Background(), []string{"install", "--home", home, "--branch", "feature/install"}, nil, &out, &out, func(_ context.Context, o Options, _ io.Reader, _ io.Writer) error { got = o; return nil })
	if code != 0 || got.Home != home || got.Source.Branch != "feature/install" {
		t.Fatal(got, code)
	}
	for _, args := range [][]string{{"plugins", "remove", "manager"}, {"update", "--branch", "main"}, {"install", "--release", "--branch", "main"}, {"plugins", "install", "../bad"}} {
		if _, err := Parse(args); err == nil {
			t.Fatal(args)
		}
	}
}
func TestRepositoryCatalog(t *testing.T) {
	c, err := Catalog("../..")
	if err != nil || c["telegram"].Package != "./plugins/telegram" || c["agent:telegram"].Binary != "agent-telegram" || c["agent"].Binary != "horizon-agent" {
		t.Fatal(c, err)
	}
}
