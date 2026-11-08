package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestServiceErrorWriterBoundsSameInode(t *testing.T) {
	f, err := os.OpenFile(filepath.Join(t.TempDir(), "stderr"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := &serviceErrorWriter{file: f, limit: 8}
	for _, value := range []string{"123456", "abcdef", "1234567890"} {
		if n, err := w.Write([]byte(value)); err != nil || n != len(value) {
			t.Fatal(n, err)
		}
	}
	data, _ := os.ReadFile(f.Name())
	if string(data) != "34567890" {
		t.Fatal(string(data))
	}
}
