package tileview

import (
	"encoding/json"

	"github.com/baum-ai/baum/internal/etagjson"
	"github.com/baum-ai/baum/internal/tiledrafts"
	"github.com/baum-ai/baum/internal/workstats"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/reviewv2"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// Served is the body of the tile routes (GET /api/tiles and a running
// review's draft with tiles=1): the version as a baum.review/2 document,
// whatever schema it was stored in, with the tiles the person added merged
// into its tiles. Problem says why the document could not be read (never
// quoting it), with Document then null or the stored one as it is.
//
// A custom tile's definition is served without its HTML (ServedDefinition),
// as in Response; Frames registers the frame documents.
type Served struct {
	Document json.RawMessage `json:"document"`
	Problem  string          `json:"problem,omitempty"`
	// Drafts are the pending cards of tiles being made in the session panel
	// for the pull request; the tile route sets them on the latest version.
	Drafts []tiledrafts.Draft `json:"drafts,omitempty"`
	Frames Frames             `json:"-"`
	// doc is the served document, for tests.
	doc *reviewv2.Document
}

// Serve builds the tile routes' answer of a stored or draft review document
// with the composition stored beside it, if any, and the tiles the person
// added to its pull request:
//
//   - a baum.review/2 document is served as stored, its hunks' GitHub links
//     derived (review.DeriveHunkURLs). Each tile the person added is checked
//     against it (reviewv2.Substrate, tiles.ExtraTiles), served unavailable
//     with its problems when it fails, and owned by the section it was added
//     on when the document has that section, else by the section whose hunks
//     it cites most, else by context; a type the document already has is not
//     served twice; a section without a body (written before bodies) is
//     served with the one it reads as (reviewv2.EnsureBodies);
//   - a baum.review/1 document is converted (reviewv2.FromV1), its tiles
//     those of the review page's view of it (BuildWith: the composition's
//     native diagrams, library and authored tiles and the added tiles,
//     checked and placed as that view places them; reviewv2.TilesOfView).
//
// Added tiles keep added, from and filling. Every custom tile's frame is
// registered and its definition served without its HTML. Serving never
// fails the answer for a tile.
func Serve(document, composition []byte, added []AddedTile, opts tiles.Options) Served {
	if reviewv2.IsV2(document) {
		return serveV2(document, added)
	}
	built := BuildWith(document, composition, added, opts)
	d, err := reviewv2.FromV1(built.Document)
	if err != nil {
		problem := built.Problem
		if problem == "" {
			problem = "The review could not be read as a review document."
		}
		return Served{Document: raw(document), Problem: problem, Frames: built.Frames}
	}
	if built.View != nil {
		d.Tiles = reviewv2.TilesOfView(*built.View)
		d.Overview = reviewv2.OverviewOfView(*built.View)
		d.Summary.RiskWhy = built.View.Overview.RiskSummary
		reviewv2.EnsureBodies(&d)
	}
	return encodeServed(&d, built.Frames, built.Problem)
}

func encodeServed(d *reviewv2.Document, frames Frames, problem string) Served {
	encoded, err := json.Marshal(d)
	if err != nil {
		return Served{Document: json.RawMessage("null"), Problem: "The review could not be encoded."}
	}
	return Served{Document: encoded, Problem: problem, Frames: frames, doc: d}
}

// serveV2 serves a baum.review/2 document with the added tiles.
func serveV2(document []byte, added []AddedTile) Served {
	Builds.Add(1)
	if derived, err := review.DeriveHunkURLs(document); err == nil {
		document = derived
	}
	var d reviewv2.Document
	if err := json.Unmarshal(document, &d); err != nil {
		return Served{Document: raw(document), Problem: "The review could not be read as a review document."}
	}
	if d.Tiles == nil {
		d.Tiles = []reviewv2.Tile{}
	}
	d.Tiles = append(d.Tiles, addedTiles(d, added)...)
	// A document written before sections had a body is served with one, and
	// every tile a section owns, an added one too, is placed in its body.
	reviewv2.EnsureBodies(&d)
	frames := registerTileFrames(d.Tiles)
	return encodeServed(&d, frames, "")
}

// addedTiles are the person's added tiles as tiles of a baum.review/2
// document: checked against it, owned where they were added or by the
// hunks they cite. A tile whose type or ID the document has is left out.
func addedTiles(d reviewv2.Document, added []AddedTile) (out []reviewv2.Tile) {
	if len(added) == 0 {
		return nil
	}
	defer func() {
		if recover() != nil {
			workstats.Inc("tileview.views_ignored")
			out = nil
		}
	}()
	have := map[string]bool{}
	for _, t := range d.Tiles {
		have[t.ID], have[t.Type] = true, true
	}
	notes := map[string]AddedTile{}
	var library []tiles.LibraryTile
	for _, a := range added {
		name := definitionName(a.Tile.Definition)
		id := tiles.LibraryTileID(name)
		if name == "" || have[id] || have[name] {
			continue
		}
		have[id] = true
		library = append(library, a.Tile)
		notes[id] = a
	}
	if len(library) == 0 {
		return nil
	}
	checked, err := tiles.ExtraTiles(reviewv2.Substrate(d), library, nil)
	if err != nil {
		workstats.Inc("tileview.views_ignored")
		return nil
	}
	for _, t := range checked {
		note := notes[t.ID]
		tile := reviewv2.Tile{ID: t.ID, Type: definitionName(t.Definition), Owner: reviewv2.OwnerFor(d, note.Section, t.Covers), Title: t.Title, Data: t.Data,
			State: t.State, Definition: t.Definition, Problems: t.Problems, Cites: []reviewv2.Anchor{}, Added: true, From: note.From, Filling: note.Filling}
		if tile.Type == "" {
			tile.Type = tiles.TypeCustom
		}
		for _, h := range t.Covers {
			tile.Cites = append(tile.Cites, reviewv2.HunkAnchor(h, nil))
		}
		out = append(out, tile)
	}
	return out
}

// registerTileFrames registers the frame document of each ready tile with
// a definition and serves every definition without its HTML, marked built
// in when it is a built-in library type's. A ready tile whose definition
// fails its checks, or whose frame document cannot be made, becomes
// unavailable with its problems.
func registerTileFrames(ts []reviewv2.Tile) Frames {
	var registered Frames
	for i := range ts {
		tile := &ts[i]
		if len(tile.Definition) == 0 {
			continue
		}
		if tile.State == reviewv2.TileReady {
			def, problems := validator.CheckDefinition(tile.Definition)
			switch {
			case len(problems) > 0:
				tile.State, tile.Problems = reviewv2.TileUnavailable, problems
			default:
				if _, err := frames.Add(def); err != nil {
					tile.State, tile.Problems = reviewv2.TileUnavailable, []string{frameProblem(err)}
				} else {
					registered = append(registered, def)
				}
			}
		}
		builtin := library.IsBuiltin(tile.Definition)
		tile.Definition = ServedDefinition(tile.Definition)
		if builtin {
			tile.Definition = markBuiltin(tile.Definition)
		}
	}
	return registered
}

// Encode returns the encoded answer with its ETag.
func Encode(s Served) (Answer, error) {
	body, err := etagjson.Encode(s)
	return Answer{Body: body, Frames: s.Frames}, err
}
