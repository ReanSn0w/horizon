package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestServiceStopUsesInstalledFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	path, err := servicePath("darwin", home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	var got []string
	runner := func(_ context.Context, _ io.Writer, name string, args ...string) error {
		got = append([]string{name}, args...)
		return nil
	}
	if err := controlServiceWith(context.Background(), "darwin", home, "stop", io.Discard, runner); err != nil {
		t.Fatal(err)
	}
	want := []string{"launchctl", "bootout", "gui/" + strconv.Itoa(os.Getuid()), path}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("command = %v, want %v", got, want)
	}
}

func TestMacLogsUsePrivateServiceFiles(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "run"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "run", "agent.stderr.log")
	if err := os.WriteFile(path, []byte("error\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var got []string
	runner := func(_ context.Context, _ io.Writer, name string, args ...string) error {
		got = append([]string{name}, args...)
		return nil
	}
	if err := logsWith(context.Background(), "darwin", home, 25, false, io.Discard, runner); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"tail", "-n", "25", path}) {
		t.Fatal(got)
	}
	if err := logsWith(context.Background(), "darwin", home, 0, false, io.Discard, runner); err == nil || !strings.Contains(err.Error(), "--lines") {
		t.Fatal(err)
	}
}

func TestServiceStartRejectsInvalidAdapterBeforeLaunch(t *testing.T) {
	home := t.TempDir()
	configuration := `default_model: chatting
models:
  chatting:
    provider: default
    model: test-model
    compact_threshold: 1000
providers:
  default:
    url: https://example.test/v1
    key: test-key
plugins:
  agent:
    adapters: [missing]
`
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	runner := func(context.Context, io.Writer, string, ...string) error {
		called = true
		return nil
	}
	err := controlServiceWith(context.Background(), "darwin", home, "start", io.Discard, runner)
	if err == nil || !strings.Contains(err.Error(), "adapter missing") || called {
		t.Fatal(err, called)
	}
}

func TestServiceRemoveDeletesOnlyDefaultFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HORIZON_INHERITED_ACCESS", "")
	home := t.TempDir()
	path, err := servicePath(runtime.GOOS, home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("managed"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := service(home, "remove", serviceOptions{}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("service file remains", err)
	}
}
