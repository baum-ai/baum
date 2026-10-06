package bundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestLoadReviewConfigAbsent(t *testing.T) {
	config, err := LoadReviewConfig(context.Background(), fakeBlobFetcher{err: ErrNotFound}, "basesha")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if config.Present || config.Digest != "" || config.Content != "" {
		t.Fatalf("expected an empty absent config, got %+v", config)
	}
}

func TestLoadReviewConfigPresentCarriesContentAndDigest(t *testing.T) {
	text := "Review guidance: focus on the payments module."
	config, err := LoadReviewConfig(context.Background(), fakeBlobFetcher{content: []byte(text)}, "basesha")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sum := sha256.Sum256([]byte(text))
	wantDigest := hex.EncodeToString(sum[:])
	if !config.Present {
		t.Fatalf("expected present config, got %+v", config)
	}
	if config.Digest != wantDigest {
		t.Fatalf("digest = %q, want %q", config.Digest, wantDigest)
	}
	if config.Content != text {
		t.Fatalf("content = %q, want %q", config.Content, text)
	}
}

func TestLoadReviewConfigBinaryContentOmitsTextButKeepsDigest(t *testing.T) {
	binary := []byte{0x00, 0x01, 0x02, 0xff}
	config, err := LoadReviewConfig(context.Background(), fakeBlobFetcher{content: binary}, "basesha")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !config.Present {
		t.Fatalf("expected present config for unreadable content, got %+v", config)
	}
	if config.Content != "" {
		t.Fatalf("expected no content for non-UTF-8 REVIEW.md, got %q", config.Content)
	}
	sum := sha256.Sum256(binary)
	if config.Digest != hex.EncodeToString(sum[:]) {
		t.Fatalf("digest should still be computed from the raw bytes, got %q", config.Digest)
	}
}

func TestChangesReviewConfigMatchesPathOrPreviousPath(t *testing.T) {
	for _, tc := range []struct {
		hunk Hunk
		want bool
	}{
		{Hunk{Path: "REVIEW.md", Change: "modified"}, true},
		{Hunk{Path: "REVIEW.md", Change: "added"}, true},
		{Hunk{Path: "REVIEW.md", Change: "removed"}, true},
		{Hunk{Path: "docs/REVIEW.md", OldPath: "REVIEW.md", Change: "renamed"}, true},
		{Hunk{Path: "docs/REVIEW.md", Change: "modified"}, false},
		{Hunk{Path: "main.go", Change: "modified"}, false},
	} {
		if got := ChangesReviewConfig([]Hunk{{Path: "other.go"}, tc.hunk}); got != tc.want {
			t.Fatalf("ChangesReviewConfig(%+v) = %t, want %t", tc.hunk, got, tc.want)
		}
	}
}

func TestLoadReviewConfigPropagatesOtherErrors(t *testing.T) {
	sentinel := errors.New("git service unavailable")
	if _, err := LoadReviewConfig(context.Background(), fakeBlobFetcher{err: sentinel}, "basesha"); !errors.Is(err, sentinel) {
		t.Fatalf("expected wrapped sentinel error, got %v", err)
	}
}

// TestBuildCapsOversizedReviewConfigContent guards the byte cap that exists
// specifically because Fit has no stage that trims
// ReviewConfig.Content: an oversized file must never reach that budget
// check untrimmed. It drives CapReviewConfigContent directly rather than
// reimplementing the cap, so it stays honest if build's call to it or the
// cap's own logic changes.
func TestBuildCapsOversizedReviewConfigContent(t *testing.T) {
	oversized := strings.Repeat("a", maxReviewConfigContentBytes+100)
	config, err := LoadReviewConfig(context.Background(), fakeBlobFetcher{content: []byte(oversized)}, "basesha")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !CapReviewConfigContent(config) {
		t.Fatalf("expected CapReviewConfigContent to report a cut for oversized content")
	}
	if len(config.Content) != maxReviewConfigContentBytes {
		t.Fatalf("content length = %d, want exactly the cap %d", len(config.Content), maxReviewConfigContentBytes)
	}
	if CapReviewConfigContent(config) {
		t.Fatalf("expected no further cut once content is within the cap")
	}
}

// TestRedactLeavesReviewConfigContentUnmasked guards against re-adding
// ReviewConfig.Content to redact's field list: REVIEW.md has no audience but
// the review agent itself, which can already read the same unredacted file
// directly off the checkout, so masking only the bundled copy would just
// create an inconsistency between the two, not add any protection.
func TestRedactLeavesReviewConfigContentUnmasked(t *testing.T) {
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatalf("NewRedactor: %v", err)
	}
	email := "maintainer@example.com"
	original := "Questions go to " + email + " before merge."
	bundle := &Bundle{ReviewConfig: &ReviewConfig{Present: true, Content: original}}
	if err := Redact(context.Background(), bundle, redactor); err != nil {
		t.Fatalf("redact: %v", err)
	}
	if bundle.ReviewConfig.Content != original {
		t.Fatalf("expected REVIEW.md content to pass through unredacted, got %q", bundle.ReviewConfig.Content)
	}
}

// fakeBlobFetcher returns content for any path, or err when it is set,
// simulating LoadReviewConfig's BlobFetcher outcomes.
type fakeBlobFetcher struct {
	content []byte
	err     error
}

func (f fakeBlobFetcher) Blob(context.Context, string, string) ([]byte, error) {
	return f.content, f.err
}
