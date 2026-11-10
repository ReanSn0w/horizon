package daemon

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func shortHome(t *testing.T) string {
	t.Helper()
	p, e := os.MkdirTemp("", "hd-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(p) })
	return p
}
func TestServerLifecycle(t *testing.T) {
	h := shortHome(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, h, nil) }()
	for i := 0; i < 100; i++ {
		if Hello(ctx, h) == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e := Hello(ctx, h); e != nil {
		t.Fatal(e)
	}
	if e := Serve(ctx, h, nil); e == nil {
		t.Fatal("second server accepted")
	}
	if i, e := os.Stat(Socket(h)); e != nil || i.Mode().Perm() != 0600 {
		t.Fatal(i, e)
	}
	if _, e := Call(ctx, h, Request{ID: "stop", Method: "stop"}); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("stop hung")
	}
}
func TestPreserveNonSocket(t *testing.T) {
	h := shortHome(t)
	os.MkdirAll(filepath.Dir(Socket(h)), 0700)
	os.WriteFile(Socket(h), []byte("keep"), 0600)
	if Serve(context.Background(), h, nil) == nil {
		t.Fatal("accepted file")
	}
	b, _ := os.ReadFile(Socket(h))
	if string(b) != "keep" {
		t.Fatal("removed file")
	}
}

func TestMalformedAndIncompatibleRequests(t *testing.T) {
	h := shortHome(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, h, nil) }()
	defer func() { cancel(); <-done }()
	for i := 0; i < 100; i++ {
		if Hello(ctx, h) == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, input := range []string{"not-json\n", `{"version":99,"id":"bad","method":"hello","caller":{}}` + "\n", strings.Repeat("x", MaxFrame+1) + "\n"} {
		c, e := net.Dial("unix", Socket(h))
		if e != nil {
			t.Fatal(e)
		}
		c.SetDeadline(time.Now().Add(time.Second))
		go func() { io.WriteString(c, input) }()
		var out Response
		if e = frame(c, &out); e != nil {
			t.Fatal(e)
		}
		c.Close()
		if out.OK || out.Error == nil {
			t.Fatal(out)
		}
	}
}
func TestRecoverStaleSocket(t *testing.T) {
	h := shortHome(t)
	os.MkdirAll(filepath.Dir(Socket(h)), 0700)
	l, e := net.ListenUnix("unix", &net.UnixAddr{Name: Socket(h), Net: "unix"})
	if e != nil {
		t.Fatal(e)
	}
	l.SetUnlinkOnClose(false)
	l.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, h, nil) }()
	defer func() { cancel(); <-done }()
	for i := 0; i < 100; i++ {
		if Hello(ctx, h) == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("stale socket not recovered")
}
