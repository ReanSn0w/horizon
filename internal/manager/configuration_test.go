package manager

import (
	"bytes"
	"context"
	"errors"
	"github.com/ReanSn0w/horizon/internal/config"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInstallUsesSelectedRevisionCoreTemplate(t *testing.T) {
	e := fixtureEngine(t)
	path := filepath.Join(e.repository, "config.example.yaml")
	data, _ := os.ReadFile(path)
	data = bytes.Replace(data, []byte("max_model_requests: 128"), []byte("max_model_requests: 73 # selected revision"), 1)
	os.WriteFile(path, data, 0600)
	commitFixture(t, e.repository)
	home := t.TempDir()
	if err := e.execute(context.Background(), Options{Command: "install", Home: home, Yes: true, Source: &config.Source{Type: "branch", Branch: "feature/install"}}, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	installed, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
	if !bytes.Contains(installed, []byte("max_model_requests: 73 # selected revision")) {
		t.Fatal("installed running manager defaults instead of source defaults")
	}
}

func TestUpdateCoreConfigPreservesSymlinkRollsBackAndDoesNotRewrite(t *testing.T) {
	e, home := installFixture(t)
	configPath := filepath.Join(home, "config.yaml")
	target := filepath.Join(t.TempDir(), "custom.yaml")
	data, _ := os.ReadFile(configPath)
	lines := strings.Split(string(data), "\n")
	kept := lines[:0]
	for _, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "max_input_tokens:") {
			kept = append(kept, line)
		}
	}
	data = []byte(strings.Join(kept, "\n"))
	data = bytes.Replace(data, []byte("max_model_requests: 128"), []byte("max_model_requests: 0 # personal cap"), 1)
	os.WriteFile(configPath, data, 0640)
	os.Chmod(configPath, 0640)
	os.Rename(configPath, target)
	os.Symlink(target, configPath)
	sourceTemplate := filepath.Join(e.repository, "config.example.yaml")
	template, _ := os.ReadFile(sourceTemplate)
	template = bytes.Replace(template, []byte("max_input_tokens: 2000000"), []byte("max_input_tokens: 123456 # new default"), 1)
	os.WriteFile(sourceTemplate, template, 0600)
	commitFixture(t, e.repository)
	reached := false
	e.afterReplace = func(int) error {
		current, _ := os.ReadFile(target)
		if bytes.Contains(current, []byte("max_input_tokens: 123456")) {
			reached = true
			return errors.New("injected failure after configuration update")
		}
		return nil
	}
	if err := runOperation(e, home, "update", ""); err == nil || !reached {
		t.Fatal("rollback path was not tested", err)
	}
	restored, _ := os.ReadFile(target)
	if !bytes.Equal(restored, data) {
		t.Fatal("rollback lost original config")
	}
	e.afterReplace = nil
	var out bytes.Buffer
	if err := e.execute(context.Background(), Options{Command: "update", Home: home}, nil, &out); err != nil {
		t.Fatal(err)
	}
	updated, _ := os.ReadFile(target)
	for _, text := range []string{"max_model_requests: 0 # personal cap", "max_input_tokens: 123456 # new default"} {
		if !bytes.Contains(updated, []byte(text)) {
			t.Fatal("missing setting", text)
		}
	}
	info, _ := os.Lstat(configPath)
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced")
	}
	info, _ = os.Stat(target)
	if info.Mode().Perm() != 0640 {
		t.Fatal("permissions changed")
	}
	at := time.Unix(123456789, 0)
	os.Chtimes(target, at, at)
	out.Reset()
	if err := e.execute(context.Background(), Options{Command: "install", Home: home, Yes: true}, nil, &out); err != nil {
		t.Fatal(err)
	}
	info, _ = os.Stat(target)
	if !info.ModTime().Equal(at) || !strings.Contains(out.String(), "Configuration already up to date") {
		t.Fatal("reinstall rewrote unchanged configuration", out.String())
	}
}
