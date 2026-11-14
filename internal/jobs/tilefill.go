package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/githubauth"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/tilelayout"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// KindTileFill names a fill of a tile a person added to a pull request's
// review, for a version generation did not fill it for (an older
// generation, tile reviews off, a fill that was dropped), in logs and
// counts. It is not a queued job: it is one model call the person asked
// for by adding the tile, run beside the queue by TileFills, and it writes
// only the person's own layout data (tilelayout's fills).
const KindTileFill = "tile_fill"

// The bounds of a tile fill.
const (
	// tileFillTimeout bounds one fill, assembly included.
	tileFillTimeout = 5 * time.Minute
	// tileFillRetry is how long a failed fill of one version's type is not
	// tried again.
	tileFillRetry = 10 * time.Minute
)

// TileFiller runs one fill of an added tile and returns the data to store,
// with the definition it was written against; Local implements it.
type TileFiller interface {
	FillTile(ctx context.Context, r tilelayout.FillRequest) (definition, data json.RawMessage, cost *float64, err error)
}

// TileFills fills the tiles people added to reviews (tilelayout.Filler for
// the tile routes): a pull request version's type is filled once while the
// daemon runs, one fill at a time, in the background, and stores what
// it wrote as the version's fill, which tells the page to read its tiles
// again. A failed fill is not tried again for a while; the next version's
// generation fills the type in any case.
type TileFills struct {
	// Ctx bounds every fill: the daemon's lifetime.
	Ctx     context.Context
	Filler  TileFiller
	Layouts *tilelayout.Service
	Log     *slog.Logger
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	mu      sync.Mutex
	running map[string]bool
	failed  map[string]time.Time
	// done are the fills that succeeded: each version's type is filled once
	// for as long as the daemon runs, however often it is added and undone.
	done map[string]bool
	slot chan struct{}
	wg   sync.WaitGroup
}

func (t *TileFills) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *TileFills) log() *slog.Logger {
	if t.Log != nil {
		return t.Log
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Fill starts a fill unless one of the same version and type runs, already
// succeeded, or failed recently.
func (t *TileFills) Fill(r tilelayout.FillRequest) {
	if t == nil || t.Filler == nil || t.Layouts == nil {
		return
	}
	key := tilelayout.Key(r.Repository, r.Number) + "@" + strconv.Itoa(r.Version) + "/" + r.Type + "/" + r.Digest
	t.mu.Lock()
	if t.running == nil {
		t.running, t.failed, t.done, t.slot = map[string]bool{}, map[string]time.Time{}, map[string]bool{}, make(chan struct{}, 1)
	}
	if t.running[key] || t.done[key] || t.now().Sub(t.failed[key]) < tileFillRetry {
		t.mu.Unlock()
		return
	}
	t.running[key] = true
	t.mu.Unlock()
	t.wg.Go(func() {
		err := t.run(r)
		t.mu.Lock()
		defer t.mu.Unlock()
		delete(t.running, key)
		if err != nil {
			t.failed[key] = t.now()
		} else {
			delete(t.failed, key)
			t.done[key] = true
		}
	})
}

// Wait returns once every fill started has ended; tests use it.
func (t *TileFills) Wait() { t.wg.Wait() }

func (t *TileFills) run(r tilelayout.FillRequest) error {
	base := t.Ctx
	if base == nil {
		base = context.Background()
	}
	select {
	case t.slot <- struct{}{}:
	case <-base.Done():
		return base.Err()
	}
	defer func() { <-t.slot }()
	ctx, cancel := context.WithTimeout(base, tileFillTimeout)
	defer cancel()
	started := t.now()
	definition, data, cost, err := t.Filler.FillTile(ctx, r)
	attrs := []any{"kind", KindTileFill, "repository", r.Repository, "pr", r.Number, "version", r.Version, "type", r.Type, "elapsed", t.now().Sub(started).Round(time.Millisecond).String()}
	if cost != nil {
		attrs = append(attrs, "cost_usd", fmt.Sprintf("%.4f", *cost))
	}
	if err != nil {
		t.log().Warn("an added tile was not filled", append(attrs, "error", err.Error())...)
		return err
	}
	if err := t.Layouts.PutFill(context.WithoutCancel(ctx), tilelayout.Key(r.Repository, r.Number), tiles.LibraryTileID(r.Type), r.Version, tilelayout.OriginFill, definition, data); err != nil {
		t.log().Warn("an added tile's fill was not stored", append(attrs, "error", err.Error())...)
		return err
	}
	t.log().Info("filled an added tile", attrs...)
	return nil
}

// ErrFillSuperseded stands a tile fill down: the version it fills is no
// longer the pull request's latest, or the pull request moved since it was
// reviewed; the next review fills the type.
var ErrFillSuperseded = errors.New("the review changed since the tile was added; its next version fills it")

// FillTile implements TileFiller: it fills a library type (or a type made in
// chat, by its kept definition) for the latest
// saved version of a pull request with the review's agent and model, in
// one call (generate.FillTile) over the pull request assembled at the
// version's head, and checks the data against that version's hunks.
func (l *Local) FillTile(ctx context.Context, r tilelayout.FillRequest) (json.RawMessage, json.RawMessage, *float64, error) {
	if r.Digest != "" {
		// A type made in chat is never filled again: its fill instructions
		// were written by the session agent, which reads untrusted pull
		// request content, and the person has not saved them.
		return nil, nil, nil, fmt.Errorf("tile %q was made in chat and is not filled again", r.Type)
	}
	entry, ok := library.Get(r.Type)
	if !ok {
		return nil, nil, nil, fmt.Errorf("no library type %q", r.Type)
	}
	c, err := l.clone(ctx, r.Clone)
	if err != nil {
		return nil, nil, nil, err
	}
	cfg, err := l.resolve(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	repository, err := github.ParseRepository(c.Repository)
	if err != nil {
		return nil, nil, nil, err
	}
	var manager *githubauth.Manager
	if token, _ := cfg.ExplicitGitHubTokenSource(repository.Host); repository.Host == config.GitHubHost && token == "" {
		manager = config.GitHubAuth
		var cancel context.CancelFunc
		ctx, cancel = manager.IdentityContext(ctx)
		defer cancel()
	}
	clone, err := gitlocal.Open(ctx, c.Path, gitlocal.Options{})
	if err != nil {
		return nil, nil, nil, err
	}
	defer func() { _ = clone.Close() }()
	versions, err := store.Open(clone.CommonDir(), "", false)
	if err != nil {
		return nil, nil, nil, err
	}
	key := store.Key{Repository: c.Repository, Number: r.Number}
	latest, err := versions.Latest(ctx, key)
	if err != nil {
		return nil, nil, nil, err
	}
	if latest.Metadata.Version != r.Version || latest.Metadata.Head == "" {
		return nil, nil, nil, ErrFillSuperseded
	}
	reader, err := l.Client.REST(ctx, repository)
	if err != nil {
		return nil, nil, nil, err
	}
	pr, err := readPull(ctx, reader, repository, r.Number)
	if err != nil {
		return nil, nil, nil, err
	}
	if pr.Head != latest.Metadata.Head {
		return nil, nil, nil, ErrFillSuperseded
	}
	caps, err := cfg.EffectiveCaps()
	if err != nil {
		return nil, nil, nil, err
	}
	backends := l.Backends
	if backends.Claude == nil {
		backends = generate.DefaultBackendsFor(cfg)
	}
	selected, err := generate.SelectDesktop(ctx, cfg.Backend, cfg.Model, backends, io.Discard)
	if err != nil {
		return nil, nil, nil, err
	}
	assembler := &assemble.Assembler{GitHub: l.Client, Git: clone, Fetcher: clone, Sessions: assemble.LocalSessions{Dir: clone.Dir(), Warn: io.Discard, Repo: clone}, Warn: io.Discard}
	options := generate.Options{
		Assembler: headAssembler{Assembler: assembler, head: latest.Metadata.Head},
		Request:   assemble.Request{Repository: repository, Number: r.Number, Limits: assemble.LimitsFrom(caps)},
		Backend:   selected.Backend, BackendVersion: selected.Version, Model: selected.Model,
		Budgets:     agent.Budgets{WallClock: tileFillTimeout, MaxCalls: 2, MaxTokens: cfg.Budgets.MaxTokens, MaxProviderAttempts: 3, MaxRepairAttempts: 1},
		PassTimeout: max(time.Duration(cfg.Budgets.PassTimeout), tileFillTimeout),
	}
	result, err := generate.FillTile(ctx, options, entry)
	list := pricing.Active()
	cost := list.BackendOutcomeCost(selected.Backend.Name(), result.Outcome.Model, result.Outcome)
	if err != nil {
		return nil, nil, cost, err
	}
	if result.State != agent.FillReady {
		return nil, nil, cost, fmt.Errorf("the fill was dropped: %s", result.Reason)
	}
	// The data cites the version shown: the assembled diff is its diff.
	normalized, err := review.NormalizeStored(latest.Document)
	if err != nil {
		return nil, nil, cost, err
	}
	if doc, err := tiles.FromReview(normalized, tiles.Options{}); err == nil {
		if problems := library.CheckAddedFill(tiles.NewValidator(), entry, result.Data, library.DiffOf(doc)); len(problems) > 0 {
			return nil, nil, cost, fmt.Errorf("the fill does not fit the saved version: %s", problems[0])
		}
	}
	return entry.Encoded, result.Data, cost, nil
}

// alwaysTiles are the library types the person added to the pull request's
// review (generate.Tiles.Always), each with the last data held for it: the
// newest of baum's own fills (never data the agent wrote, which stays the
// person's) and of the latest version's composition, with the hunks of the
// version it was written for.
func (l *Local) alwaysTiles(ctx context.Context, versions *store.Store, key store.Key) []agent.AlwaysTile {
	if l.Layouts == nil {
		return nil
	}
	pr := tilelayout.Key(key.Repository, key.Number)
	state, err := l.Layouts.Read(ctx, pr)
	if err != nil {
		return nil
	}
	added := state.AddedTypes()
	if len(added) == 0 {
		return nil
	}
	fills, _ := l.Layouts.Fills(ctx, pr)
	latest, latestErr := versions.Latest(ctx, key)
	hunksOf := map[int][]string{}
	hunks := func(version int) []string {
		if ids, ok := hunksOf[version]; ok {
			return ids
		}
		var ids []string
		if v, err := versions.Get(ctx, key, version); err == nil {
			if normalized, err := review.NormalizeStored(v.Document); err == nil {
				if doc, err := tiles.FromReview(normalized, tiles.Options{}); err == nil {
					for _, h := range library.DiffOf(doc) {
						ids = append(ids, h.ID)
					}
				}
			}
		}
		hunksOf[version] = ids
		return ids
	}
	var out []agent.AlwaysTile
	for _, a := range added {
		if _, ok := library.Get(a.Name); !ok {
			continue
		}
		always := agent.AlwaysTile{Name: a.Name}
		version := 0
		for _, f := range fills {
			// Data the agent wrote stays the person's: only baum's own fill
			// runs may stand for a generated tile.
			if f.Origin == tilelayout.OriginFill && f.Tile == tiles.LibraryTileID(a.Name) && f.Version > version {
				version, always.Last = f.Version, f.Data
			}
		}
		if latestErr == nil && latest.Metadata.Version > version {
			if c, err := tiles.ParseComposition(latest.Composition); err == nil {
				for _, lib := range c.Library {
					var def struct {
						Name string `json:"name"`
					}
					if !lib.Pending && json.Unmarshal(lib.Definition, &def) == nil && def.Name == a.Name {
						version, always.Last = latest.Metadata.Version, lib.Data
					}
				}
			}
		}
		if version > 0 {
			always.LastHunks = hunks(version)
		}
		out = append(out, always)
	}
	return out
}
