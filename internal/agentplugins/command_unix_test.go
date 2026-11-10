//go:build unix

package agentplugins

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNativeCommandCancellationStopsInheritedOutputPipes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := runCommand(ctx, "sh", "-c", "sleep 5 & wait")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("cancelled native command waited %v for child output pipes", elapsed)
	}
}
