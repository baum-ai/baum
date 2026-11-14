package uiserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/tilelayout"
	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// A tile the person added to a pull request's review (an add op of their
// overlay) shows, on each version, what generation filled for it or else
// the fill the host holds (tilelayout.AddedFor). On the latest version a
// tile with no fill of its own shows its last data, or pending, while a fill
// runs (LocalAPI.Fills), started when the tile is added without data and
// when a version that needs one is served.

// TileFiller starts filling added tiles; jobs.TileFills implements it.
type TileFiller interface {
	// Fill starts a fill of an added tile for a version, in the
	// background, unless one runs or recently failed.
	Fill(request tilelayout.FillRequest)
}

// fillValidator checks data the agent wrote for an added tile.
var fillValidator = sync.OnceValue(tiles.NewValidator)

// layoutState is a pull request's overlay and what names it with its fills
// in a cache key.
func (a LocalAPI) layoutState(ctx context.Context, repository string, number int) (tilelayout.State, []tilelayout.Fill, string) {
	if a.Layouts == nil {
		return tilelayout.State{}, nil, ""
	}
	pr := tilelayout.Key(repository, number)
	state, err := a.Layouts.Read(ctx, pr)
	if err != nil {
		a.logger().Warn("a tile layout could not be read", "error", err.Error())
		return tilelayout.State{}, nil, ""
	}
	key := tileview.LayoutKey(tileview.Layout{Overlay: state.Overlay})
	if key == "" || len(state.AddedTypes()) == 0 {
		return state, nil, key
	}
	fills, err := a.Layouts.Fills(ctx, pr)
	if err != nil {
		a.logger().Warn("the fills of added tiles could not be read", "error", err.Error())
	}
	sum := sha256.New()
	for _, f := range fills {
		_, _ = fmt.Fprintf(sum, "%s\x00%d\x00%s\x00", f.Tile, f.Version, f.Digest)
		sum.Write(f.Data)
	}
	return state, fills, key + "\x00fills\x00" + hex.EncodeToString(sum.Sum(nil))
}

// layoutFor is the layout a version is served with: the overlay and what
// the host holds for its added tiles. On the latest version, the added
// tiles it has no fill for are filled (LocalAPI.Fills).
func (a LocalAPI) layoutFor(ctx context.Context, c repos.Clone, number int, version store.Version, latest bool, state tilelayout.State, fills []tilelayout.Fill) tileview.Layout {
	layout := tileview.Layout{Overlay: state.Overlay}
	added := state.AddedTypes()
	if len(added) == 0 || !state.Edited() {
		return layout
	}
	v := tilelayout.Version{Number: version.Metadata.Version, Latest: latest, Generated: tilelayout.Generated(version.Composition), Diff: diffOfStored(version.Document)}
	held, missing := tilelayout.AddedFor(added, fills, v, func(digest string) (json.RawMessage, bool) { return a.Layouts.Definition(ctx, digest) })
	layout.Added, layout.Notes = held.Added, held.Notes
	if a.Fills != nil {
		for _, name := range missing {
			a.Fills.Fill(tilelayout.FillRequest{Clone: c.ID, Repository: c.Repository, Number: number, Version: version.Metadata.Version, Type: name})
		}
	}
	return layout
}

// diffOfStored is the hunks a stored review document's index lists.
func diffOfStored(document []byte) []bundle.Hunk {
	var stored struct {
		Index json.RawMessage `json:"index"`
	}
	if json.Unmarshal(document, &stored) != nil {
		return []bundle.Hunk{}
	}
	diff := library.DiffOf(&tiles.Document{Index: stored.Index})
	if diff == nil {
		diff = []bundle.Hunk{}
	}
	return diff
}

// AddTile is an add_tile request: the library type, its size and place, and
// the data the agent wrote for it, if any.
type AddTile struct {
	Type string
	Size tiles.Size
	To   *tiles.Place
	Data json.RawMessage
}

// AddedTile is what an add did: the tile's ID, whether it shows its data
// ("ready") or is being filled ("filling"), and the layout's change.
type AddedTile struct {
	Tile   string
	State  string
	Change tilelayout.Change
}

// ErrUnknownType is an add of a type the tile library does not have.
var ErrUnknownType = errors.New("unknown tile type")

// addTile adds a library type to the review of the pull request's latest
// version, as one batch. With data, the data is checked against that
// version's hunks (library.CheckAddedFill: its problems name places, never
// values), masked as every model output is and stored as the version's
// fill before the add, so the tile shows at once. Without data, a fill
// starts once the add is saved and the tile is pending until it lands.
func (a LocalAPI) addTile(ctx context.Context, c repos.Clone, number int, by string, add AddTile) (AddedTile, error) {
	entry, ok := library.Find(add.Type)
	if !ok {
		return AddedTile{}, ErrUnknownType
	}
	shown, err := a.shownTiles(ctx, c, number, 0)
	if err != nil {
		return AddedTile{}, err
	}
	id := tiles.LibraryTileID(entry.Definition.Name)
	// With no place, an added tile stands right after the overview, where
	// the person sees it, rather than past the landing page.
	if add.To == nil && slices.ContainsFunc(shown.Document.Layout.Entries, func(e tiles.Entry) bool { return e.Tile == tiles.TypeOverview && e.Group != tiles.GroupHidden }) {
		add.To = &tiles.Place{After: tiles.TypeOverview}
	}
	op := tiles.Op{Op: tiles.OpAdd, Size: add.Size, To: add.To, Add: &tiles.Added{Kind: "library", Name: entry.Definition.Name, Digest: entry.Definition.Digest}}
	if reason := tiles.CheckOp(shown.Document, op); reason != "" {
		return AddedTile{}, errors.New(reason)
	}
	data := add.Data
	if len(data) > 0 && string(data) != "null" {
		if problems := library.CheckAddedFill(fillValidator(), entry, data, library.DiffOf(shown.Document)); len(problems) > 0 {
			return AddedTile{}, fmt.Errorf("The data does not fit the %s tile, so nothing changed: %s", entry.Definition.Title, strings.Join(problems[:min(len(problems), 12)], "; "))
		}
		masked, err := maskData(ctx, data)
		if err != nil {
			return AddedTile{}, err
		}
		if err := a.Layouts.PutFill(ctx, tilelayout.Key(c.Repository, number), id, shown.Version, entry.Encoded, masked); err != nil {
			return AddedTile{}, err
		}
	} else {
		data = nil
	}
	change, err := a.editLayout(ctx, c, number, by, []tiles.Op{op})
	if err != nil {
		return AddedTile{}, err
	}
	if !change.Saved {
		reasons := make([]string, len(change.Refused))
		for i, refusal := range change.Refused {
			reasons[i] = refusal.Reason
		}
		return AddedTile{Change: change}, errors.New(strings.Join(reasons, "; "))
	}
	out := AddedTile{Tile: id, State: "ready", Change: change}
	if data == nil && !slices.Contains(shownLibrary(shown.Document), id) {
		out.State = "filling"
		if a.Fills != nil {
			a.Fills.Fill(tilelayout.FillRequest{Clone: c.ID, Repository: c.Repository, Number: number, Version: shown.Version, Type: entry.Definition.Name})
		}
	}
	return out, nil
}

// shownLibrary are the IDs of the custom tiles a document shows.
func shownLibrary(doc *tiles.Document) []string {
	var out []string
	for _, tile := range doc.Tiles {
		if tile.Type == tiles.TypeCustom && tile.State == tiles.StateReady {
			out = append(out, tile.ID)
		}
	}
	return out
}

// maskData masks secret spans in data the agent wrote, as every model
// output is masked before it is stored.
func maskData(ctx context.Context, data json.RawMessage) (json.RawMessage, error) {
	redactor, err := bundle.NewRedactor()
	if err != nil {
		return nil, err
	}
	var scratch agent.Outcome
	masked, err := agent.RedactTaskOutput(ctx, redactor, data, &scratch)
	if err != nil {
		return nil, errors.New("the data could not be checked for secrets, so nothing changed")
	}
	return masked, nil
}
