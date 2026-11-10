package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/ReanSn0w/horizon/internal/maintenance"
)

type Handler func(context.Context, Request) (any, error)

// Serve holds the singleton and maintenance locks for the full foreground lifetime.
func Serve(ctx context.Context, home string, handle Handler) error {
	return serve(ctx, home, handle, nil)
}
func serve(ctx context.Context, home string, handle Handler, setup func(context.Context) (Handler, func(), error)) error {
	if len(Socket(home)) > 100 {
		return errors.New("daemon Unix socket path exceeds 100 bytes; use a shorter home path")
	}
	if err := os.MkdirAll(filepath.Join(home, "run"), 0700); err != nil {
		return err
	}
	info, err := os.Lstat(filepath.Join(home, "run"))
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("run must be a real directory")
	}
	if err = os.Chmod(filepath.Join(home, "run"), 0700); err != nil {
		return err
	}
	unlock, err := maintenance.Lock(filepath.Join(home, "run", "daemon.lock"), true)
	if err != nil {
		return err
	}
	defer unlock()
	release, err := maintenance.Acquire(home, false)
	if err != nil {
		return err
	}
	defer release()
	if info, err := os.Lstat(Socket(home)); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("daemon socket path is occupied by a non-socket")
		}
		probe, e := net.DialTimeout("unix", Socket(home), 200*time.Millisecond)
		if e == nil {
			probe.Close()
			return errors.New("daemon socket is already active")
		}
		if !errors.Is(e, syscall.ECONNREFUSED) {
			return errors.New("cannot prove daemon socket is stale")
		}
		if err = os.Remove(Socket(home)); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: Socket(home), Net: "unix"})
	if err != nil {
		return err
	}
	defer listener.Close()
	if err = os.Chmod(Socket(home), 0600); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if setup != nil {
		var cleanup func()
		handle, cleanup, err = setup(ctx)
		if err != nil {
			return err
		}
		defer cleanup()
	}
	closeListener := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer closeListener()
	var wg sync.WaitGroup
	defer wg.Wait()
	slots := make(chan struct{}, 64)
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			defer conn.Close()
			stop := context.AfterFunc(ctx, func() { conn.Close() })
			defer stop()
			_ = conn.SetDeadline(time.Now().Add(35 * time.Second))
			var r Request
			err := frame(conn, &r)
			var data any
			if err != nil {
				err = fail("invalid_request", "expected a bounded JSON frame")
			} else if r.Version != Version {
				err = fail("protocol_mismatch", "unsupported version")
			} else {
				switch r.Method {
				case "hello":
					data = map[string]any{"version": Version, "capabilities": []string{"tasks", "events", "adapters"}}
				case "stop":
					data = map[string]bool{"stopping": true}
				default:
					if handle == nil {
						err = fail("unknown_method", "unsupported method")
					} else {
						data, err = handle(ctx, r)
					}
				}
			}
			_ = json.NewEncoder(conn).Encode(reply(r, data, err))
			if err == nil && r.Method == "stop" {
				cancel()
			}
		}()
	}
}
