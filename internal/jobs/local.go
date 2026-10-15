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
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/github"
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
	return repos.Clone{}, errors.New("registered clone unavailable")
}
func (l *Local) Prepare(ctx context.Context, item Item) (Prepared, string, error) {
	if item.PR <= 0 {
		return Prepared{}, "", errors.New("invalid pull request")
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
	cfg := c.Settings.Resolve(l.Config)
	backends := l.Backends
	if backends.Claude == nil {
		backends = generate.DefaultBackends()
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
	}{cfg.Budgets, cfg.Caps, selected.Version})
	digest := sha256.Sum256(fingerprint)
	list := pricing.Active()
	p := Prepared{ConfigDigest: hex.EncodeToString(digest[:]), PriceVersion: list.Version(), Item: item, Repository: c.Repository, HeadSHA: pr.Head.SHA, Backend: selected.Backend.Name(), Model: selected.Model, RequiresIsolationConfirmation: !trusted || !strings.EqualFold(pr.Head.Repo.FullName, repository.Owner+"/"+repository.Name), EstimateUSD: list.Estimate(selected.Model, cfg.Budgets.MaxTokens, cfg.Budgets.MaxCalls)}
	if p.EstimateUSD == nil {
		p.BlockedReason = "unknown_model_price"
	}
	return p, list.Version(), nil
}
func (l *Local) Execute(ctx context.Context, p Prepared, progress func(Progress)) (*float64, error) {
	current, _, e := l.Prepare(ctx, p.Item)
	if e != nil {
		return nil, e
	}
	if current.PriceVersion != p.PriceVersion || current.ConfigDigest != p.ConfigDigest || current.HeadSHA != p.HeadSHA || current.Backend != p.Backend || current.Model != p.Model || current.RequiresIsolationConfirmation != p.RequiresIsolationConfirmation {
		return nil, errors.New("generation inputs changed")
	}
	c, e := l.clone(ctx, p.Clone)
	if e != nil {
		return nil, e
	}
	cfg := c.Settings.Resolve(l.Config)
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
	repository, e := github.ParseRepository(c.Repository)
	if e != nil {
		return nil, e
	}
	backends := l.Backends
	if backends.Claude == nil {
		backends = generate.DefaultBackends()
	}
	selected, e := generate.Select(ctx, config.Backend(p.Backend), p.Model, backends, io.Discard)
	if e != nil {
		return nil, e
	}
	backend := &trackedBackend{Backend: selected.Backend, progress: progress}
	assembler := &assemble.Assembler{GitHub: l.Client, Git: clone, Sessions: assemble.LocalSessions{Dir: clone.Dir(), Warn: io.Discard}, Images: assemble.NewHTTPImageFetcher(nil), Warn: io.Discard}
	// An assembly which observes a changed head must stop before calling a model.
	guarded := headAssembler{Assembler: assembler, head: p.HeadSHA}
	list := pricing.Active()
	progress(Progress{Phase: "assembling"})
	result, e := generate.Publish(ctx, versions, store.Key{Repository: p.Repository, Number: p.PR}, generate.Options{Assembler: guarded, Request: assemble.Request{Repository: repository, Number: p.PR, Limits: assemble.LimitsFrom(caps)}, Backend: backend, BackendVersion: selected.Version, Model: p.Model, Budgets: agent.Budgets{WallClock: time.Duration(cfg.Budgets.RunTimeout), MaxCalls: cfg.Budgets.MaxCalls, MaxTokens: cfg.Budgets.MaxTokens, MaxProviderAttempts: 3, MaxRepairAttempts: 2}, OnValidation: func(pass int, valid bool) {
		backend.repairs = pass - 1
		if !valid {
			backend.repairs = pass
		}
		progress(Progress{Phase: "validating", Calls: backend.calls, Tokens: backend.tokens, Repairs: backend.repairs})
	}, PassTimeout: time.Duration(cfg.Budgets.PassTimeout)}, func(r generate.Result) *float64 { return list.OutcomeCost(r.Outcome.Model, r.Outcome) })
	progress(Progress{Phase: "publishing", Calls: result.Outcome.Calls, Tokens: result.Outcome.Tokens, Repairs: result.Outcome.ValidationRepairs})
	return list.OutcomeCost(result.Outcome.Model, result.Outcome), e
}

type trackedBackend struct {
	agent.Backend
	progress               func(Progress)
	calls, tokens, repairs int
}

func (b *trackedBackend) Run(ctx context.Context, r agent.Request) (agent.Result, error) {
	b.progress(Progress{Phase: "generating", Calls: b.calls, Tokens: b.tokens, Repairs: b.repairs})
	result, e := b.Backend.Run(ctx, r)
	b.calls += result.Calls
	b.tokens += result.Tokens
	b.progress(Progress{Phase: "validating", Calls: b.calls, Tokens: b.tokens, Repairs: b.repairs})
	return result, e
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
