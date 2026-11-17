package generate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/claude"
	"github.com/baum-ai/baum/agent/orchestrate"
	"github.com/baum-ai/baum/agent/prompt"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/reviewv2"
	"github.com/baum-ai/baum/sessions"
	"github.com/baum-ai/baum/tiles/library"
)

// ReviewMCP gives a run baum's MCP server: Mint returns the server's URL and
// a read token scoped to one pull request, with the function that revokes
// it; ok is false when the server is not available now. The daemon
// implements it; a nil ReviewMCP runs without the server.
type ReviewMCP interface {
	Mint(repository string, pr int) (url, token string, revoke func(), ok bool)
}

// V2 writes a single pull request's review as a baum.review/2 document, in
// the levels of agent/orchestrate, instead of the staged review and its
// page view (Options.V2). The completed document is the version's document;
// it has no composition.
type V2 struct {
	// Prompts defaults to prompt.V2Prompts{} and FillPrompts, the library
	// fill texts of dedicated tiles, to prompt.TilesPrompts{}.
	Prompts     agent.ReviewV2Prompts
	FillPrompts agent.TileAnalysisPrompts
	// Previous is the latest stored version's document, of either schema;
	// Publish sets it. A baum.review/2 one's unchanged sections are reused.
	Previous []byte
	// NoReuse writes every section, as Options.NoReuse.
	NoReuse bool
	// MCP mints the run's MCP token, once per run; nil runs without it.
	MCP ReviewMCP
	// Clone is the person's clone the run's git worktree is added from, at
	// the pull request's head; empty runs the calls over the unpacked
	// trees instead.
	Clone string
	// Concurrency bounds the calls at once; zero is the orchestration's
	// default.
	Concurrency int
	// Step receives the level the run is at, in words, for the job's
	// progress.
	Step func(string)
	// Record receives every call's prompt, input and reply (replay).
	Record func(orchestrate.Call)
	// Complete replaces reviewv2.Complete, for tests.
	Complete orchestrate.CompleteFunc
	// PreviousVersion is Previous's version number; Publish sets it.
	PreviousVersion int
}

// v2Run writes the review of the assembled pull request with the
// orchestration. Only a failed map, or a run stopped or out of time before
// the map is in, fails it.
func v2Run(ctx, runCtx context.Context, o Options, assembled assemble.Result, captured []store.Attachment, runDir string, redactor *bundle.Redactor,
	fail func(Category, error) error, progress func(string, ...any), started time.Time, now func() time.Time) (result Result, err error) {
	v := o.V2
	b := assembled.Bundle
	prompts := v.Prompts
	if prompts == nil {
		prompts = prompt.V2Prompts{}
	}
	prompts = markedV2Prompts{prompts}
	fills := v.FillPrompts
	if fills == nil {
		fills = prompt.TilesPrompts{}
	}
	if o.Tiles != nil && o.Tiles.Prompts != nil {
		if p, ok := o.Tiles.Prompts.(agent.TileAnalysisPrompts); ok {
			fills = p
		}
	}
	instructions, err := prompts.ReviewV2Instructions(ctx, o.Backend.Name())
	if err != nil {
		return result, fail(CategoryBackend, err)
	}
	// The dedicated types: baum's library and the person's saved types.
	entries := library.All()
	if o.Tiles != nil {
		t := o.Tiles.withSaved(ctx, &b)
		if t.Library != nil {
			entries = t.Library
		}
		if t.NoLibrary {
			entries = nil
		}
	}
	version := v.PreviousVersion + 1
	if b.Previous != nil {
		version = b.Previous.Version + 1
	}
	var previous *reviewv2.Document
	if len(v.Previous) > 0 {
		var doc reviewv2.Document
		if json.Unmarshal(v.Previous, &doc) == nil && doc.Schema == reviewv2.Name {
			previous = &doc
		}
	}
	model := o.Model
	if model == "" {
		model = o.Backend.Name()
	}
	generator := func(model string) []byte {
		encoded, _ := json.Marshal(map[string]any{"model": model, "prompt_version": instructions.Version, "prompt_digest": instructions.Digest,
			"backend": o.Backend.Name(), "generated_at": now().UTC().Format(time.RFC3339)})
		return encoded
	}
	completeInput := reviewv2.CompleteInput{Bundle: assembled.JSON, Previous: v.Previous, Version: version, Generator: generator(model)}

	backend := o.Backend
	if o.PassTimeout > 0 {
		backend = passBounded{Backend: backend, timeout: o.PassTimeout}
	}
	g := &agent.Generator{Backend: backend, Redactor: redactor}
	run := orchestrate.Run{
		Generator: g, Bundle: &b, EncodedBundle: assembled.JSON, Directory: runDir, Budgets: o.Budgets,
		Prompts: prompts, FillPrompts: fills, Registry: orchestrate.NewRegistry(entries), Previous: previous, NoReuse: v.NoReuse || o.NoReuse,
		Concurrency: v.Concurrency, Complete: v.Complete, CompleteInput: completeInput, Record: v.Record,
		Step: func(text string) {
			progress("%s", text)
			if v.Step != nil {
				v.Step(text)
			}
		},
	}
	// A shell that reads, where the backend can sandbox it.
	shellOff := ""
	switch o.Backend.Name() {
	case agent.BackendClaude:
		if ok, why := claude.SandboxAvailable(); ok {
			run.Shell = true
		} else {
			shellOff = why
			progress("the review's agents read without a shell: %s", why)
		}
	case agent.BackendCodex:
		run.Shell = true
	}
	if v.Clone != "" {
		w, remove, err := addWorktree(ctx, v.Clone, filepath.Join(runDir, "repo"), assembled.Head, assembled.MergeBase)
		if err != nil {
			progress("the review runs over the unpacked trees: no git worktree (%v)", err)
		} else {
			defer remove()
			run.Worktree = w
		}
	}
	if v.MCP != nil {
		if url, token, revoke, ok := v.MCP.Mint(b.Repository, b.PullRequest.Number); ok {
			defer revoke()
			run.MCP = &agent.MCPServer{URL: url, Token: token}
		} else {
			progress("the review's agents read without baum's MCP server: it is not available")
		}
	}
	if o.OnDraft != nil {
		run.Draft = func(doc reviewv2.Document) {
			if encoded, err := json.Marshal(doc); err == nil {
				o.OnDraft(Draft{Document: encoded})
			}
		}
	}
	outcome, doc, genErr := orchestrate.Generate(runCtx, run)
	outcome.ShellOff = shellOff
	result.V2 = &outcome
	redactCtx := context.WithoutCancel(ctx)
	if _, _, err := agent.RedactOutput(redactCtx, nil, redactor, nil, nil, nil, &outcome.Outcome); err != nil {
		return result, fail(CategoryInternal, fmt.Errorf("masking the outcome: %w", err))
	}
	result.Outcome = outcome.Outcome
	if genErr != nil {
		if errors.Is(genErr, orchestrate.ErrMapFailed) && ctx.Err() == nil && runCtx.Err() == nil {
			return result, fail(CategoryValidation, genErr)
		}
		category, cause := classify(ctx, runCtx, genErr)
		return result, fail(category, cause)
	}
	if err := ctx.Err(); err != nil {
		return result, &Error{Category: CategoryInterrupted, Err: errors.New("interrupted")}
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		return result, err
	}
	if encoded, err = agent.RedactTaskOutput(redactCtx, redactor, encoded, &result.Outcome); err != nil {
		return result, fail(CategoryInternal, fmt.Errorf("masking the document: %w", err))
	}
	var header struct {
		Review struct {
			Version     int    `json:"version"`
			GeneratedAt string `json:"generated_at"`
		} `json:"review"`
	}
	_ = json.Unmarshal(encoded, &header)
	if header.Review.Version > 0 {
		version = header.Review.Version
	}
	generatedAt := header.Review.GeneratedAt
	if generatedAt == "" {
		generatedAt = now().UTC().Format(time.RFC3339Nano)
	}
	result.Metadata = store.Metadata{Accounting: result.Outcome.Accounting, Version: version, Head: assembled.Head, Base: assembled.Base, MergeBase: assembled.MergeBase,
		BundleDigest: b.Digest, Backend: o.Backend.Name(), BackendVersion: o.BackendVersion, Model: cmpOr(outcome.Model, model), Schema: reviewv2.Name,
		PromptVersion: instructions.Version, PromptDigest: instructions.Digest, PromptSource: o.PromptSource, GeneratedAt: generatedAt,
		Elapsed: time.Since(started), Calls: result.Outcome.Calls, Tokens: result.Outcome.Tokens}
	for _, s := range doc.Sections {
		if s.Details == "failed" {
			result.FailedSections = append(result.FailedSections, s.ID)
		}
	}
	result.ContextFailed = doc.Context == nil
	result.Attachments = captured
	result.Document = encoded
	if o.OnDraft != nil {
		o.OnDraft(Draft{Document: encoded})
	}
	progress("generated in %s: %s, %d repair(s)", time.Since(started).Round(time.Second), spent(result.Outcome), result.Outcome.ValidationRepairs)
	return result, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// addWorktree adds a git worktree of clone at dir, detached at head, for the
// run's calls, and returns it with the function that removes it. Worktrees
// whose directories are gone (a run that crashed, its run directory since
// cleaned) are pruned first.
func addWorktree(ctx context.Context, clone, dir, head, mergeBase string) (orchestrate.Worktree, func(), error) {
	git := func(ctx context.Context, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", clone}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}
	_, _ = git(ctx, "worktree", "prune")
	if _, err := git(ctx, "worktree", "add", "--detach", dir, head); err != nil {
		return orchestrate.Worktree{}, nil, err
	}
	remove := func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, _ = git(cleanup, "worktree", "remove", "--force", dir)
		_, _ = git(cleanup, "worktree", "prune")
	}
	common, err := git(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		common = ""
	}
	return orchestrate.Worktree{Dir: dir, GitDir: common, Head: head, MergeBase: mergeBase}, remove, nil
}

// markedV2Prompts is markedPrompts for a baum.review/2 run: the shared text,
// which starts every call's prompt, starts with the marker.
type markedV2Prompts struct{ agent.ReviewV2Prompts }

func (p markedV2Prompts) ReviewV2Instructions(ctx context.Context, backend string) (agent.ReviewV2Prompt, error) {
	out, err := p.ReviewV2Prompts.ReviewV2Instructions(ctx, backend)
	if err != nil {
		return out, err
	}
	out.Shared = sessions.GenerationRunMarker + "\n" + out.Shared
	out.Digest = prompt.V2Digest(out)
	return out, nil
}
