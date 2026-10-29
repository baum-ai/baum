package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/fix"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/githubauth"
	"github.com/baum-ai/baum/internal/pricing"
)

// mergeableRetry is how long prepareFix waits before reading a pull request
// again whose mergeability GitHub has not computed yet.
var mergeableRetry = 1500 * time.Millisecond

// prepareFix prepares a fix of item from fresh GitHub evidence: the pull
// request's branches and head, whether its branch is in the repository
// itself (a fork is never pushed to), whether the signed-in account may push
// to the repository, and what is broken: merge conflicts (GitHub's
// mergeability), failing checks of the head and, on the account's own pull
// request, unresolved review threads (fixThreads). The harness and model are
// the review's, resolved the same way. What stops a fix is its BlockedReason.
func (l *Local) prepareFix(ctx context.Context, item Item) (Prepared, string, error) {
	c, e := l.clone(ctx, item.Clone)
	if e != nil {
		return Prepared{}, "", e
	}
	repository, e := github.ParseRepository(c.Repository)
	if e != nil {
		return Prepared{}, "", e
	}
	reader, e := l.Client.REST(ctx, repository)
	if e != nil {
		return Prepared{}, "", e
	}
	pr, e := fix.ReadPullRequest(ctx, reader, repository, item.PR)
	if e == nil && pr.Mergeable == nil {
		// GitHub computes mergeability on the first read after a change.
		select {
		case <-ctx.Done():
			return Prepared{}, "", ctx.Err()
		case <-time.After(mergeableRetry):
		}
		pr, e = fix.ReadPullRequest(ctx, reader, repository, item.PR)
	}
	if e != nil {
		return Prepared{}, "", e
	}
	cfg, e := l.resolve(ctx, c)
	if e != nil {
		return Prepared{}, "", e
	}
	backends := l.Backends
	if backends.Claude == nil {
		backends = generate.DefaultBackendsFor(cfg)
	}
	selected, e := generate.Select(ctx, cfg.Backend, cfg.Model, backends, io.Discard)
	if e != nil {
		return Prepared{}, "", e
	}
	if cfg.Budgets.MaxTokens <= 0 || cfg.Budgets.MaxCalls <= 0 || cfg.Budgets.RunTimeout <= 0 {
		return Prepared{}, "", errors.New("positive generation budgets required")
	}
	list := pricing.Active()
	p := Prepared{Item: item, Repository: c.Repository, HeadSHA: pr.HeadSHA, Backend: selected.Backend.Name(), Model: selected.Model,
		MaxTokens: cfg.Budgets.MaxTokens, MaxCalls: cfg.Budgets.MaxCalls, PriceVersion: list.Version(), Draft: pr.Draft,
		Fix: FixInputs{HeadRef: pr.HeadRef, BaseRef: pr.BaseRef}}
	switch {
	case pr.Fork():
		p.BlockedReason = fix.ReasonFork
	case !fix.Supported(p.Backend):
		p.BlockedReason = fix.ReasonUnsupportedBackend
	default:
		push, err := fix.CanPush(ctx, reader, repository)
		if err != nil {
			return Prepared{}, "", errors.New("repository permission evidence unavailable")
		}
		if !push {
			p.BlockedReason = fix.ReasonNoPushAccess
			break
		}
		checks, err := fix.FailingChecks(ctx, reader, repository, pr.HeadSHA)
		if err != nil {
			return Prepared{}, "", err
		}
		p.Fix.Conflicts, p.Fix.Checks = pr.Conflicts(), len(checks) > 0
		threads, err := l.fixThreads(ctx, repository, item.PR)
		if err != nil {
			return Prepared{}, "", err
		}
		p.Fix.Threads = fix.Digest(threads.Threads)
		p.FixThreadsCapped, p.FixThreadsUntrusted = threads.Capped, threads.Untrusted
		if len(threads.Threads) > 0 {
			// The excerpts are stored with the job: secrets in them are
			// masked as in the agent's summary.
			redactor, err := bundle.NewRedactor()
			if err != nil {
				return Prepared{}, "", err
			}
			for _, t := range threads.Threads {
				author := ""
				if len(t.Comments) > 0 {
					author = t.Comments[0].Author
				}
				excerpt, _, err := redactor.RedactString(ctx, fix.Excerpt(t))
				if err != nil {
					return Prepared{}, "", err
				}
				p.FixThreads = append(p.FixThreads, FixThread{Ref: t.Ref, Path: t.Path, Line: t.Line, Outdated: t.Outdated, Author: author, Excerpt: excerpt})
			}
		}
		if item.FixChat != nil {
			chat, turn, reason, err := fixChat(ctx, c, item.PR, *item.FixChat)
			if err != nil {
				return Prepared{}, "", err
			}
			if reason != "" {
				p.BlockedReason = reason
				break
			}
			// The estimate pins the last turn: the confirmed fix uses the
			// conversation up to it, whatever is asked after.
			p.Item.FixChat = &FixChatRef{Conversation: chat.Conversation, Turn: turn}
			p.Fix.Chat, p.Fix.ChatTurn, p.Fix.ChatDigest = chat.Conversation, turn, fix.ChatDigest(chat)
		}
		if !p.Fix.Conflicts && !p.Fix.Checks && p.Fix.Threads == "" && p.Fix.ChatDigest == "" {
			p.BlockedReason = fix.ReasonNothingToFix
		}
	}
	fingerprint, _ := json.Marshal(struct {
		Kind     string
		Budgets  config.Budgets
		Version  string
		Identity string
	}{KindFix, cfg.Budgets, selected.Version, reader.CacheIdentity()})
	digest := sha256.Sum256(fingerprint)
	p.ConfigDigest = hex.EncodeToString(digest[:])
	// Like a review's, the estimate assumes the whole budget is used.
	p.EstimateUSD, p.EstimateReason = list.BackendEstimate(p.Backend, p.Model, cfg.Budgets.MaxTokens, cfg.Budgets.MaxCalls)
	return p, list.Version(), nil
}

// fixThreads reads the unresolved review threads a fix of the pull request
// addresses (fix.SelectThreads), but only on a pull request the signed-in
// account opened: replying "Addressed in" on reviewers' threads and resolving
// them is its author's to do. On another pull request there are none.
func (l *Local) fixThreads(ctx context.Context, repository github.Repository, number int) (fix.Selection, error) {
	list, err := l.Client.UnresolvedThreads(ctx, repository, number)
	if err != nil {
		return fix.Selection{}, err
	}
	if !list.ViewerDidAuthor {
		return fix.Selection{}, nil
	}
	return fix.SelectThreads(list), nil
}

// executeFix rechecks a confirmed fix against GitHub, reads the failing
// checks' output and logs and the review threads, and runs the fix
// (fix.Run). Progress carries the phase, the agent's usage once it has run,
// the pushed commit and what became of each review thread.
func (l *Local) executeFix(ctx context.Context, p Prepared, progress func(Progress)) (*float64, error) {
	c, e := l.clone(ctx, p.Clone)
	if e != nil {
		return nil, e
	}
	cfg, e := l.resolve(ctx, c)
	if e != nil {
		return nil, e
	}
	repository, e := github.ParseRepository(c.Repository)
	if e != nil {
		return nil, e
	}
	var manager *githubauth.Manager
	if token, _ := cfg.ExplicitGitHubTokenSource(repository.Host); repository.Host == config.GitHubHost && token == "" {
		manager = config.GitHubAuth
		var cancel context.CancelFunc
		ctx, cancel = manager.IdentityContext(ctx)
		defer cancel()
	}
	if e := l.confirmed(ctx, p); e != nil {
		return nil, e
	}
	backends := l.Backends
	if backends.Claude == nil {
		backends = generate.DefaultBackendsFor(cfg)
	}
	selected, e := generate.Select(ctx, config.Backend(p.Backend), p.Model, backends, io.Discard)
	if e != nil {
		return nil, e
	}
	fixer, ok := selected.Backend.(agent.Fixer)
	if !ok || !fix.Supported(selected.Backend.Name()) {
		return nil, &fix.Error{Reason: fix.ReasonUnsupportedBackend, Err: errors.New(selected.Backend.Name() + " cannot fix pull requests; choose claude or codex")}
	}
	reader, e := l.Client.REST(ctx, repository)
	if e != nil {
		return nil, e
	}
	pr, e := fix.ReadPullRequest(ctx, reader, repository, p.PR)
	if e != nil {
		return nil, e
	}
	var evidence []fix.Check
	if p.Fix.Checks {
		progress(Progress{Phase: "reading checks"})
		checks, err := fix.FailingChecks(ctx, reader, repository, p.HeadSHA)
		if err != nil {
			return nil, err
		}
		evidence = fix.Logs(ctx, reader, repository, checks)
	}
	var threads []fix.Thread
	if p.Fix.Threads != "" {
		progress(Progress{Phase: "reading review comments"})
		selection, err := l.fixThreads(ctx, repository, p.PR)
		if err != nil {
			return nil, err
		}
		// The agent gets exactly the threads that were confirmed.
		if threads = selection.Threads; fix.Digest(threads) != p.Fix.Threads {
			return nil, &fix.Error{Reason: fix.ReasonThreadsChanged, Err: errors.New("the review comments changed since the fix was confirmed; confirm it again")}
		}
	}
	var conversation *fix.ChatContext
	if p.Fix.ChatDigest != "" && p.FixChat != nil {
		progress(Progress{Phase: "reading the conversation"})
		chat, _, reason, err := fixChat(ctx, c, p.PR, *p.FixChat)
		if err != nil {
			return nil, err
		}
		// The agent gets exactly the conversation that was confirmed.
		if reason != "" || fix.ChatDigest(chat) != p.Fix.ChatDigest {
			return nil, &fix.Error{Reason: fix.ReasonChatChanged, Err: errors.New("the conversation changed since the fix was confirmed; confirm it again")}
		}
		conversation = &chat
	}
	var transcript func(agent.TranscriptEntry)
	hasTranscript := false
	if target, ok := transcriptTargetOf(ctx); ok {
		if writer, err := openTranscript(target.Dir, target.JobID); err == nil {
			defer writer.Close()
			transcript, hasTranscript = func(entry agent.TranscriptEntry) { writer.Add(ctx, entry) }, true
		}
	}
	list := pricing.Active()
	out, err := fix.Run(ctx, fix.Options{ID: id(), Clone: c.Path, Repository: repository, Number: p.PR, Title: pr.Title,
		HeadRef: p.Fix.HeadRef, HeadSHA: p.HeadSHA, BaseRef: p.Fix.BaseRef, Conflicts: p.Fix.Conflicts, Checks: p.Fix.Checks, Evidence: evidence,
		Threads: threads, Chat: conversation, Replies: l.Client, Agent: fixer, Backend: p.Backend, Model: p.Model, MaxCalls: cfg.Budgets.MaxCalls, MaxTokens: cfg.Budgets.MaxTokens,
		WallClock: time.Duration(cfg.Budgets.RunTimeout), Directory: l.FixDirectory, Env: cfg.AgentEnviron(), Transcript: transcript,
		Progress: func(phase string) { progress(Progress{Phase: phase, HasTranscript: hasTranscript}) }})
	report := Progress{Phase: "finishing", HasTranscript: hasTranscript}
	var cost *float64
	if out.Agent != nil {
		accounting := &agent.Accounting{}
		accounting.Add(*out.Agent, err != nil)
		report.Calls, report.Tokens = out.Agent.Calls, out.Agent.Tokens
		cost, report.CostReason = list.BackendAccountingCost(p.Backend, accounting)
		report.Accounting = accounting
	}
	// The summary and the provider's model names are the model's words:
	// secrets are masked before they are stored or shown.
	if redactor, rerr := bundle.NewRedactor(); rerr == nil {
		report.Summary, _, rerr = redactor.RedactString(ctx, out.Summary)
		if report.Accounting != nil {
			for i := range report.Accounting.Attempts {
				if rerr == nil {
					report.Accounting.Attempts[i].Model, _, rerr = redactor.RedactString(ctx, report.Accounting.Attempts[i].Model)
				}
			}
		}
		if rerr != nil {
			report.Summary = ""
			report.Accounting.ClearStrings()
		}
	} else {
		report.Accounting.ClearStrings()
	}
	report.Threads = out.Threads
	if out.Commit != "" {
		report.Commit = out.Commit
		report.CommitURL = "https://" + repository.Host + "/" + url.PathEscape(repository.Owner) + "/" + url.PathEscape(repository.Name) + "/commit/" + out.Commit
	}
	progress(report)
	return cost, err
}
