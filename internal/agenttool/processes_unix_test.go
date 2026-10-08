//go:build unix

package agenttool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestManagedProcessYieldsAndReturnsFinalOutput(t *testing.T) {
	manager := newManagedProcesses()
	defer manager.close()
	dir := t.TempDir()
	args := json.RawMessage(`{"command":"printf first; sleep 1; printf second","timeout_ms":null,"max_output_chars":null}`)
	started := manager.start(context.Background(), args, environment{workspace: dir, artifactsDir: filepath.Join(dir, "artifacts"), callID: "call-1"}, 10*time.Millisecond, 16000)
	first, ok := started.Data.(managedShellData)
	if !ok || first.Status != "running" || first.ProcessID == "" {
		t.Fatalf("start = %+v", started)
	}
	finished := manager.wait(context.Background(), first.ProcessID, 3*time.Second, 16000)
	final, ok := finished.Data.(managedShellData)
	if !ok || final.Status != "completed" || final.ExitCode == nil || *final.ExitCode != 0 {
		t.Fatalf("wait = %+v", finished)
	}
	if first.Stdout+final.Stdout != "firstsecond" {
		t.Fatalf("output chunks = %q + %q", first.Stdout, final.Stdout)
	}
	contents, err := os.ReadFile(final.StdoutPath)
	if err != nil || string(contents) != "firstsecond" {
		t.Fatalf("artifact = %q, %v", contents, err)
	}
}
