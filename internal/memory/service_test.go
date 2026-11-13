package memory

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestWatchCancellationWithoutWrites(t *testing.T) {
	store := newStore(t.TempDir(), t.TempDir(), defaultSettings("test"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var diagnostics bytes.Buffer
	err := watch(ctx, store, time.Second, func(context.Context, string, string, []note) (string, error) {
		t.Fatal("empty watcher invoked summarizer")
		return "", nil
	}, &diagnostics)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
