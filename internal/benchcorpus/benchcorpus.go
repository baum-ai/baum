// Package benchcorpus supplies the review store that index benchmarks read: a
// copy of a real store named by BAUM_BENCH_STORE, or a deterministic synthetic
// one shaped like real stores. It is for benchmarks only; nothing imports it
// in production.
package benchcorpus

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/store"
)

// Env names a directory holding the versions.sqlite to measure. It is opened
// read-only and never written.
const Env = "BAUM_BENCH_STORE"

// Repository is the identity of every synthetic pull request.
const Repository = "acme/widgets"

// Synthetic corpus shape, close to a busy real clone: documents of about
// 150 KB, most of it patch text under index.hunks[].diff.
const (
	PullRequests = 64
	// Version counts cycle through 1..MaxVersions.
	MaxVersions = 4
	hunks       = 56
	diffLines   = 14
)

// Root returns a directory with a versions.sqlite and a label for reports. A
// real store is used as is; otherwise a synthetic one is published into
// b.TempDir.
func Root(b testing.TB) (root, label string) {
	b.Helper()
	if dir := os.Getenv(Env); dir != "" {
		if _, err := os.Stat(filepath.Join(dir, "versions.sqlite")); err != nil {
			b.Fatalf("%s=%s: %v", Env, dir, err)
		}
		return dir, "real"
	}
	root = b.TempDir()
	if err := Generate(root); err != nil {
		b.Fatal(err)
	}
	return root, "synthetic"
}

// ProjectedRoot is Root with the summaries projection present. A real store is
// copied into b.TempDir and write-opened there, which backfills the copy; the
// original is never written. Synthetic stores are projected as they publish.
func ProjectedRoot(b testing.TB) (root, label string) {
	b.Helper()
	src, label := Root(b)
	if label == "synthetic" {
		return src, label
	}
	raw, err := os.ReadFile(filepath.Join(src, "versions.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	root = b.TempDir()
	if err := os.WriteFile(filepath.Join(root, "versions.sqlite"), raw, 0o600); err != nil {
		b.Fatal(err)
	}
	if _, err := store.Open("", root, true); err != nil {
		b.Fatal(err)
	}
	return root, label
}

// Generate publishes the synthetic corpus into a new store at root.
func Generate(root string) error {
	ctx := context.Background()
	s, err := store.Open("", root, true)
	if err != nil {
		return err
	}
	for pr := 1; pr <= PullRequests; pr++ {
		key := store.Key{Repository: Repository, Number: pr}
		var previous store.Version
		for n := 1; n <= 1+pr%MaxVersions; n++ {
			next := store.Version{
				Metadata: store.Metadata{Version: n, Head: fmt.Sprintf("head-%d-%d", pr, n), BundleDigest: "digest", Backend: "claude", Model: "model", PromptVersion: "prompt", GeneratedAt: "2026-10-15T12:00:00Z"},
				Document: document(pr, n),
			}
			if err = s.Put(ctx, key, previous, next); err != nil {
				return err
			}
			previous = next
		}
	}
	return nil
}

// document is deterministic in (pr, version).
func document(pr, version int) json.RawMessage {
	hs := make([]map[string]any, hunks)
	for i := range hs {
		lines := make([]map[string]any, diffLines)
		for j := range lines {
			kind := [...]string{"context", "add", "delete"}[(i+j)%3]
			lines[j] = map[string]any{"kind": kind, "old": j + 1, "new": j + 1, "text": fmt.Sprintf("    value%d := compute(pr%d, hunk%d, line%d) // %s", j, pr, i, j, strings.Repeat("x", 40))}
		}
		hs[i] = map[string]any{
			"id": fmt.Sprintf("h_%d_%d_%d", pr, version, i), "path": fmt.Sprintf("pkg/module%d/file%d.go", i%12, i),
			"change": "modified", "role": "logic", "new_range": []int{10 + i, diffLines}, "additions": 5, "deletions": 4,
			"github_url": fmt.Sprintf("diff-%064x%s%d", pr*1000+i, "R", 10+i),
			"diff":       map[string]any{"header": fmt.Sprintf("@@ -%d,%d +%d,%d @@ func f%d()", i, diffLines, i, diffLines, i), "lines": lines},
		}
	}
	sections := make([]map[string]any, 40)
	for i := range sections {
		sections[i] = map[string]any{"id": fmt.Sprintf("sec_%d", i), "title": fmt.Sprintf("Section %d", i), "kind": "logical", "text": strings.Repeat("Explains the change in prose. ", 20)}
	}
	doc := map[string]any{
		"schema": "review/v1",
		"review": map[string]any{"repository": Repository, "pull_request": pr, "version": version, "pr_state": "open", "head": fmt.Sprintf("head-%d-%d", pr, version)},
		"summary": map[string]any{
			"title": fmt.Sprintf("Pull request %d", pr), "text": strings.Repeat("Summary prose. ", 40),
			"risk_score": map[string]any{"rubric": "risk-v1", "status": "assessed", "score": pr % 10, "reasons": []any{}}, "review_minutes": 5 + pr%30,
		},
		"index":    map[string]any{"hunks": hs, "checks": []any{}, "sessions": []any{}},
		"sections": sections,
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return raw
}
