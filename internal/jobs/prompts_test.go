package jobs

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/prompt"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/sessions"
)

// customText is the built-in text of id with a marker line added, which
// still passes validation.
func customText(t *testing.T, id prompt.ID, marker string) string {
	t.Helper()
	entry, ok := prompt.Lookup(id)
	if !ok {
		t.Fatal("unknown prompt", id)
	}
	return entry.Default + "\n" + marker + "\n"
}

func setOverride(t *testing.T, registry *repos.Registry, repository string, id prompt.ID, marker string) {
	t.Helper()
	if _, err := registry.SetPromptOverride(context.Background(), repository, id, customText(t, id, marker)); err != nil {
		t.Fatal(err)
	}
}

// runReview publishes a review of the fixture pull request with the prompts
// resolved for repository, and returns the backend's requests and the stored
// metadata.
func runReview(t *testing.T, registry *repos.Registry, repository string, staged bool) ([]agent.Request, store.Metadata) {
	t.Helper()
	ctx := context.Background()
	backend := &generatetest.Backend{Version: "9.9.9", RunFunc: generatetest.StagedReplies(nil)}
	o := reviewOptions(t, backend)
	o.Staged, o.Budgets.MaxCalls = staged, 16
	local := &Local{Registry: registry, ReviewRuns: filepath.Join(t.TempDir(), "review-runs")}
	t.Cleanup(func() { chmodTree(local.ReviewRuns) })
	if err := local.applyPrompts(ctx, repository, &o); err != nil {
		t.Fatal(err)
	}
	versions, err := store.Open("", t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: assembletest.Repository.String(), Number: assembletest.Number}
	result, err := local.publish(ctx, versions, versions, key, backend.Name(), o, nil)
	if err != nil {
		t.Fatal(err)
	}
	return backend.Requests, result.Metadata
}

func requestsMention(requests []agent.Request, marker string) bool {
	for _, r := range requests {
		if !strings.Contains(r.Prompt, marker) {
			return false
		}
	}
	return len(requests) > 0
}

func TestReviewUsesTheRepositorysPrompts(t *testing.T) {
	registry, clone := registeredClone(t)
	setOverride(t, registry, clone.Repository, prompt.IDReviewStagedShared, "STAGED-REPO-MARKER")

	requests, metadata := runReview(t, registry, clone.Repository, true)
	if !requestsMention(requests, "STAGED-REPO-MARKER") {
		t.Fatalf("staged prompts lack the override: %d requests", len(requests))
	}
	if !strings.HasSuffix(metadata.PromptVersion, prompt.CustomSuffix) || metadata.PromptSource != "repository" {
		t.Fatalf("metadata %q %q", metadata.PromptVersion, metadata.PromptSource)
	}
	if !strings.HasPrefix(requests[0].Prompt, sessions.GenerationRunMarker) {
		t.Fatal("the generation-run marker no longer starts the prompt")
	}
}

func TestReviewUsesTheGlobalPromptsInAnotherRepository(t *testing.T) {
	registry, _ := registeredClone(t)
	setOverride(t, registry, "", prompt.IDReviewStagedShared, "GLOBAL-MARKER")
	requests, metadata := runReview(t, registry, "someone/else", true)
	if !requestsMention(requests, "GLOBAL-MARKER") || metadata.PromptSource != "global" || !strings.HasSuffix(metadata.PromptVersion, prompt.CustomSuffix) {
		t.Fatalf("%q %q", metadata.PromptVersion, metadata.PromptSource)
	}
}

// An override of a prompt the review does not use leaves it built in and
// unmarked: a one-pass review takes none.
func TestReviewWithoutCustomPromptsIsUnmarked(t *testing.T) {
	registry, clone := registeredClone(t)
	_, before := runReview(t, registry, clone.Repository, false)
	setOverride(t, registry, clone.Repository, prompt.IDFix, "FIX-ONLY-MARKER")
	setOverride(t, registry, clone.Repository, prompt.IDReviewStagedShared, "STAGED-ONLY-MARKER")
	setOverride(t, registry, clone.Repository, prompt.IDReviewGroup, "GROUP-ONLY-MARKER")
	requests, after := runReview(t, registry, clone.Repository, false)
	if requestsMention(requests, "STAGED-ONLY-MARKER") || requestsMention(requests, "GROUP-ONLY-MARKER") || after.PromptSource != "" || after.PromptVersion != before.PromptVersion || strings.HasSuffix(after.PromptVersion, prompt.CustomSuffix) {
		t.Fatalf("%q %q (was %q)", after.PromptVersion, after.PromptSource, before.PromptVersion)
	}
}

func TestGroupReviewUsesTheRepositorysPrompts(t *testing.T) {
	registry, clone := registeredClone(t)
	setOverride(t, registry, clone.Repository, prompt.IDReviewGroup, "GROUP-MARKER")
	local := &Local{Registry: registry}
	o := generate.Options{Group: &generate.Group{}}
	if err := local.applyPrompts(context.Background(), clone.Repository, &o); err != nil {
		t.Fatal(err)
	}
	got, err := o.Prompts.Instructions(context.Background(), agent.BackendClaude)
	if err != nil || !strings.Contains(got.Body, "GROUP-MARKER") || !strings.HasSuffix(got.Version, prompt.CustomSuffix) || o.PromptSource != "repository" {
		t.Fatalf("version %q source %q err %v", got.Version, o.PromptSource, err)
	}
}

func TestPromptSourcePrefersTheRepository(t *testing.T) {
	ids := prompt.ReviewIDs(false, true)
	overrides := prompt.Overrides{ids[0]: "a", ids[1]: "b"}
	sources := map[prompt.ID]repos.PromptSource{ids[0]: repos.PromptSourceGlobal, ids[1]: repos.PromptSourceRepository}
	if got := promptSourceOf(ids, overrides, sources); got != "repository" {
		t.Fatal(got)
	}
	sources[ids[1]] = repos.PromptSourceGlobal
	if got := promptSourceOf(ids, overrides, sources); got != "global" {
		t.Fatal(got)
	}
	if got := promptSourceOf(prompt.ReviewIDs(false, false), overrides, sources); got != "" {
		t.Fatal(got)
	}
}

func TestReviewFailsWhenThePromptsCannotBeResolved(t *testing.T) {
	registry, clone := registeredClone(t)
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	local := &Local{Registry: registry}
	var o generate.Options
	if err := local.applyPrompts(context.Background(), clone.Repository, &o); err == nil || !strings.Contains(err.Error(), "resolving the prompts") {
		t.Fatal(err)
	}
}

func TestFixJobUsesTheRepositorysFixPrompt(t *testing.T) {
	r := newFixRepositories(t)
	ctx := context.Background()
	setOverride(t, r.registry, r.clone.Repository, prompt.IDFix, "FIX-REPO-MARKER")
	var sent string
	backend := &fixBackend{name: agent.BackendClaude, fix: resolveConflict, message: func(p string) string { sent = p; return "Resolved." }}
	local := fixRunner(t, r, &fakeGitHub{head: r.head, headRepository: "owner/repo", push: true}, backend)
	q, err := Open(filepath.Join(privateDir(t), "jobs.json"), local, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.Close)
	jobs, err := q.Generate(ctx, []Item{{Clone: r.clone.ID, PR: 1, Kind: KindFix}}, "single", false)
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	if job := waitFinished(t, q, jobs[0].ID); job.State != "completed" {
		t.Fatalf("job %+v", job)
	}
	if !strings.Contains(sent, "FIX-REPO-MARKER") || !strings.Contains(sent, "BEGIN UNTRUSTED DATA") {
		t.Fatalf("prompt: %s", sent)
	}
}
