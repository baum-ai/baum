//go:build unix

package shellenv

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
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

func TestAgentReplacesAStaleSocketWithTheShells(t *testing.T) {
	live := listenSocket(t)
	t.Setenv("SSH_AUTH_SOCK", "/tmp/ssh-gone/agent.1")
	calls := 0
	a := &Agent{Resolve: func(context.Context) (string, error) { calls++; return live, nil }}
	if got := a.Socket(context.Background()); got != live || os.Getenv("SSH_AUTH_SOCK") != live {
		t.Fatalf("socket %q, env %q", got, os.Getenv("SSH_AUTH_SOCK"))
	}
	if got := a.Socket(context.Background()); got != live || calls != 1 {
		t.Fatalf("socket %q after %d shell runs", got, calls)
	}
}

func TestAgentNeverUsesAStaleSocket(t *testing.T) {
	now := time.Unix(1000, 0)
	for name, resolve := range map[string]func(context.Context) (string, error){
		"shell has the same stale one": func(context.Context) (string, error) { return "/tmp/ssh-gone/agent.1", nil },
		"shell has none":               func(context.Context) (string, error) { return "", nil },
		"shell fails":                  func(context.Context) (string, error) { return "", errors.New("timed out") },
		"not a socket":                 func(context.Context) (string, error) { return os.Args[0], nil },
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("SSH_AUTH_SOCK", "/tmp/ssh-gone/agent.1")
			calls := 0
			a := &Agent{Now: func() time.Time { return now }, Resolve: func(ctx context.Context) (string, error) { calls++; return resolve(ctx) }}
			if got := a.Socket(context.Background()); got != "" {
				t.Fatalf("used %q", got)
			}
			if _, ok := os.LookupEnv("SSH_AUTH_SOCK"); ok {
				t.Fatal("stale socket left in the environment")
			}
			// Within the retry interval the shell is not run again.
			if got := a.Socket(context.Background()); got != "" || calls != 1 {
				t.Fatalf("%q after %d runs", got, calls)
			}
			now = now.Add(2 * DefaultAgentRetry)
			a.Socket(context.Background())
			if calls != 2 {
				t.Fatalf("not retried: %d", calls)
			}
		})
	}
}
