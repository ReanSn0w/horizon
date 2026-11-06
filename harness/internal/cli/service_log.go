package cli

import (
	"io"
	"os"
	"sync"
)

// ServiceErrorWriter bounds the launchd fallback without renaming the inode
// whose descriptor launchd has already opened. Ordinary CLI stderr is unchanged.
func ServiceErrorWriter(f *os.File) io.Writer {
	if os.Getenv("HORIZON_SERVICE_LOG") == "" {
		return f
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return f
	}
	return &serviceErrorWriter{file: f, limit: 1024 * 1024}
}

type serviceErrorWriter struct {
	mu    sync.Mutex
	file  *os.File
	limit int64
}

func (w *serviceErrorWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	if int64(n) > w.limit {
		p = p[int64(n)-w.limit:]
	}
	info, err := w.file.Stat()
	if err != nil {
		return 0, err
	}
	if info.Size()+int64(len(p)) > w.limit {
		if err = w.file.Truncate(0); err != nil {
			return 0, err
		}
	}
	if _, err = w.file.Seek(0, io.SeekEnd); err != nil {
		return 0, err
	}
	if _, err = w.file.Write(p); err != nil {
		return 0, err
	}
	return n, nil
}
