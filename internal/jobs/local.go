package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/fix"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/githubauth"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewgroups"
	"github.com/baum-ai/baum/internal/reviewruns"
	"github.com/baum-ai/baum/internal/store"
)

type Local struct {
	Registry *repos.Registry
	// Groups holds review groups; nil refuses group jobs.
	Groups   *reviewgroups.Store
	Config   config.Config
	Client   *github.Client
	Backends generate.Backends
	// FixDirectory holds fixes' temporary checkouts; empty is
	// fix.DefaultDirectory.
	FixDirectory string
	// ReviewRuns holds the run directories of reviews that keep their agent
	// session; empty is reviewruns.DefaultRoot.
	ReviewRuns string
}

// keepsSession reports whether a review by the named backend keeps its agent
// session: only a local Claude Code or Codex session can be resumed.
func keepsSession(backend string) bool {
	return backend == string(config.BackendClaude) || backend == string(config.BackendCodex)
}

type identityVersions struct {
	generate.Versions
	manager *githubauth.Manager
}

func (v identityVersions) Put(ctx context.Context, key store.Key, previous, next store.Version) error {
	if v.manager == nil {
		return v.Versions.Put(ctx, key, previous, next)
	}
	return v.manager.WithIdentity(ctx, func() error { return v.Versions.Put(ctx, key, previous, next) })
}

func (l *Local) clone(ctx context.Context, id string) (repos.Clone, error) {
	clones, e := l.Registry.List(ctx)
	if e != nil {
		return repos.Clone{}, e
	}
	for _, c := range clones {
		if c.ID == id && c.Available {
			return c, nil
		}
	}
	return repos.Clone{}, ErrNeedsClone
}
func (l *Local) Prepare(ctx context.Context, item Item) (Prepared, string, error) {
	if (item.Group == "") != (item.PR > 0) {
		return Prepared{}, "", errors.New("invalid pull request")
	}
	switch item.Kind {
	case "":
	case KindFix:
		return l.prepareFix(ctx, item)
	default:
		return Prepared{}, "", errors.New("unknown job kind")
	}
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
	var pr pull
	var group groupInputs
	if item.Group == "" {
		pr, e = readPull(ctx, reader, repository, item.PR)
	} else {
		group, e = l.prepareGroup(ctx, c, reader, repository, item.Group)
		pr = group.pull
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
	if cfg.Budgets.MaxTokens <= 0 || cfg.Budgets.MaxCalls <= 0 || cfg.Budgets.RunTimeout <= 0 || cfg.Budgets.PassTimeout <= 0 {
		return Prepared{}, "", errors.New("positive generation budgets required")
	}
	fingerprint, _ := json.Marshal(struct {
		Budgets config.Budgets
		Caps    map[string]int
		Version string
		// Bind persisted confirmations to the originating credential partition.
		// This closes the admission-to-Execute window across account changes
		// and daemon restarts without exposing a separate credential fingerprint.
		Identity string
	}{cfg.Budgets, cfg.Caps, selected.Version, reader.CacheIdentity()})
	digest := sha256.Sum256(fingerprint)
	list := pricing.Active()
	p := Prepared{MaxTokens: cfg.Budgets.MaxTokens, MaxCalls: cfg.Budgets.MaxCalls, ConfigDigest: hex.EncodeToString(digest[:]), PriceVersion: list.Version(), Item: item, Repository: c.Repository, HeadSHA: pr.Head, Backend: selected.Backend.Name(), Model: selected.Model, RequiresIsolationConfirmation: pr.Untrusted, Draft: pr.Draft, GroupName: group.name, Members: group.members}
	return p, list.Version(), nil
}

// pull is what preparation reads of a pull request from GitHub.
type pull struct {
	Head  string
	Draft bool
	// Untrusted: the pull request comes from a fork or an author without
	// known write access, so running an agent on it needs confirmation.
	Untrusted bool
}

// readPull reads an open pull request's head, draft state and trust from
// GitHub's REST API.
func readPull(ctx context.Context, reader *github.RESTReader, repository github.Repository, number int) (pull, error) {
	path := "/repos/" + url.PathEscape(repository.Owner) + "/" + url.PathEscape(repository.Name)
	status, _, raw, e := reader.Read(ctx, fmt.Sprintf("%s/pulls/%d", path, number), "")
	if e != nil || status != 200 {
		return pull{}, errors.New("pull request evidence unavailable")
	}
	var pr struct {
		State string
		Draft bool
		Head  struct {
			SHA  string
			Repo struct {
				FullName string `json:"full_name"`
			}
		}
		User struct{ Login string }
	}
	if e = json.Unmarshal(raw, &pr); e != nil || pr.Head.SHA == "" || pr.State != "open" || pr.User.Login == "" {
		return pull{}, errors.New("open pull request evidence required")
	}
	trusted := false
	status, _, raw, e = reader.Read(ctx, path+"/collaborators/"+url.PathEscape(pr.User.Login)+"/permission", "")
	if e == nil && status == 200 {
		var permission struct{ Permission string }
		if json.Unmarshal(raw, &permission) == nil {
			trusted = permission.Permission == "write" || permission.Permission == "maintain" || permission.Permission == "admin"
		}
	}
	return pull{Head: pr.Head.SHA, Draft: pr.Draft, Untrusted: !trusted || !strings.EqualFold(pr.Head.Repo.FullName, repository.Owner+"/"+repository.Name)}, nil
}

// confirmed rechecks a job's confirmed inputs. The price list is not one: a
// job runs, and is priced, with whichever list is active when it runs. An
// automatic job (RefuseDraft) whose pull request is a draft now is refused
// with ReasonDraft; a confirmed run of a draft is not.
func (l *Local) confirmed(ctx context.Context, p Prepared) error {
	current, _, e := l.Prepare(ctx, p.Item)
	if e != nil {
		return e
	}
	if current.ConfigDigest != p.ConfigDigest || current.HeadSHA != p.HeadSHA || current.Members != p.Members || current.Backend != p.Backend || current.Model != p.Model || current.RequiresIsolationConfirmation != p.RequiresIsolationConfirmation {
		if p.Kind == KindFix && current.HeadSHA != p.HeadSHA {
			return &fix.Error{Reason: fix.ReasonHeadChanged, Err: errors.New("the pull request's head changed since the fix was started")}
		}
		return errors.New("generation inputs changed")
	}
	// A fix GitHub now refuses (a lost push permission) or that has nothing
	// left to fix does not run.
	if p.Kind == KindFix && (current.BlockedReason != "" || current.Fix != p.Fix) {
		reason := current.BlockedReason
		switch {
		case reason != "":
		case current.Fix.Threads != p.Fix.Threads && p.Fix.Threads != "":
			reason = fix.ReasonThreadsChanged
		default:
			reason = fix.ReasonNothingToFix
		}
		return &fix.Error{Reason: reason, Err: errors.New("what the fix was started for changed; start it again")}
	}
	if p.RefuseDraft && current.Draft {
		return &NotAutomaticError{ReasonDraft}
	}
	return nil
}

// resolve layers the config file, the app's preferences and the
// repository's settings, reading the preferences for every job so a change
// in the app applies without restarting the daemon.
func (l *Local) resolve(ctx context.Context, c repos.Clone) (config.Config, error) {
	prefs, e := l.Registry.Preferences(ctx)
	if e != nil {
		return config.Config{}, e
	}
	return c.Settings.Resolve(prefs.Resolve(l.Config)), nil
}

// resolveFix is resolve for a fix: the preferences' fix agent, when set,
// replaces the review's backend and model (repos.Preferences.ResolveFix).
func (l *Local) resolveFix(ctx context.Context, c repos.Clone) (config.Config, error) {
	prefs, e := l.Registry.Preferences(ctx)
	if e != nil {
		return config.Config{}, e
	}
	return prefs.ResolveFix(c.Settings.Resolve(prefs.Resolve(l.Config))), nil
}

func (l *Local) Execute(ctx context.Context, p Prepared, progress func(Progress)) (*float64, error) {
	if p.Kind == KindFix {
		return l.executeFix(ctx, p, progress)
	}
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
	caps, e := cfg.EffectiveCaps()
	if e != nil {
		return nil, e
	}
	clone, e := gitlocal.Open(ctx, c.Path, gitlocal.Options{})
	if e != nil {
		return nil, e
	}
	defer func() { _ = clone.Close() }()
	versions, e := store.Open(clone.CommonDir(), "", true)
	if e != nil {
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
	// The active list prices the run and is recorded with the saved review,
	// even when it is newer than the one the job was prepared with.
	list := pricing.Active()
	backend := &trackedBackend{Backend: selected.Backend, progress: progress, prices: list}
	assembler := &assemble.Assembler{GitHub: l.Client, Git: clone, Fetcher: clone, Sessions: assemble.LocalSessions{Dir: clone.Dir(), Warn: io.Discard}, Images: assemble.NewHTTPImageFetcher(nil), Warn: io.Discard}
	// An assembly which observes a changed head must stop before calling a model.
	var guarded generate.Assembler = headAssembler{Assembler: assembler, head: p.HeadSHA}
	var published generate.Versions = identityVersions{versions, manager}
	key := store.Key{Repository: p.Repository, Number: p.PR}
	var group *generate.Group
	if p.Group != "" {
		// A group's members carry their confirmed heads, which the group
		// assembly checks; its description images are left out.
		if group, e = l.groupRequest(ctx, p, versions); e != nil {
			return nil, e
		}
		assembler.Images = nil
		guarded = assembler
		published = identityVersions{reviewgroups.Publisher{Store: l.Groups, Group: p.Group}, manager}
		key.Number = group.Members[0].Number
	}
	progress(Progress{Phase: "assembling"})
	result, e := l.publish(ctx, published, versions, key, p.Backend, generate.Options{Group: group, PriceList: list, Assembler: guarded, Request: assemble.Request{Repository: repository, Number: key.Number, Limits: assemble.LimitsFrom(caps)}, Backend: backend, BackendVersion: selected.Version, Model: p.Model, Budgets: agent.Budgets{WallClock: time.Duration(cfg.Budgets.RunTimeout), MaxCalls: cfg.Budgets.MaxCalls, MaxTokens: cfg.Budgets.MaxTokens, MaxProviderAttempts: 3, MaxRepairAttempts: 2}, OnValidation: func(pass int, valid bool) {
		backend.repairs = pass - 1
		if !valid {
			backend.repairs = pass
		}
		backend.report("validating")
	}, PassTimeout: time.Duration(cfg.Budgets.PassTimeout)}, func(r generate.Result) *float64 {
		return list.BackendOutcomeCost(p.Backend, r.Outcome.Model, r.Outcome)
	})
	backend.repairs = result.Outcome.ValidationRepairs
	backend.accounting = result.Outcome.Accounting.Clone()
	backend.redacted = true
	backend.report("publishing")
	return list.BackendOutcomeCost(p.Backend, result.Outcome.Model, result.Outcome), e
}

// publish generates and stores a review. A single pull request reviewed by a
// local Claude Code or Codex keeps its agent session and run directory, and
// the directories of its older versions are pruned once the new one is stored.
func (l *Local) publish(ctx context.Context, published generate.Versions, versions *store.Store, key store.Key, backend string, o generate.Options, cost func(generate.Result) *float64) (generate.Result, error) {
	o.KeepSession = o.Group == nil && keepsSession(backend)
	o.SessionRoot = l.ReviewRuns
	result, e := generate.Publish(ctx, published, key, o, cost)
	if e == nil && result.Metadata.Session != nil {
		l.pruneReviewRuns(ctx, versions, key)
	}
	return result, e
}

type trackedBackend struct {
	prices   *pricing.List
	redacted bool
	agent.Backend
	progress               func(Progress)
	calls, tokens, repairs int
	accounting             *agent.Accounting
}

func (b *trackedBackend) Run(ctx context.Context, r agent.Request) (agent.Result, error) {
	b.report("generating")
	result, e := b.Backend.Run(ctx, r)
	b.calls += result.Calls
	b.tokens += result.Tokens
	next := &agent.Accounting{}
	if b.accounting != nil {
		*next = *b.accounting
		next.Attempts = append([]agent.UsageAttempt(nil), b.accounting.Attempts...)
	}
	next.Add(result, e != nil)
	b.accounting = next
	b.report("validating")
	return result, e
}

func (b *trackedBackend) report(phase string) {
	reason := "usage_not_reported"
	if b.accounting != nil {
		list := b.prices
		if list == nil {
			list = pricing.Active()
		}
		_, reason = list.BackendAccountingCost(b.Name(), b.accounting)
	}
	safe := b.accounting.Clone()
	if !b.redacted {
		safe.ClearStrings()
		reason = "metadata_pending_redaction"
	}
	b.progress(Progress{Phase: phase, Calls: b.calls, Tokens: b.tokens, Repairs: b.repairs, Accounting: safe, CostReason: reason})
}

type headAssembler struct {
	generate.Assembler
	head string
}

func (a headAssembler) Assemble(ctx context.Context, r assemble.Request) (assemble.Result, error) {
	result, e := a.Assembler.Assemble(ctx, r)
	if e == nil && result.Head != a.head {
		return assemble.Result{}, errors.New("pull request head changed; start it again")
	}
	return result, e
}

// pruneReviewRuns removes the kept run directories of the pull request's
// versions beyond the latest two. A failure only leaves them for the daily
// expiry.
func (l *Local) pruneReviewRuns(ctx context.Context, versions *store.Store, key store.Key) {
	root := l.ReviewRuns
	if root == "" {
		var e error
		if root, e = reviewruns.DefaultRoot(); e != nil {
			return
		}
	}
	if all, e := versions.Versions(ctx, key); e == nil {
		_ = reviewruns.PruneVersions(root, all)
	}
}
