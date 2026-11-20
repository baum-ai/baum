package uiserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/addedtiles"
	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/internal/viewserver"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/reviewv2"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// The tiles a person added to a pull request's review page
// (internal/addedtiles) show on every version the tile route serves, on the
// section they were added on. On each version an added tile shows what
// generation filled for it, or else the fill the host holds
// (addedtiles.AddedFor). On the latest version a tile with no fill of its
// own shows its last data, or pending, while a fill runs (LocalAPI.Fills),
// started when the tile is added without data and when a version that
// needs one is served. POST /api/tiles/added removes one, and its Undo
// restores it.

// TileFiller starts filling added tiles; jobs.TileFills implements it.
type TileFiller interface {
	// Fill starts a fill of an added tile for a version, in the
	// background, unless one runs or recently failed.
	Fill(request addedtiles.FillRequest)
}

// fillValidator checks data the agent wrote for an added tile.
var fillValidator = sync.OnceValue(tiles.NewValidator)

// addedState is a pull request's added tiles, their fills, and what names
// both in a cache key.
func (a LocalAPI) addedState(ctx context.Context, repository string, number int) ([]addedtiles.Added, []addedtiles.Fill, string) {
	if a.Added == nil {
		return nil, nil, ""
	}
	pr := addedtiles.Key(repository, number)
	added, err := a.Added.List(ctx, pr)
	if err != nil {
		a.logger().Warn("the added tiles could not be read", "error", err.Error())
		return nil, nil, ""
	}
	if len(added) == 0 {
		return nil, nil, ""
	}
	fills, err := a.Added.Fills(ctx, pr)
	if err != nil {
		a.logger().Warn("the fills of added tiles could not be read", "error", err.Error())
	}
	sum := sha256.New()
	for _, t := range added {
		_, _ = fmt.Fprintf(sum, "%s\x00%s\x00%s\x00%s\x00", t.Tile, t.Kind, t.Digest, t.Section)
	}
	for _, f := range fills {
		_, _ = fmt.Fprintf(sum, "%s\x00%d\x00%s\x00", f.Tile, f.Version, f.Digest)
		sum.Write(f.Data)
	}
	return added, fills, hex.EncodeToString(sum.Sum(nil))
}

// addedFor is what a version is served with of the tiles the person added:
// what the host holds for each on it. On the latest version, the added
// tiles it has no fill for are filled (LocalAPI.Fills).
func (a LocalAPI) addedFor(ctx context.Context, c repos.Clone, number int, version store.Version, latest bool, added []addedtiles.Added, fills []addedtiles.Fill) []tileview.AddedTile {
	if len(added) == 0 {
		return nil
	}
	v := addedtiles.Version{Number: version.Metadata.Version, Latest: latest, Generated: addedtiles.Generated(version.Composition, version.Document), Diff: addedtiles.DiffOfStored(version.Document)}
	held, missing := addedtiles.AddedFor(added, fills, v, func(digest string) (json.RawMessage, bool) { return a.Added.Definition(ctx, digest) })
	if a.Fills != nil {
		for _, add := range missing {
			request := addedtiles.FillRequest{Clone: c.ID, Repository: c.Repository, Number: number, Version: version.Metadata.Version, Type: add.Name}
			if add.Kind == addedtiles.KindCustom {
				request.Digest = add.Digest
			}
			a.Fills.Fill(request)
		}
	}
	return held
}

// shownVersion is a pull request's latest saved version as its page shows
// it: its number, the review as a baum.review/2 document (for its sections,
// one placement rule for both schemas) and the tile document converted from
// it, which an added tile's data is checked against.
type shownVersion struct {
	Version  int
	Review   reviewv2.Document
	Document *tiles.Document
	// owners maps each tile of a baum.review/2 version to its section, so
	// an add asked for on a tile goes on the section that shows it.
	owners map[string]string
}

// errNoTiles is a saved version that cannot be shown with tiles.
var errNoTiles = errors.New("This review cannot be shown with tiles")

// latestShown reads the pull request's latest saved version.
func (a LocalAPI) latestShown(ctx context.Context, c repos.Clone, number int) (shownVersion, error) {
	s, err := store.Open(c.CommonDir, "", false)
	if err != nil {
		return shownVersion{}, err
	}
	version, err := s.Latest(ctx, store.Key{Repository: c.Repository, Number: number})
	if err != nil {
		return shownVersion{}, err
	}
	if len(version.Document) > viewserver.MaxDocumentBytes {
		return shownVersion{}, errors.New("The saved review is too large to show")
	}
	if reviewv2.IsV2(version.Document) {
		// A baum.review/2 version: its added tiles are checked against its
		// substrate, as the tile route serves them.
		var d reviewv2.Document
		if err := json.Unmarshal(version.Document, &d); err != nil {
			return shownVersion{}, errNoTiles
		}
		owners := map[string]string{}
		for _, t := range d.Tiles {
			owners[t.ID] = t.Owner
		}
		return shownVersion{Version: version.Metadata.Version, Review: d, Document: reviewv2.Substrate(d), owners: owners}, nil
	}
	normalized, err := review.NormalizeStored(version.Document)
	if err != nil {
		return shownVersion{}, errNoTiles
	}
	r, err := reviewv2.FromV1(normalized)
	if err != nil {
		return shownVersion{}, errNoTiles
	}
	doc, err := tiles.FromReview(normalized, tiles.Options{})
	if err != nil {
		return shownVersion{}, errNoTiles
	}
	return shownVersion{Version: version.Metadata.Version, Review: r, Document: doc}, nil
}

// section is where a tile added to the shown version goes: the section
// asked for when the review has it (or context), else the one whose hunks
// the data cites most, else context. Data that cites nothing yet (a tile
// being filled) with no section asked keeps none: the page places it by
// what its fill cites. A tile asked for stands for the section it is on.
func (s shownVersion) section(asked string, data json.RawMessage) string {
	if owner, ok := s.owners[asked]; ok {
		asked = owner
		if owner == reviewv2.OwnerOverview {
			asked = ""
		}
	}
	var hunks []string
	for _, c := range library.Citations(data) {
		if c.Type == "hunk" {
			hunks = append(hunks, c.ID)
		}
	}
	if asked == "" && len(hunks) == 0 {
		return ""
	}
	return reviewv2.OwnerFor(s.Review, asked, hunks)
}

// AddTile is an add_tile request: the library type, the section it goes on
// and the data the agent wrote for it, if any.
type AddTile struct {
	Type    string
	Section string
	Data    json.RawMessage
}

// AddedTile is what an add did: the tile's ID, whether it shows its data
// ("ready") or is being filled ("filling"), and its section.
type AddedTile struct {
	Tile    string
	State   string
	Section string
}

// ErrUnknownType is an add of a type the tile library does not have.
var ErrUnknownType = errors.New("unknown tile type")

// roomFor refuses an add of a tile that is not added yet to a pull request
// that already has tiles.MaxLibraryTiles added tiles: the page shows no
// more.
func (a LocalAPI) roomFor(ctx context.Context, pr, id string) error {
	added, err := a.Added.List(ctx, pr)
	if err != nil {
		return err
	}
	if len(added) >= tiles.MaxLibraryTiles && !slices.ContainsFunc(added, func(t addedtiles.Added) bool { return t.Tile == id }) {
		return fmt.Errorf("This pull request's review already has %d added tiles; ask the person to remove one first", tiles.MaxLibraryTiles)
	}
	return nil
}

// addTile adds a library type to the pull request's review page. With
// data, the data is checked against the latest version's hunks
// (library.CheckAddedFill: its problems name places, never values), masked
// as every model output is and stored as the version's fill (the agent's,
// OriginChat), so the tile shows at once. Without data, a fill starts and
// the tile is pending until it lands, unless the version already has a
// fill for it.
func (a LocalAPI) addTile(ctx context.Context, c repos.Clone, number int, by string, add AddTile) (AddedTile, error) {
	if a.Added == nil {
		return AddedTile{}, errors.New("adding tiles is not available in this baum")
	}
	entry, ok := library.Find(add.Type)
	if !ok {
		return AddedTile{}, ErrUnknownType
	}
	shown, err := a.latestShown(ctx, c, number)
	if err != nil {
		return AddedTile{}, err
	}
	id := tiles.LibraryTileID(entry.Definition.Name)
	pr := addedtiles.Key(c.Repository, number)
	if err := a.roomFor(ctx, pr, id); err != nil {
		return AddedTile{}, err
	}
	var masked json.RawMessage
	if len(add.Data) > 0 && string(add.Data) != "null" {
		if problems := library.CheckAddedFill(fillValidator(), entry, add.Data, library.DiffOf(shown.Document)); len(problems) > 0 {
			return AddedTile{}, fmt.Errorf("The data does not fit the %s tile, so nothing changed: %s", entry.Definition.Title, strings.Join(problems[:min(len(problems), 12)], "; "))
		}
		if masked, err = maskData(ctx, add.Data); err != nil {
			return AddedTile{}, err
		}
	}
	section := shown.section(strings.TrimSpace(add.Section), masked)
	if err := a.Added.Add(ctx, pr, addedtiles.Added{Tile: id, Kind: addedtiles.KindLibrary, Name: entry.Definition.Name, Digest: entry.Definition.Digest, Section: section, By: by}); err != nil {
		return AddedTile{}, err
	}
	out := AddedTile{Tile: id, State: "ready", Section: section}
	if masked != nil {
		if err := a.Added.PutFill(ctx, pr, id, shown.Version, addedtiles.OriginChat, entry.Encoded, masked); err == nil {
			return out, nil
		}
	}
	// A fill held for this version (a tile removed and added again) is shown
	// as it is: a version's type is filled once.
	if fills, err := a.Added.Fills(ctx, pr); err == nil && slices.ContainsFunc(fills, func(f addedtiles.Fill) bool { return f.Tile == id && f.Version == shown.Version }) {
		return out, nil
	}
	out.State = "filling"
	if a.Fills != nil {
		a.Fills.Fill(addedtiles.FillRequest{Clone: c.ID, Repository: c.Repository, Number: number, Version: shown.Version, Type: entry.Definition.Name})
	}
	return out, nil
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

// addedRoutes mounts POST /api/tiles/added ({clone, pr, tile, action:
// remove or restore}): the page's Remove on an added tile's menu, and the
// Undo of its toast. It answers {tile, action}, 404 when the pull request
// has no such added tile (or none removed to restore), 400 for a malformed
// request and 503 without added tiles.
func (a LocalAPI) addedRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/tiles/added", func(w http.ResponseWriter, r *http.Request) {
		if a.Added == nil {
			http.Error(w, "capability unavailable", http.StatusServiceUnavailable)
			return
		}
		var request struct {
			Clone  string `json:"clone"`
			PR     int    `json:"pr"`
			Tile   string `json:"tile"`
			Action string `json:"action"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid JSON request", http.StatusBadRequest)
			return
		}
		if request.PR < 1 || request.Tile == "" {
			http.Error(w, "positive pr and a tile required", http.StatusBadRequest)
			return
		}
		c, ok := a.cloneByID(w, r, request.Clone)
		if !ok {
			return
		}
		pr := addedtiles.Key(c.Repository, request.PR)
		var err error
		switch request.Action {
		case "remove":
			err = a.Added.Remove(r.Context(), pr, request.Tile)
		case "restore":
			err = a.Added.Restore(r.Context(), pr, request.Tile)
		default:
			http.Error(w, "action is remove or restore", http.StatusBadRequest)
			return
		}
		switch {
		case errors.Is(err, addedtiles.ErrNotFound):
			http.NotFound(w, r)
			return
		case err != nil:
			http.Error(w, addedtiles.ErrUnsaved.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]string{"tile": request.Tile, "action": request.Action})
	})
}

// AddedTilesAPI is the session panel's way to add library tiles
// (agentcontext.AddedTiles), added by "chat".
type AddedTilesAPI struct {
	API LocalAPI
}

var _ agentcontext.AddedTiles = AddedTilesAPI{}

// Add implements agentcontext.AddedTiles.
func (e AddedTilesAPI) Add(ctx context.Context, repository string, number int, add agentcontext.TileAdd) (agentcontext.TileAdded, error) {
	c, err := e.API.pullClone(ctx, repository, number)
	if err != nil {
		return agentcontext.TileAdded{}, err
	}
	added, err := e.API.addTile(ctx, c, number, "chat", AddTile(add))
	if errors.Is(err, ErrUnknownType) {
		return agentcontext.TileAdded{}, errors.New("No tile type " + add.Type + " in baum's tile library")
	}
	if err != nil {
		return agentcontext.TileAdded{}, err
	}
	return agentcontext.TileAdded(added), nil
}

// pullClone is the registered, available clone of a repository whose review
// store holds a saved version of the pull request.
func (a LocalAPI) pullClone(ctx context.Context, repository string, number int) (repos.Clone, error) {
	clones, err := a.Registry.List(ctx)
	if err != nil {
		return repos.Clone{}, err
	}
	found := false
	for _, c := range clones {
		if !strings.EqualFold(c.Repository, repository) {
			continue
		}
		found = true
		s, err := store.Open(c.CommonDir, "", false)
		if err != nil {
			continue
		}
		if _, err := s.Latest(ctx, store.Key{Repository: c.Repository, Number: number}); err == nil {
			return c, nil
		}
	}
	if !found {
		return repos.Clone{}, errors.New("The repository " + repository + " has no registered clone")
	}
	return repos.Clone{}, errors.New("Pull request #" + strconv.Itoa(number) + " has no saved review yet")
}
