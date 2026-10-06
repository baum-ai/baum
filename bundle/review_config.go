package bundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// ProvisionalReviewConfigPath is where this loader looks for REVIEW.md until
// the shaping location is settled.
const ProvisionalReviewConfigPath = "REVIEW.md"

// maxReviewConfigContentBytes bounds REVIEW.md content placed in the bundle,
// independent of the overall MaxBytes budget: Fit has no documented trim
// stage for this field, so an oversized file must be capped here before it
// can ever reach that budget check.
const maxReviewConfigContentBytes = 32 << 10

// BlobFetcher is the trusted blob accessor the loader reads one file through.
// It returns ErrNotFound, or an error wrapping it, for a commit or path that
// does not exist.
type BlobFetcher interface {
	Blob(ctx context.Context, commitOID, path string) ([]byte, error)
}

// LoadReviewConfig loads REVIEW.md at commitSHA, which the builder passes as
// the pull request's base commit, or reports it absent. Content
// carries the file's own committed text, verbatim and unredacted (see
// Redact), so generation sees it up front instead of needing to
// browse for it. Binary or non-UTF-8 content is reported present, with a
// digest, but no content — the same "cannot fabricate text" rule an
// unreadable diff hunk follows.
func LoadReviewConfig(ctx context.Context, fetcher BlobFetcher, commitSHA string) (*ReviewConfig, error) {
	content, err := fetcher.Blob(ctx, commitSHA, ProvisionalReviewConfigPath)
	if errors.Is(err, ErrNotFound) {
		return &ReviewConfig{Present: false}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("loading REVIEW.md: %w", err)
	}
	digest := sha256.Sum256(content)
	config := &ReviewConfig{Present: true, Digest: hex.EncodeToString(digest[:])}
	if utf8.Valid(content) {
		config.Content = string(content)
	}
	return config, nil
}

// ChangesReviewConfig reports whether the diff adds, edits, renames or
// removes REVIEW.md: a rename away from the location carries it as OldPath.
func ChangesReviewConfig(hunks []Hunk) bool {
	for _, hunk := range hunks {
		if hunk.Path == ProvisionalReviewConfigPath || hunk.OldPath == ProvisionalReviewConfigPath {
			return true
		}
	}
	return false
}

// CapReviewConfigContent bounds config.Content to its leading bytes in
// place, reporting whether it cut anything.
func CapReviewConfigContent(config *ReviewConfig) bool {
	if len(config.Content) <= maxReviewConfigContentBytes {
		return false
	}
	config.Content = strings.ToValidUTF8(config.Content[:maxReviewConfigContentBytes], "")
	return true
}
