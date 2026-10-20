//go:build unix

package baumcli

import (
	"bytes"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestBootstrapDoesNotBlockOnAHookThatIsAFIFO(t *testing.T) {
	repo := initHookTestRepo(t)
	hook := filepath.Join(repo, ".git", "hooks", "pre-push")
	if err := syscall.Mkfifo(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	stubEntireBinary(t, "#!/bin/sh\nexit 0\n")

	done := make(chan error, 1)
	go func() { done <- runHooksBootstrap(t.Context(), repo, &bytes.Buffer{}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("bootstrap blocked reading a FIFO hook")
	}
}
