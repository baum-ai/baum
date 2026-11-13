package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/prompt"
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
	"github.com/baum-ai/baum/internal/reviewrefs"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/review"
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
	// Features builds feature histories; nil refuses feature builds.
	Features FeatureBuilder
	// Log receives what a job's agent reports and keeps out of the job, such
	// as a fix agent's provider error; nil logs nothing.
	Log *slog.Logger
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
	if item.Kind == KindFeatureBuild {
		return l.prepareFeatureBuild(ctx, item)
	}
	if (item.Group == "") != (item.PR > 0) {
		return Prepared{}, "", errors.New("invalid pull request")
	}
	switch item.Kind {
	case "":
	case KindFix:
		return l.prepareFix(ctx, item)
	case KindDetails:
		return l.prepareDetails(ctx, item)
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
		group, e = l.prepareGroup(ctx, c, reader, repository, item.Group, item.AwaitMembers)
		pr = group.pull
	}
	if e != nil {
		return Prepared{}, "", e
	}
	cfg, e := l.resolve(ctx)
	if e != nil {
		return Prepared{}, "", e
	}
	backends := l.Backends
	if backends.Claude == nil {
		backends = generate.DefaultBackendsFor(cfg)
	}
	selected, e := generate.SelectDesktop(ctx, cfg.Backend, cfg.Model, backends, io.Discard)
	if e != nil {
		return Prepared{}, "", e
	}
	if e := cfg.Budgets.Validate(); e != nil {
		return Prepared{}, "", e
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
	p := Prepared{account: accountOf(reader), MaxTokens: cfg.Budgets.MaxTokens, MaxCalls: cfg.Budgets.MaxCalls, ConfigDigest: hex.EncodeToString(digest[:]), PriceVersion: list.Version(), Item: item, Repository: c.Repository, HeadSHA: pr.Head, BaseSHA: pr.Base, Backend: selected.Backend.Name(), Model: selected.Model, RequiresIsolationConfirmation: pr.Untrusted, Draft: pr.Draft, GroupName: group.name, Members: group.members, Awaiting: group.awaiting}
	return p, list.Version(), nil
}

// accountOf identifies the reader's credential partition for Prepared.account.
func accountOf(reader *github.RESTReader) string {
	sum := sha256.Sum256([]byte("account:" + reader.CacheIdentity()))
	return hex.EncodeToString(sum[:])
}

// The BlockedReasons of a retry of a version's details (KindDetails).
const (
	// ReasonHeadChanged: the pull request's head is no longer the one the
	// version reviewed; regenerate it instead.
	ReasonHeadChanged = "head_changed"
	// ReasonBaseChanged: the base branch moved since the version was
	// written, so its diff is not the one reviewed; regenerate it instead.
	ReasonBaseChanged = "base_changed"
	// ReasonVersionChanged: the version is no longer the latest.
	ReasonVersionChanged = "version_changed"
	// ReasonNothingToRetry: no section of the version lacks its details.
	ReasonNothingToRetry = "nothing_to_retry"
)

// prepareDetails prepares a retry of the failed section details of the
// latest version, item.Version: the pull request's review preparation, with
// the backend and model that wrote the version, blocked when the pull
// request's head moved since, the version is no longer the latest, or none
// of its sections failed.
func (l *Local) prepareDetails(ctx context.Context, item Item) (Prepared, string, error) {
	if item.Group != "" || item.PR <= 0 || item.Version <= 0 {
		return Prepared{}, "", errors.New("invalid details retry")
	}
	p, version, e := l.Prepare(ctx, Item{Clone: item.Clone, PR: item.PR})
	if e != nil {
		return Prepared{}, "", e
	}
	p.Item = item
	c, e := l.clone(ctx, item.Clone)
	if e != nil {
		return Prepared{}, "", e
	}
	versions, e := store.Open(c.CommonDir, "", false)
	if e != nil {
		return Prepared{}, "", e
	}
	latest, e := versions.Latest(ctx, store.Key{Repository: p.Repository, Number: item.PR})
	if e != nil && !errors.Is(e, store.ErrNotFound) {
		return Prepared{}, "", e
	}
	failed, _ := generate.Gaps(latest.Document)
	switch {
	case e != nil || latest.Metadata.Version != item.Version:
		p.BlockedReason = ReasonVersionChanged
	case latest.Metadata.Head != p.HeadSHA:
		p.BlockedReason = ReasonHeadChanged
	case p.BaseSHA != "" && latest.Metadata.Base != p.BaseSHA:
		p.BlockedReason = ReasonBaseChanged
	case len(failed) == 0:
		p.BlockedReason = ReasonNothingToRetry
	}
	if p.BlockedReason != "" {
		return p, version, nil
	}
	// The details are written by what wrote the rest of the version.
	cfg, e := l.resolve(ctx)
	if e != nil {
		return Prepared{}, "", e
	}
	backends := l.Backends
	if backends.Claude == nil {
		backends = generate.DefaultBackendsFor(cfg)
	}
	selected, e := generate.SelectDesktop(ctx, config.Backend(latest.Metadata.Backend), latest.Metadata.Model, backends, io.Discard)
	if e != nil {
		return Prepared{}, "", e
	}
	p.Backend, p.Model = selected.Backend.Name(), selected.Model
	return p, version, nil
}

// pull is what preparation reads of a pull request from GitHub.
type pull struct {
	// Head and Base are the head commit and the base branch's commit.
	Head, Base string
	Draft      bool
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
		Base struct{ SHA string }
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
	return pull{Head: pr.Head.SHA, Base: pr.Base.SHA, Draft: pr.Draft, Untrusted: !trusted || !strings.EqualFold(pr.Head.Repo.FullName, repository.Owner+"/"+repository.Name)}, nil
}

// confirmed rechecks a job's confirmed inputs. The price list is not one: a
// job runs, and is priced, with whichever list is active when it runs. An
// automatic job (RefuseDraft) whose pull request is a draft now is refused
// with ReasonDraft; a confirmed run of a draft is not.
//
// A changed agent, model or limit is not a changed input: the run uses the
// current settings, as one started now would. The GitHub account is one.
func (l *Local) confirmed(ctx context.Context, p Prepared) error {
	current, _, e := l.Prepare(ctx, p.Item)
	if e != nil {
		return e
	}
	// A retry whose pull request moved, or whose version is no longer the
	// latest, stands down.
	if p.Kind == KindDetails && current.BlockedReason != "" {
		return &BlockedError{current.BlockedReason}
	}
	sameAccount := current.ConfigDigest == p.ConfigDigest || (p.account != "" && current.account == p.account)
	if !sameAccount || current.HeadSHA != p.HeadSHA || current.Members != p.Members || current.RequiresIsolationConfirmation != p.RequiresIsolationConfirmation {
		if p.Kind == KindFix && current.HeadSHA != p.HeadSHA {
			return &fix.Error{Reason: fix.ReasonHeadChanged, Err: errors.New("the pull request's head changed since the fix was started")}
		}
		if sameAccount && (current.HeadSHA != p.HeadSHA || current.Members != p.Members) {
			return fmt.Errorf("generation inputs changed: %w", ErrHeadChanged)
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
		case current.Fix.ChatDigest != p.Fix.ChatDigest && p.Fix.ChatDigest != "":
			reason = fix.ReasonChatChanged
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

// resolve layers the app's preferences over the config file, reading them
// for every job so a change in the app applies without restarting the
// daemon.
func (l *Local) resolve(ctx context.Context) (config.Config, error) {
	prefs, e := l.Registry.Preferences(ctx)
	if e != nil {
		return config.Config{}, e
	}
	return prefs.Resolve(l.Config), nil
}

// resolveFix is resolve for a fix: the preferences' fix agent, when set,
// replaces the review's backend and model (repos.Preferences.ResolveFix).
func (l *Local) resolveFix(ctx context.Context) (config.Config, error) {
	prefs, e := l.Registry.Preferences(ctx)
	if e != nil {
		return config.Config{}, e
	}
	return prefs.ResolveFix(prefs.Resolve(l.Config)), nil
}

func (l *Local) Execute(ctx context.Context, p Prepared, progress func(Progress)) (*float64, error) {
	switch p.Kind {
	case KindFix:
		return l.executeFix(ctx, p, progress)
	case KindFeatureBuild:
		return l.executeFeatureBuild(ctx, p, progress)
	}
	c, e := l.clone(ctx, p.Clone)
	if e != nil {
		return nil, e
	}
	cfg, e := l.resolve(ctx)
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
	// A review runs with the current settings; a details retry with what
	// wrote the version (prepareDetails).
	wanted, model := cfg.Backend, cfg.Model
	if p.Kind == KindDetails {
		wanted, model = config.Backend(p.Backend), p.Model
	}
	selected, e := generate.SelectDesktop(ctx, wanted, model, backends, io.Discard)
	if e != nil {
		return nil, e
	}
	// The active list prices the run and is recorded with the saved review,
	// even when it is newer than the one the job was prepared with.
	list := pricing.Active()
	backend := &trackedBackend{Backend: selected.Backend, progress: progress, prices: list, model: selected.Model}
	// A review keeps its agent's transcript like a fix's, every call of it.
	if target, ok := transcriptTargetOf(ctx); ok {
		if writer, err := openTranscript(target.Dir, target.JobID); err == nil {
			defer writer.Close()
			backend.transcript = func(entry agent.TranscriptEntry) { writer.Add(ctx, entry) }
		}
	}
	assembler := &assemble.Assembler{GitHub: l.Client, Git: clone, Fetcher: clone, Sessions: assemble.LocalSessions{Dir: clone.Dir(), Warn: io.Discard, Repo: clone}, Images: assemble.NewHTTPImageFetcher(nil), Warn: io.Discard}
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
	name := selected.Backend.Name()
	progress(Progress{Phase: "assembling", Backend: name, Model: selected.Model})
	// A single pull request is written in stages, with a draft while they
	// run; a group's combined review in one pass, under a longer limit.
	limit := time.Duration(cfg.Budgets.ReviewTimeout)
	if group != nil {
		limit = groupReviewTimeout(limit, len(group.Members))
	}
	options := generate.Options{Group: group, Staged: group == nil, PriceList: list, Assembler: guarded, Request: assemble.Request{Repository: repository, Number: key.Number, Limits: assemble.LimitsFrom(caps)}, Backend: backend, BackendVersion: selected.Version, Model: selected.Model, Budgets: agent.Budgets{WallClock: limit, MaxCalls: cfg.Budgets.MaxCalls, MaxTokens: cfg.Budgets.MaxTokens, MaxProviderAttempts: 3, MaxRepairAttempts: 2},
		OnRepairs: backend.repaired, PassTimeout: max(time.Duration(cfg.Budgets.PassTimeout), limit)}
	if e := l.applyPrompts(ctx, p.Repository, &options); e != nil {
		return nil, e
	}
	if group == nil {
		options.OnDraft = backend.draft
		options.Stored = func(ctx context.Context, key store.Key, m store.Metadata) error {
			// The version is stored: its refs are written even when the job
			// is cancelled now.
			keep, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			e := reviewrefs.Keep(keep, clone, versions, key, m)
			if e != nil && l.Log != nil {
				l.Log.Warn("keeping the version's commits in the clone", "repository", key.Repository, "pr", key.Number, "version", m.Version, "error", e)
			}
			return e
		}
	}
	var result generate.Result
	if p.Kind == KindDetails {
		result, e = l.completeDetails(ctx, versions, manager, key, p, options)
	} else {
		result, e = generate.Publish(ctx, published, key, options, func(r generate.Result) *float64 {
			return list.BackendOutcomeCost(name, r.Outcome.Model, r.Outcome)
		})
	}
	backend.finish(result, e == nil && group == nil)
	return list.BackendOutcomeCost(name, result.Outcome.Model, result.Outcome), e
}

// completeDetails writes again the failed section details of the latest
// version, p.Version, and updates it in place (store.Store.CompleteCurrent).
// Each write rebases on the version as it is then: the sections it wrote are
// spliced into the latest bytes (review.DocumentValidator.SpliceSections), so
// a refresh in the meantime keeps its newer evidence, and a conflict reads
// and splices once more. A newer version, a changed generation, or a second
// conflict stands it down with ErrSuperseded.
func (l *Local) completeDetails(ctx context.Context, versions *store.Store, manager *githubauth.Manager, key store.Key, p Prepared, o generate.Options) (generate.Result, error) {
	latest, e := versions.Latest(ctx, key)
	if e != nil {
		return generate.Result{}, e
	}
	if latest.Metadata.Version != p.Version {
		return generate.Result{}, ErrSuperseded
	}
	// The version was reconciled with the one before it, and so is its
	// completion.
	if p.Version > 1 {
		earlier, e := versions.Get(ctx, key, p.Version-1)
		if e != nil {
			return generate.Result{}, e
		}
		if o.Request.Previous, e = review.PreviousVersionOf(earlier.Document, p.Version-1); e != nil {
			return generate.Result{}, e
		}
	}
	validator := o.Validator
	if validator == nil {
		if validator, e = review.NewDocumentValidator(); e != nil {
			return generate.Result{}, e
		}
		o.Validator = validator
	}
	o.Continue = &latest
	result, e := generate.Run(ctx, o)
	if e != nil {
		return result, e
	}
	// Only the sections that failed and now have their details change.
	failed, _ := generate.Gaps(latest.Document)
	var written []string
	for _, id := range failed {
		if !slices.Contains(result.FailedSections, id) {
			written = append(written, id)
		}
	}
	if len(written) == 0 {
		return result, nil
	}
	for range 2 {
		current, e := versions.Latest(ctx, key)
		if e != nil {
			return result, e
		}
		if current.Metadata.Version != p.Version {
			return result, ErrSuperseded
		}
		spliced, e := validator.SpliceSections(current.Document, result.Document, written)
		if e != nil {
			return result, ErrSuperseded
		}
		if findings := validator.ValidateStored(spliced, nil); len(findings) > 0 {
			return result, &generate.Error{Category: generate.CategoryInternal, Err: errors.New("the completed version is not a valid stored document")}
		}
		write := func() error { return versions.CompleteCurrent(ctx, key, current, spliced) }
		if manager != nil {
			e = manager.WithIdentity(ctx, write)
		} else {
			e = write()
		}
		if !errors.Is(e, store.ErrConflict) {
			result.Document = spliced
			return result, e
		}
	}
	return result, ErrSuperseded
}

// trackedBackend reports a job's calls, tokens, repairs, usage and drafts as
// they happen. The stages of a staged review call it from their own
// goroutines, so its counts are locked.
type trackedBackend struct {
	prices *pricing.List
	agent.Backend
	// model is the model the run was configured with, which prices the
	// usage while the provider's own model strings are still masked.
	model    string
	progress func(Progress)

	mu                     sync.Mutex
	redacted               bool
	calls, tokens, repairs int
	accounting             *agent.Accounting
	// live is what each run in progress has reported so far
	// (agent.Request.Usage), by the order the runs started in: a staged
	// review runs several at once. liveAt is when it was last reported.
	live   map[int][]agent.UsageAttempt
	runs   int
	liveAt time.Time
	phase  string
	// transcript, when set, keeps every call's steps, each call marked by a
	// "call" entry; marked counts them.
	transcript func(agent.TranscriptEntry)
	marked     int
}

// ConcurrentCalls forwards the backend's agent.ConcurrentCaller, so a staged
// review runs its details and context at once where the backend allows it.
func (b *trackedBackend) ConcurrentCalls() bool { return generate.ConcurrentCalls(b.Backend) }

func (b *trackedBackend) Run(ctx context.Context, r agent.Request) (agent.Result, error) {
	b.mu.Lock()
	b.runs++
	run := b.runs
	b.report("generating", nil)
	id := ""
	if b.transcript != nil && r.Transcript == nil {
		b.marked++
		id = fmt.Sprintf("call-%d", b.marked)
	}
	b.mu.Unlock()
	if id != "" {
		name, keep := callName(r), b.transcript
		r.Transcript = func(entry agent.TranscriptEntry) {
			entry.Call = name
			// Each call can reuse its backend's step IDs, so they are
			// namespaced by the call to keep later calls from replacing
			// earlier ones' steps.
			if entry.ID != "" && entry.Kind != "call" {
				entry.ID = id + "/" + entry.ID
			}
			keep(entry)
		}
		r.Emit(agent.TranscriptEntry{Kind: "call", ID: id, Title: name, Status: "running"})
	}
	watch := r.Usage
	r.Usage = func(attempts []agent.UsageAttempt) {
		if watch != nil {
			watch(attempts)
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.live == nil {
			b.live = map[int][]agent.UsageAttempt{}
		}
		b.live[run] = attempts
		// Each update is saved before it is published, so a stream's many
		// reports are sent at most every liveUsageInterval.
		if time.Since(b.liveAt) >= liveUsageInterval {
			b.liveAt = time.Now()
			b.report(b.phase, nil)
		}
	}
	result, e := b.Backend.Run(ctx, r)
	if id != "" {
		status := "completed"
		if e != nil {
			status = "failed"
		}
		r.Emit(agent.TranscriptEntry{Kind: "call", ID: id, Status: status})
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.live, run)
	b.calls += result.Calls
	b.tokens += result.Tokens
	next := &agent.Accounting{}
	if b.accounting != nil {
		*next = *b.accounting
		next.Attempts = append([]agent.UsageAttempt(nil), b.accounting.Attempts...)
	}
	next.Add(result, e != nil)
	b.accounting = next
	b.report("validating", nil)
	return result, e
}

// callName is a call's name in a review's transcript: its stage, or the
// review for a one-pass one, and which repair it is.
func callName(r agent.Request) string {
	name := "Review"
	switch r.Stage {
	case agent.StageOutline:
		name = "Outline"
	case agent.StageDetails:
		name = "Details"
	case agent.StageContext:
		name = "Codebase context"
	case "":
	default:
		name = r.Stage
	}
	if r.Pass > 1 {
		name += fmt.Sprintf(" · repair %d", r.Pass-1)
	}
	return name
}

// repaired is generate.Options.OnRepairs.
func (b *trackedBackend) repaired(repairs int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.repairs = repairs
	b.report("validating", nil)
}

// draft is generate.Options.OnDraft: the draft goes with the counts so far.
func (b *trackedBackend) draft(document []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.report(b.phase, document)
}

// finish reports the run's redacted outcome, and what a staged review was
// published without.
func (b *trackedBackend) finish(result generate.Result, staged bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.repairs = result.Outcome.ValidationRepairs
	b.accounting = result.Outcome.Accounting.Clone()
	b.redacted = true
	b.phase = "publishing"
	b.live = nil
	cost, reason := b.cost(b.accounting)
	p := Progress{Phase: b.phase, Calls: b.calls, Tokens: b.tokens, Repairs: b.repairs, Accounting: b.accounting.Clone(), CostUSD: cost, CostReason: reason, HasTranscript: b.transcript != nil}
	if staged {
		p.Gaps, p.FailedSections, p.ContextFailed = true, result.FailedSections, result.ContextFailed
	}
	b.progress(p)
}

// report sends the counts so far, and a draft when one is given. The
// caller holds mu.
func (b *trackedBackend) report(phase string, draft []byte) {
	if phase == "" {
		phase = "generating"
	}
	b.phase = phase
	current := b.current()
	safe := current.Clone()
	cost, reason := b.cost(current)
	if !b.redacted {
		safe.ClearStrings()
	}
	calls, tokens := b.calls, b.tokens
	for _, attempts := range b.live {
		for _, attempt := range attempts {
			calls, tokens = calls+attempt.Calls, tokens+attempt.ChargedTokens
		}
	}
	b.progress(Progress{Phase: phase, Calls: calls, Tokens: tokens, Repairs: b.repairs, Accounting: safe, CostUSD: cost, CostReason: reason, Draft: draft, HasTranscript: b.transcript != nil})
}

// liveUsageInterval is how often a run in progress reports its usage.
const liveUsageInterval = 2 * time.Second

// current is the usage so far: the finished runs' accounting and what the run
// in progress has reported. The caller holds mu.
func (b *trackedBackend) current() *agent.Accounting {
	if len(b.live) == 0 {
		return b.accounting
	}
	next := b.accounting.Clone()
	if next == nil {
		next = &agent.Accounting{}
	}
	for _, run := range slices.Sorted(maps.Keys(b.live)) {
		for _, attempt := range b.live[run] {
			next.Usage.Add(attempt.Usage)
			next.ChargedTokens += attempt.ChargedTokens
			next.Attempts = append(next.Attempts, attempt)
		}
	}
	return next
}

// cost is what the usage so far costs at API prices, estimated where it is
// incomplete (pricing.List.AccountingCost). Until the provider's strings are
// masked they are not shown, but each attempt is still priced: at the
// configured model's rates, or at the model it reported when none was
// configured, with a speed other than a known one treated as unreported. The
// caller holds mu.
func (b *trackedBackend) cost(accounting *agent.Accounting) (*float64, string) {
	list := b.prices
	if list == nil {
		list = pricing.Active()
	}
	if b.redacted || accounting == nil {
		return list.BackendAccountingCost(b.Name(), accounting)
	}
	estimate := accounting.Clone()
	for i := range estimate.Attempts {
		attempt := &estimate.Attempts[i]
		if b.model != "" {
			attempt.Model, attempt.ModelSource = b.model, "configured"
		}
		if s := attempt.Usage.Speed; s != nil && !knownSpeed(*s) {
			attempt.Usage.Speed = nil
		}
	}
	return list.BackendAccountingCost(b.Name(), estimate)
}

func knownSpeed(s string) bool {
	switch pricing.Speed(s) {
	case pricing.SpeedStandard, pricing.SpeedFast, pricing.SpeedUltrafast:
		return true
	}
	return false
}

type headAssembler struct {
	generate.Assembler
	head string
}

func (a headAssembler) Assemble(ctx context.Context, r assemble.Request) (assemble.Result, error) {
	result, e := a.Assembler.Assemble(ctx, r)
	if e == nil && result.Head != a.head {
		return assemble.Result{}, fmt.Errorf("%w; start it again", ErrHeadChanged)
	}
	return result, e
}

// prepareFeatureBuild plans a feature build of the clone's repository. One
// with no backend that can call now is blocked; nothing about its price is.
func (l *Local) prepareFeatureBuild(ctx context.Context, item Item) (Prepared, string, error) {
	if item.PR != 0 || item.Group != "" || l.Features == nil {
		return Prepared{}, "", errors.New("invalid feature build")
	}
	c, e := l.clone(ctx, item.Clone)
	if e != nil {
		return Prepared{}, "", e
	}
	plan, e := l.Features.PlanFeatureBuild(ctx, c.Repository, c.Path)
	if e != nil {
		return Prepared{}, "", e
	}
	list := pricing.Active()
	return Prepared{Item: item, Repository: c.Repository, Backend: plan.Backend, Model: plan.Model, PriceVersion: list.Version(),
		BlockedReason: plan.Reason}, list.Version(), nil
}

// executeFeatureBuild runs a started feature build.
func (l *Local) executeFeatureBuild(ctx context.Context, p Prepared, progress func(Progress)) (*float64, error) {
	if l.Features == nil {
		return nil, errors.New("invalid feature build")
	}
	c, e := l.clone(ctx, p.Clone)
	if e != nil {
		return nil, e
	}
	spent, e := l.Features.BuildFeatures(ctx, c.Repository, c.Path, progress)
	return &spent, e
}

// promptSourceOf is store.Metadata's PromptSource for a run that used ids:
// "repository" when a custom one came from the repository, "global" when
// custom ones came only from the global settings, "" when none was custom.
func promptSourceOf(ids []prompt.ID, overrides prompt.Overrides, sources map[prompt.ID]repos.PromptSource) string {
	source := ""
	for _, id := range ids {
		if overrides[id] == "" {
			continue
		}
		switch sources[id] {
		case repos.PromptSourceRepository:
			return string(repos.PromptSourceRepository)
		case repos.PromptSourceGlobal:
			source = string(repos.PromptSourceGlobal)
		}
	}
	return source
}

// applyPrompts sets the review's prompts to those resolved for the
// repository (repository, then global, then built in) and records where the
// custom ones came from. They are read once, at the start of the job: a change
// made while it runs applies to the next one. A failed read fails the job
// rather than reviewing with prompts the user did not choose.
func (l *Local) applyPrompts(ctx context.Context, repository string, o *generate.Options) error {
	overrides, sources, e := l.Registry.ResolvePrompts(ctx, repository)
	if e != nil {
		return fmt.Errorf("resolving the prompts: %w", e)
	}
	o.StagedPrompts = prompt.StagedPrompts{Overrides: overrides}
	if o.Group != nil {
		o.Prompts = prompt.GroupPrompts{Overrides: overrides}
	}
	o.PromptSource = promptSourceOf(prompt.ReviewIDs(o.Group != nil, o.Staged && o.Group == nil), overrides, sources)
	return nil
}

// groupReviewTimeout is a group review's time limit: the review limit plus
// half of it for each member after the first, at most three times the review
// limit. A group's review is written in one pass whose final document alone
// takes minutes, so the single pull request's limit is too short for it.
func groupReviewTimeout(review time.Duration, members int) time.Duration {
	extra := min(max(members-1, 0), 4)
	return review + time.Duration(extra)*review/2
}
