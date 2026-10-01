package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestProcessCancellationKillsIgnoringTerm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	output, err := processRunner("/bin/sh", "")(ctx, "", []string{"-c", "printf %s $$ > '" + strings.ReplaceAll(path, "'", "'\"'\"'") + "'; trap '' TERM; exec sleep 30"}, "")
	if !errors.Is(err, context.DeadlineExceeded) || output != "" || time.Since(started) > 4*time.Second {
		t.Fatalf("cancellation: %v %q %s", err, output, time.Since(started))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("managed process survived cancellation: %v", err)
	}
}
