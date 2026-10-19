package filelock

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestHolderHelper holds the lock named by FILELOCK_HELPER in its own
// process, probes it from that same process, and keeps it until stdin closes.
func TestHolderHelper(t *testing.T) {
	path := os.Getenv("FILELOCK_HELPER")
	if path == "" {
		t.Skip("lock holder process")
	}
	l, err := TryLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if held, err := Held(context.Background(), path); err != nil || !held {
			t.Fatalf("own lock not seen as held: %v %v", held, err)
		}
	}
	fmt.Println("ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
	if err = l.Release(); err != nil {
		t.Fatal(err)
	}
}

// A holder probing its own lock must keep it against other processes: on
// Unix, closing any descriptor of the file would drop the process's locks.
func TestHolderProbingItsOwnLockKeepsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^TestHolderHelper$")
	cmd.Env = append(os.Environ(), "FILELOCK_HELPER="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	ready := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if scanner.Text() == "ready" {
				ready <- true
			}
		}
		close(ready)
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("holder exited")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("holder not ready")
	}
	if l, err := TryLock(context.Background(), path); !errors.Is(err, ErrLocked) {
		_ = l.Release()
		t.Fatalf("took a lock another process holds: %v", err)
	}
	if held, err := Held(context.Background(), path); err != nil || !held {
		t.Fatalf("held = %v, %v", held, err)
	}
	_ = stdin.Close()
	l, err := Wait(context.Background(), path, 30*time.Second)
	if err != nil {
		t.Fatal("lock not released after the holder let go:", err)
	}
	_ = l.Release()
}

func TestInProcessLocksExclude(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	first, err := TryLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = TryLock(context.Background(), path); !errors.Is(err, ErrLocked) {
		t.Fatal(err)
	}
	if err = first.Release(); err != nil {
		t.Fatal(err)
	}
	if held, err := Held(context.Background(), path); err != nil || held {
		t.Fatal(held, err)
	}
	if held, err := Held(context.Background(), filepath.Join(t.TempDir(), "missing")); err != nil || held {
		t.Fatal(held, err)
	}
}
