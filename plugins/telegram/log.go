package main

import (
	"io"
	"os"
	"path/filepath"
	"sync"
)

type rotatingLog struct {
	mu    sync.Mutex
	path  string
	limit int64
}

func (l *rotatingLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0700); err != nil {
		return 0, err
	}
	if info, err := os.Stat(l.path); err == nil && info.Size()+int64(len(p)) > l.limit {
		if err = os.Rename(l.path, l.path+".1"); err != nil {
			return 0, err
		}
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return 0, err
	}
	return f.Write(p)
}

type synchronizedWriter struct {
	mu  sync.Mutex
	out io.Writer
}

func (w *synchronizedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.out.Write(p)
}
