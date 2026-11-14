package uiserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"

	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/tilelibrary"
	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/tiles/library"
)

// TileTypes is the session panel's way to the person's tile library
// (agentcontext.TileTypes): the store, and the render checker whose results
// a proposal rests on.
type TileTypes struct {
	Store  *tilelibrary.Store
	Checks tilelibrary.Checks
}

var _ agentcontext.TileTypes = TileTypes{}

// Read implements agentcontext.TileTypes: a type in full, source included,
// for the one tool that reads it.
func (t TileTypes) Read(ctx context.Context, name string) (json.RawMessage, error) {
	if t.Store == nil {
		return nil, errors.New("the tile library is not available in this baum")
	}
	view, err := t.Store.Lookup(ctx, name)
	if err != nil {
		return nil, err
	}
	return json.Marshal(view)
}

// Propose implements agentcontext.TileTypes.
func (t TileTypes) Propose(ctx context.Context, req agentcontext.TileTypeRequest) (string, json.RawMessage, error) {
	if t.Store == nil {
		return "", nil, errors.New("the tile library is not available in this baum")
	}
	card, err := t.Store.ProposeCard(ctx, t.Checks, tilelibrary.Request{FromName: req.FromName, Name: req.Name,
		Title: req.Title, UseWhen: req.UseWhen, Glance: req.Glance, Paths: req.Paths, ReviewChecks: req.ReviewChecks, Check: req.Check,
		PullDiff: req.PullDiff})
	if err != nil {
		return "", nil, err
	}
	after, err := json.Marshal(card)
	return agentcontext.TargetTileType + card.Name, after, err
}

// tileTypeTarget is a saved tile type: what a chat proposes to add to the
// person's library or change in it, which the person's Save writes. Its
// current value, shown to the person and never to the agent, is the type as
// a tilelibrary.Card, the shape of the proposal's After, so the panel diffs
// the two field by field. Its revision covers whether it is in the library
// and which version is current, and a proposal for an existing type names
// the version it started from (based_on), which must still be current when
// it is recorded and when it is saved: a type saved, edited or deleted
// meanwhile makes the save stale, and a save never reverts texts the person
// saved after the agent read them. The save is the library's own (Create,
// Update): the same checks as any other save.
func (p *Proposers) tileTypeTarget(name string) (*target, error) {
	store := p.TileTypes
	if store == nil {
		return nil, errors.New("the tile library is not available in this baum")
	}
	if err := tilelibrary.CheckName(name); err != nil {
		return nil, err
	}
	decode := func(after json.RawMessage) (tilelibrary.Change, *tilelibrary.Base, error) {
		var card tilelibrary.Card
		if err := decodeAfter(after, &card); err != nil {
			return tilelibrary.Change{}, nil, err
		}
		c, err := card.Change()
		if err != nil {
			return c, nil, err
		}
		prepared, err := tilelibrary.Prepare(c)
		if err != nil {
			return c, nil, err
		}
		if prepared.Name() != name {
			return c, nil, errors.New("the proposal is for another tile type than its target")
		}
		return c, card.BasedOn, nil
	}
	// startedFrom refuses a state that is not the version the proposal's
	// texts started from.
	startedFrom := func(based *tilelibrary.Base, state tilelibrary.State) error {
		if based == nil || (state.Present && !state.Deleted && state.Digest == based.Digest && state.Updated == based.Updated) {
			return nil
		}
		return agentcontext.ErrStale
	}
	return &target{
		read: func(ctx context.Context) (any, any, error) {
			state, err := store.State(ctx, name)
			if err != nil {
				return nil, nil, errors.New("the tile library could not be read")
			}
			if !state.Present || state.Deleted {
				return nil, state, nil
			}
			t, err := store.Get(ctx, name)
			if err != nil {
				return nil, nil, errors.New("the tile library could not be read")
			}
			return tilelibrary.CardOfType(t), state, nil
		},
		validate: func(ctx context.Context, after json.RawMessage) error {
			_, based, err := decode(after)
			if err != nil || based == nil {
				return err
			}
			state, err := store.State(ctx, name)
			if err != nil {
				return errors.New("the tile library could not be read")
			}
			return startedFrom(based, state)
		},
		apply: func(ctx context.Context, after json.RawMessage, unchanged func(any) error, _ agentcontext.SaveInput) error {
			c, based, err := decode(after)
			if err != nil {
				return err
			}
			check := func(state tilelibrary.State) error {
				if err := startedFrom(based, state); err != nil {
					return err
				}
				return unchanged(state)
			}
			state, err := store.State(ctx, name)
			if err != nil {
				return errors.New("the tile library could not be read")
			}
			if state.Present && !state.Deleted {
				_, err = store.Update(ctx, c, check)
			} else {
				_, err = store.Create(ctx, c, check)
			}
			return err
		},
	}, nil
}

// tileTypeName is a name in a preview route's path: a type's name.
var tileTypeName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// tileTypePreview serves GET /api/tile-types/{name}/preview?sample=: a
// built-in or saved type drawn without a review, as the Tile library page
// shows it: the type's served definition (its HTML replaced by its length,
// built in only by its content's digest), the sample's data and what the
// page would send a frame for its citations (library.Sample.Refs) for the
// glance and the detail, and the names of the samples. sample is a sample's
// name, the first when left out. It registers the definition's frame
// document, as the tile routes do, so the frame the app loads next is
// served: the registry is fed only by what the daemon hands the app, and
// the route has the namespace's cookie or the launch token like every
// other /api route. It never carries real pull request data: a type's
// samples are synthetic.
func (a LocalAPI) tileTypePreview(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !tileTypeName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	entry, ok := library.Get(name)
	if !ok {
		if a.TileTypes == nil {
			http.NotFound(w, r)
			return
		}
		var err error
		if entry, err = a.TileTypes.Entry(r.Context(), name); err != nil {
			if errors.Is(err, tilelibrary.ErrUnknown) {
				http.NotFound(w, r)
			} else {
				apiError(w, err)
			}
			return
		}
	}
	samples := make([]string, len(entry.Samples))
	for i, s := range entry.Samples {
		samples[i] = s.Name
	}
	pick := r.URL.Query().Get("sample")
	index := 0
	if pick != "" {
		index = -1
		for i, s := range entry.Samples {
			if s.Name == pick {
				index = i
			}
		}
	}
	if index < 0 || index >= len(entry.Samples) {
		http.NotFound(w, r)
		return
	}
	sample := entry.Samples[index]
	if _, err := tileview.FrameRegistry().Add(entry.Definition); err != nil {
		http.Error(w, "the tile's frame document could not be made", http.StatusUnprocessableEntity)
		return
	}
	glance, _ := sample.Refs("glance")
	detail, _ := sample.Refs("detail")
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{
		"name": name, "builtin": library.IsBuiltin(entry.Encoded),
		"title": entry.Definition.Title, "use_when": entry.Definition.UseWhen, "glance_line": entry.Glance,
		"sizes": entry.Definition.Sizes, "kit": entry.Definition.Kit, "digest": entry.Definition.Digest,
		"definition": tileview.ServedLibraryDefinition(entry.Encoded),
		"samples":    samples, "sample": sample.Name, "data": sample.Data,
		"refs": map[string]any{"glance": glance, "detail": detail},
	})
}
