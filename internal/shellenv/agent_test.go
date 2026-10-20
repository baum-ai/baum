//go:build unix

package shellenv

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func listenSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ag")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "agent.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return path
}

const stale = "/tmp/ssh-gone/agent.1"

func TestAgentKeepsAUsableSocket(t *testing.T) {
	live := listenSocket(t)
	t.Setenv("SSH_AUTH_SOCK", live)
	a := &Agent{Resolve: func(context.Context) (string, error) {
		t.Error("asked the shell although the socket works")
		return "", nil
	}}
	if got := a.Socket(context.Background()); got != live {
		t.Fatal(got)
	}
}

func TestAgentWithoutAnySocketNeverRunsTheShell(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	a := &Agent{Resolve: func(context.Context) (string, error) {
		t.Error("ran the login shell for a user without an agent")
		return "", nil
	}}
	for range 3 {
		if got := a.Socket(context.Background()); got != "" {
			t.Fatal(got)
		}
	}
}

func TestAgentReplacesAStaleSocketWithTheShells(t *testing.T) {
	live := listenSocket(t)
	t.Setenv("SSH_AUTH_SOCK", stale)
	var calls atomic.Int32
	a := &Agent{Resolve: func(context.Context) (string, error) { calls.Add(1); return live, nil }}
	if got := a.Socket(context.Background()); got != live || os.Getenv("SSH_AUTH_SOCK") != live {
		t.Fatalf("socket %q, env %q", got, os.Getenv("SSH_AUTH_SOCK"))
	}
	if got := a.Socket(context.Background()); got != live || calls.Load() != 1 {
		t.Fatalf("socket %q after %d shell runs", got, calls.Load())
	}
}

func TestAgentNeverUsesAStaleSocket(t *testing.T) {
	for _, tc := range []struct {
		name    string
		resolve func(context.Context) (string, error)
		retried bool
	}{
		{"shell has the same stale one", func(context.Context) (string, error) { return stale, nil }, false},
		{"shell has none", func(context.Context) (string, error) { return "", nil }, false},
		{"not a socket", func(context.Context) (string, error) { return os.Args[0], nil }, false},
		{"shell fails", func(context.Context) (string, error) { return "", errors.New("timed out") }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(1000, 0)
			t.Setenv("SSH_AUTH_SOCK", stale)
			calls := 0
			a := &Agent{Now: func() time.Time { return now }, Resolve: func(ctx context.Context) (string, error) { calls++; return tc.resolve(ctx) }}
			if got := a.Socket(context.Background()); got != "" {
				t.Fatalf("used %q", got)
			}
			if _, ok := os.LookupEnv("SSH_AUTH_SOCK"); ok {
				t.Fatal("stale socket left in the environment")
			}
			if got := a.Socket(context.Background()); got != "" || calls != 1 {
				t.Fatalf("%q after %d runs", got, calls)
			}
			// An answer is remembered; only a failed run is retried later.
			now = now.Add(2 * DefaultAgentRetry)
			a.Socket(context.Background())
			if want := map[bool]int{false: 1, true: 2}[tc.retried]; calls != want {
				t.Fatalf("%d runs, want %d", calls, want)
			}
		})
	}
}

// Concurrent callers share one shell run, and a caller whose socket works
// does not wait for it.
func TestAgentShellRunIsSharedAndDoesNotBlock(t *testing.T) {
	live := listenSocket(t)
	t.Setenv("SSH_AUTH_SOCK", stale)
	release := make(chan struct{})
	var calls atomic.Int32
	a := &Agent{Resolve: func(context.Context) (string, error) {
		calls.Add(1)
		<-release
		return live, nil
	}}
	var wg sync.WaitGroup
	results := make([]string, 4)
	for i := range results {
		wg.Go(func() { results[i] = a.Socket(context.Background()) })
	}
	for calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	// While the shell runs, a caller that finds a working socket returns at
	// once.
	other := listenSocket(t)
	_ = os.Setenv("SSH_AUTH_SOCK", other)
	done := make(chan string, 1)
	go func() { done <- a.Socket(context.Background()) }()
	select {
	case got := <-done:
		if got != other {
			t.Fatalf("got %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked behind the shell run")
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("%d shell runs", calls.Load())
	}
	// A caller that started after the socket was replaced uses that one.
	for _, got := range results {
		if got != live && got != other {
			t.Fatalf("results %v", results)
		}
	}
}
