//go:build unix

package gitlocal

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO in place of a ref file does not stall baum.
func TestReadRegularRefusesAFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "HEAD")
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Skip(err)
	}
	done := make(chan string, 1)
	go func() { done <- readRegular(path, maxRefFileBytes) }()
	select {
	case got := <-done:
		if got != "" {
			t.Fatalf("read %q from a FIFO", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO blocked")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}
