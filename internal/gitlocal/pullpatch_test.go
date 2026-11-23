package gitlocal

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/baum-ai/baum/bundle"
)

func TestPullPatchIsCompleteAndBounded(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r, err := Open(ctx, f.dir, Options{MaxPatchBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	pull, err := r.ResolvePull(ctx, PullRequest{Number: 1, BaseSHA: f.base, HeadSHA: f.head})
	if err != nil {
		t.Fatal(err)
	}
	patch, err := r.PullPatch(ctx, pull)
	if err != nil || !strings.Contains(string(patch), "diff --git") || !strings.Contains(string(patch), "rename from") || !strings.Contains(string(patch), "deleted file mode") {
		t.Fatalf("patch=%s err=%v", patch, err)
	}
	small, err := Open(ctx, f.dir, Options{MaxPatchBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = small.Close() }()
	if _, err := small.PullPatch(ctx, pull); !errors.Is(err, bundle.ErrOutputTooLarge) {
		t.Fatalf("oversized diff was not rejected: %v", err)
	}
	if _, err := r.PullPatch(ctx, Pull{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid input accepted: %v", err)
	}
}
