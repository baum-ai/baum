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
	"github.com/baum-ai/baum/internal/store"
)

type Local struct {
	Registry *repos.Registry
	Config   config.Config
	Client   *github.Client
	Backends generate.Backends
	// RefreshPrices downloads and installs the signed price list; nil disables
	// the refresh for jobs confirmed against a newer list than the active one.
	RefreshPrices func(context.Context) error
	// FixDirectory holds fixes' temporary checkouts; empty is
	// fix.DefaultDirectory.
	FixDirectory string
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
	if item.PR <= 0 {
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
	path := "/repos/" + url.PathEscape(repository.Owner) + "/" + url.PathEscape(repository.Name)
	status, _, raw, e := reader.Read(ctx, fmt.Sprintf("%s/pulls/%d", path, item.PR), "")
	if e != nil || status != 200 {
		return Prepared{}, "", errors.New("pull request evidence unavailable")
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
		return Prepared{}, "", errors.New("open pull request evidence required")
	}
	trusted := false
	status, _, raw, e = reader.Read(ctx, path+"/collaborators/"+url.PathEscape(pr.User.Login)+"/permission", "")
	if e == nil && status == 200 {
		var permission struct{ Permission string }
		if json.Unmarshal(raw, &permission) == nil {
			trusted = permission.Permission == "write" || permission.Permission == "maintain" || permission.Permission == "admin"
		}
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
	p := Prepared{MaxTokens: cfg.Budgets.MaxTokens, MaxCalls: cfg.Budgets.MaxCalls, ConfigDigest: hex.EncodeToString(digest[:]), PriceVersion: list.Version(), Item: item, Repository: c.Repository, HeadSHA: pr.Head.SHA, Backend: selected.Backend.Name(), Model: selected.Model, RequiresIsolationConfirmation: !trusted || !strings.EqualFold(pr.Head.Repo.FullName, repository.Owner+"/"+repository.Name), Draft: pr.Draft}
	// The estimate is informational: a backend billed by its own subscription,
	// or a model the price list does not know, has none and still runs.
	p.EstimateUSD, p.EstimateReason = list.BackendEstimate(selected.Backend.Name(), selected.Model, cfg.Budgets.MaxTokens, cfg.Budgets.MaxCalls)
	return p, list.Version(), nil
}

// confirmed rechecks a job's confirmed inputs. A job confirmed against a newer
// list than the active one (a restart that has not yet reinstalled it) refreshes
// the signed list once before deciding. An automatic job (RefuseDraft) whose
// pull request is a draft now is refused with ReasonDraft; a confirmed run of
// a draft is not.
func (l *Local) confirmed(ctx context.Context, p Prepared) error {
	current, _, e := l.Prepare(ctx, p.Item)
	if e == nil && l.RefreshPrices != nil && pricing.CompareVersions(p.PriceVersion, current.PriceVersion) > 0 {
		if l.RefreshPrices(ctx) == nil {
			current, _, e = l.Prepare(ctx, p.Item)
		}
	}
	if e != nil {
		return e
	}
	if current.PriceVersion != p.PriceVersion || current.ConfigDigest != p.ConfigDigest || current.HeadSHA != p.HeadSHA || current.Backend != p.Backend || current.Model != p.Model || current.EstimateReason != p.EstimateReason || current.RequiresIsolationConfirmation != p.RequiresIsolationConfirmation {
		if p.Kind == KindFix && current.HeadSHA != p.HeadSHA {
			return &fix.Error{Reason: fix.ReasonHeadChanged, Err: errors.New("the pull request's head changed since the fix was confirmed")}
		}
		return errors.New("generation inputs changed")
	}
	// A fix GitHub now refuses (a lost push permission) or that has nothing
	// left to fix does not run.
	if p.Kind == KindFix && (current.BlockedReason != "" || current.Fix != p.Fix) {
		reason := current.BlockedReason
		if reason == "" {
			reason = fix.ReasonNothingToFix
		}
		return &fix.Error{Reason: reason, Err: errors.New("what the fix was confirmed for changed; confirm it again")}
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
	list := pricing.Active()
	if list.Version() != p.PriceVersion {
		return nil, errors.New("price list changed; estimate again")
	}
	backend := &trackedBackend{Backend: selected.Backend, progress: progress, prices: list}
	assembler := &assemble.Assembler{GitHub: l.Client, Git: clone, Sessions: assemble.LocalSessions{Dir: clone.Dir(), Warn: io.Discard}, Images: assemble.NewHTTPImageFetcher(nil), Warn: io.Discard}
	// An assembly which observes a changed head must stop before calling a model.
	guarded := headAssembler{Assembler: assembler, head: p.HeadSHA}
	progress(Progress{Phase: "assembling"})
	result, e := generate.Publish(ctx, identityVersions{versions, manager}, store.Key{Repository: p.Repository, Number: p.PR}, generate.Options{PriceList: list, Assembler: guarded, Request: assemble.Request{Repository: repository, Number: p.PR, Limits: assemble.LimitsFrom(caps)}, Backend: backend, BackendVersion: selected.Version, Model: p.Model, Budgets: agent.Budgets{WallClock: time.Duration(cfg.Budgets.RunTimeout), MaxCalls: cfg.Budgets.MaxCalls, MaxTokens: cfg.Budgets.MaxTokens, MaxProviderAttempts: 3, MaxRepairAttempts: 2}, OnValidation: func(pass int, valid bool) {
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
		return assemble.Result{}, errors.New("pull request head changed; estimate again")
	}
	return result, e
}
