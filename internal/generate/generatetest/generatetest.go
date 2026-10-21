// Package generatetest is a stub model backend for tests of "baum internal generate":
// it returns canned documents without calling a model, and records the run
// directories it was given.
package generatetest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/bundle"
)

// Backend is an agent.Backend and a generate.CheckedBackend whose runs a test
// scripts. With no RunFunc it returns Document(request.Directory) as the model
// "stub-model", with usage.
type Backend struct {
	// BackendName defaults to claude.
	BackendName string
	CheckErr    error
	Version     string
	RunFunc     func(ctx context.Context, request agent.Request, n int) (agent.Result, error)

	mu sync.Mutex
	// Requests are the requests received, in order. Directories are the run
	// directories of them, and Existed whether each held a bundle.json and a
	// checkout when the backend ran.
	Requests []agent.Request
	Existed  []bool
}

// Name implements agent.Backend.
func (b *Backend) Name() string {
	if b.BackendName == "" {
		return agent.BackendClaude
	}
	return b.BackendName
}

// Check implements generate.CheckedBackend.
func (b *Backend) Check(context.Context) (string, error) { return b.Version, b.CheckErr }

// Run implements agent.Backend.
func (b *Backend) Run(ctx context.Context, request agent.Request) (agent.Result, error) {
	b.mu.Lock()
	n := len(b.Requests)
	b.Requests = append(b.Requests, request)
	_, bundleErr := os.Stat(filepath.Join(request.Directory, "bundle.json"))
	_, headErr := os.Stat(filepath.Join(request.Directory, "head"))
	b.Existed = append(b.Existed, bundleErr == nil && headErr == nil)
	b.mu.Unlock()
	if b.RunFunc != nil {
		return b.RunFunc(ctx, request, n)
	}
	document, err := Document(request.Directory, nil)
	if err != nil {
		return agent.Result{}, err
	}
	return agent.Result{Document: document, Model: "stub-model", Calls: 1, Tokens: 100, Usage: agent.Usage{Input: 60, Output: 40}}, nil
}

// Document builds a valid model document for the bundle the loop wrote into
// dir: one section holding every hunk, anchored at the first. edit, when not
// nil, changes the decoded document before it is encoded.
func Document(dir string, edit func(map[string]any)) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "bundle.json"))
	if err != nil {
		return nil, err
	}
	return DocumentFor(raw, edit)
}

// DocumentFor is Document for the encoded bundle, for a stub that sees only
// the prompt.
func DocumentFor(raw []byte, edit func(map[string]any)) ([]byte, error) {
	var b bundle.Bundle
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, err
	}
	if len(b.Diff) == 0 {
		return nil, fmt.Errorf("the bundle has no hunks")
	}
	ids := make([]string, len(b.Diff))
	for i, hunk := range b.Diff {
		ids[i] = hunk.ID
	}
	document := map[string]any{
		"schema": "baum.review/1",
		"summary": map[string]any{
			"text": "Adds notes, settings and a renamed guide.", "review_exchange": map[string]any{"summary": "No reviews available."},
			"context": map[string]any{"area": map[string]any{
				"text": "The demo app keeps notes and settings beside its guide.",
				"refs": []any{map[string]any{"path": b.Diff[0].Path, "symbol": nil}},
			}},
			"change_kind": "feature", "implements": []any{}, "release_note": nil, "breaking_change": nil, "attention": nil,
			"authorship": map[string]any{"declared": "unknown", "declared_tools": []any{}, "anchors": []any{}},
		},
		"relations": []any{},
		"sections": []any{map[string]any{
			"id": "sec_changes", "title": "Changes", "kind": "logical", "hunks": ids,
			"summary":   map[string]any{"text": "The whole change.", "anchors": []any{map[string]any{"type": "hunk", "id": ids[0]}}},
			"attention": map[string]any{"text": "Check the settings handling.", "refs": []any{"rsk_settings", "ev_unit"}},
			"assessment": map[string]any{
				"level": 3, "factors": []any{"untrusted_input"},
				"reasons": []any{map[string]any{"text": "Settings come from the environment.", "anchors": []any{map[string]any{"type": "hunk", "id": ids[0]}}}},
			},
			"risks": []any{map[string]any{
				"id": "rsk_settings", "tier": "medium", "category": "data_integrity", "source": "model",
				"text": "Settings are read from the environment without validation.", "finding": nil,
				"anchors": []any{map[string]any{"type": "hunk", "id": ids[0]}},
			}},
			"evidence": []any{map[string]any{
				"id": "ev_unit", "kind": "test", "claim": "The change is covered by the demo tests.", "status": "asserted", "source": "model",
				"anchors": []any{map[string]any{"type": "hunk", "id": ids[0]}}, "detail": nil,
			}},
			"blocks": []any{},
		}},
	}
	if edit != nil {
		edit(document)
	}
	return json.Marshal(document)
}
