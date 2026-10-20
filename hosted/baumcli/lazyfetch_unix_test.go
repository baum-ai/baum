//go:build !windows

package baumcli

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// TestReadsDispatcherFromBoundedWhenDescendantHoldsPipe covers a descendant
// that outlives the command and keeps its stdout open, as a lazy-fetch child
// can on Windows where only the parent is killed. The read must still end at
// the deadline.
func TestReadsDispatcherFromBoundedWhenDescendantHoldsPipe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	// The shell exits at once; the background sleep inherits stdout and keeps
	// the pipe open. No proc.Configure, so nothing kills the sleep.
	cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 4 & exit 0")
	start := time.Now()
	if readsDispatcherFrom(ctx, cmd, "") {
		t.Fatal("found a dispatcher in empty output")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("read took %s, want it bounded by the 300ms deadline", elapsed)
	}
}
