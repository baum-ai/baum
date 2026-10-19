package daemon

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

// Log sizes: the current file rotates at MaxLogBytes, and LogBackups older
// files are kept, so the logs never exceed (LogBackups+1)*MaxLogBytes.
const (
	MaxLogBytes = 1 << 20
	LogBackups  = 2
)

// RotatingFile is a private, size-bounded log file.
type RotatingFile struct {
	mu   sync.Mutex
	path string
	max  int64
	keep int
	f    *os.File
	size int64
}

// OpenLog opens path for appending, with mode 0600, rotating it at max bytes
// and keeping keep older files (path.1 newest).
func OpenLog(path string, max int64, keep int) (*RotatingFile, error) {
	r := &RotatingFile{path: path, max: max, keep: keep}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *RotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	r.f, r.size = f, info.Size()
	return nil
}

func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return 0, errors.New("log closed")
	}
	if r.size > 0 && r.size+int64(len(p)) > r.max {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *RotatingFile) rotate() error {
	if err := r.f.Close(); err != nil {
		return err
	}
	r.f = nil
	for i := r.keep; i >= 1; i-- {
		from := r.path
		if i > 1 {
			from = fmt.Sprintf("%s.%d", r.path, i-1)
		}
		if err := os.Rename(from, fmt.Sprintf("%s.%d", r.path, i)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if r.keep == 0 {
		if err := os.Remove(r.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return r.open()
}

// Close closes the file.
func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}
