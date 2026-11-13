package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestServiceEscapesPaths(t *testing.T) {
	home := "/tmp/home with % and $ and <xml>"
	binary := home + "/bin/horizon-agent"
	name, linux, err := serviceDefinition("linux", home, binary)
	if err != nil || !strings.HasSuffix(name, ".service") || !bytes.Contains(linux, []byte("%%")) || !bytes.Contains(linux, []byte("$$")) || !bytes.Contains(linux, []byte("Restart=on-failure")) {
		t.Fatal(string(linux), err)
	}
	_, plist, err := serviceDefinition("darwin", home, binary)
	if err != nil {
		t.Fatal(err)
	}
	decoder := xml.NewDecoder(bytes.NewReader(plist))
	var stringsFound []string
	for {
		token, e := decoder.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		if start, ok := token.(xml.StartElement); ok && start.Name.Local == "string" {
			var value string
			if e = decoder.DecodeElement(&value, &start); e != nil {
				t.Fatal(e)
			}
			stringsFound = append(stringsFound, value)
		}
	}
	if !strings.Contains(strings.Join(stringsFound, "\n"), binary) {
		t.Fatal(stringsFound)
	}
	if _, _, err = serviceDefinition("linux", "/tmp/bad\nInjected=yes", binary); err == nil {
		t.Fatal("accepted line break")
	}
}
func TestServiceInstallPreservesExistingFile(t *testing.T) {
	t.Setenv("HORIZON_INHERITED_ACCESS", "")
	home := t.TempDir()
	suffix := ".service"
	if runtime.GOOS == "darwin" {
		suffix = ".plist"
	}
	path := filepath.Join(t.TempDir(), "service"+suffix)
	var out bytes.Buffer
	if err := service(home, "install", serviceOptions{Output: path}, &out); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(path)
	if err := service(home, "install", serviceOptions{Output: path}, &out); err == nil {
		t.Fatal("overwrote service")
	}
	data, _ := os.ReadFile(path)
	if !bytes.Equal(data, original) {
		t.Fatal("file changed")
	}
	t.Setenv("HORIZON_INHERITED_ACCESS", "read")
	if err := service(home, "install", serviceOptions{Output: path + "-other"}, &out); err == nil {
		t.Fatal("read installed service")
	}
}
func TestAgentHelpAndArgumentsDoNotCreateHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "absent")
	t.Setenv("HORIZON_HOME", home)
	for _, args := range [][]string{{"--help"}, {"service", "install", "--help"}, {"resolve-input", "--help"}} {
		var out bytes.Buffer
		if code := run(context.Background(), args, &out, &out); code != 0 {
			t.Fatal(args, code, out.String())
		}
	}
	for _, args := range [][]string{{"start", "extra"}, {"service"}, {"resolve-input"}} {
		var out bytes.Buffer
		if code := run(context.Background(), args, &out, &out); code != 2 {
			t.Fatal(args, code, out.String())
		}
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("created home")
	}
}
