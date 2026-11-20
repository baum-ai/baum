// Package tileview builds the answer of the daemon's tile routes: a version
// of a review as a baum.review/2 document, with the tiles the person added
// (Serve). A baum.review/1 version is converted through the review page's
// view of it (baum.view/1, Build and BuildWith), which checks and places its
// composition's tiles; replay still reads that view on its own.
package tileview

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/baum-ai/baum/internal/etagjson"
	"github.com/baum-ai/baum/internal/tileframes"
	"github.com/baum-ai/baum/internal/workstats"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/kit"
	"github.com/baum-ai/baum/tiles/library"
	"github.com/baum-ai/baum/tiles/view"
)

// Response is the review page's view of a baum.review/1 document (Build),
// which Serve converts to the tile routes' answer. Document is the document
// with the stored-document rules applied and the derived hunk URLs added;
// View is the review page's view of exactly those bytes, or nil with Problem
// naming why it could not be built. The problem never quotes the document.
//
// A custom tile's definition is served without its HTML (ServedDefinition):
// the page loads the tile's frame document by its digest, which Frames
// registers, and that definition no longer matches its digest, so nothing
// may check a served tile again.
type Response struct {
	Document json.RawMessage
	// View is the review page's view (baum.view/1, view.Build): the
	// overview's model-written parts, every section with the tiles placed on
	// it, and the tiles. It is built from the review and its review view
	// composition (baum.composition/2); a version without one, or with an
	// older composition, gets the empty view (view.Empty) with only the
	// tiles the person added.
	View    *view.View
	Problem string
	// Frames are the definitions of the ready custom tiles, kept to register
	// them again when the answer is served from a cache.
	Frames Frames
	// checked are the view's library, authored and added tiles as checked,
	// before their custom definitions were replaced by their served form;
	// tests check them.
	checked []tiles.Tile
}

// validator checks the custom tiles a fixture adds. It is shared because it
// compiles its schemas once.
var validator = tiles.NewValidator()

// frames holds the frame documents of the custom tiles the tile routes
// served; the app server's frame route reads it.
var frames = tileframes.New(tileframes.MaxBytes)

// FrameRegistry returns the registry of the frame documents of every custom
// tile the tile routes served.
func FrameRegistry() *tileframes.Registry { return frames }

// Frames are the checked definitions of a response's ready custom tiles.
type Frames []tiles.TypeDefinition

// Register registers the definitions' frame documents again, which marks
// them recently used or restores one the registry dropped.
func (f Frames) Register() {
	for _, def := range f {
		_, _ = frames.Add(def)
	}
}

// Size is the bytes of HTML the definitions hold.
func (f Frames) Size() int {
	n := 0
	for _, def := range f {
		n += len(def.Source.HTML)
	}
	return n
}

// Warm compiles the schemas the tile checks use, which the first Build would
// otherwise do (about 0.4 s), and registers the built-in library types' frame
// documents (RegisterLibrary). The daemon calls it in the background at
// start-up so the first review a person opens does not wait for it.
func Warm() error {
	if err := validator.CheckTiles(&tiles.Document{}); err != nil {
		return err
	}
	return RegisterLibrary()
}

// Builds counts the views Build has built, for tests.
var Builds atomic.Int64

// fixture, set only in a test binary (SetFixture), changes each converted
// tile document before the view is built.
var fixture atomic.Pointer[func(*tiles.Document)]

// SetFixture makes Build run f on every tile document it converts, before
// the view is built, and returns a function that removes it: each custom
// tile f adds is checked and served in the view, placed on the section
// whose hunks it cites most. It lets a test fixture serve custom tiles that
// no review generation writes. It panics outside a test binary, so a
// release build cannot serve them.
func SetFixture(f func(*tiles.Document)) (restore func()) {
	if !testing.Testing() {
		panic("tileview: SetFixture is for test binaries only")
	}
	previous := fixture.Swap(&f)
	return func() { fixture.Store(previous) }
}

// Key names a composition for a cache key: the digest of its bytes, empty for
// none, so an answer built with one is never served for another.
func Key(composition []byte) string {
	if len(composition) == 0 {
		return ""
	}
	sum := sha256.Sum256(composition)
	return hex.EncodeToString(sum[:])
}

// AddedTile is a tile the person added to the pull request, as one version
// is served with it: the library tile (its type's definition and the data
// the host holds for this version, pending without any), the section it
// sits on (empty or unknown: placed by the hunks it covers), and, when its
// data is an earlier version's, that version (From) and whether it is being
// filled for this one (Filling).
type AddedTile struct {
	Tile    tiles.LibraryTile
	Section string
	From    int
	Filling bool
}

// AddedKey names a version's added tiles for a cache key, empty for none,
// so an answer built with some is never served for others.
func AddedKey(added []AddedTile) string {
	if len(added) == 0 {
		return ""
	}
	encoded, err := json.Marshal(added)
	if err != nil {
		return "unencodable"
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// Build builds the view of a stored or draft review document with the
// composition stored beside it, if any. It is BuildWith with no added
// tiles.
func Build(document, composition []byte, opts tiles.Options) Response {
	return BuildWith(document, composition, nil, opts)
}

// BuildWith builds the view of a stored or draft review document, with the
// composition stored beside it and the tiles the person added to its pull
// request. The library and authored tiles of a review view composition
// (baum.composition/2), and the added tiles, are checked against the tile
// document converted from the review (tiles.ExtraTiles): one that fails is
// served unavailable with its problems. An added tile whose type the
// composition already fills is not served twice. Any other composition is
// ignored (a version shown as one without), and one that cannot be used is
// counted (workstats tileview.views_ignored). Building the view never
// fails the answer for a composition or an added tile: a panic gives the
// view without them. It registers the frame documents of the ready custom
// tiles and serves every custom tile's definition without its HTML.
func BuildWith(document, composition []byte, added []AddedTile, opts tiles.Options) Response {
	Builds.Add(1)
	normalized, err := review.NormalizeStored(document)
	if err != nil {
		return Response{Document: raw(document), Problem: "The review could not be read as a review document."}
	}
	if derived, err := review.DeriveHunkURLs(normalized); err == nil {
		normalized = derived
	}
	r, err := view.ParseReview(normalized)
	if err != nil {
		return Response{Document: raw(normalized), Problem: "The review could not be read for its page."}
	}
	doc, err := tiles.FromReview(normalized, opts)
	if err != nil {
		return Response{Document: raw(normalized), Problem: "The review could not be converted to tiles."}
	}
	var fixed []tiles.Tile
	if f := fixture.Load(); f != nil && *f != nil {
		fixed = fixtureTiles(doc, *f)
	}
	v, checked := pageView(r, doc, composition, added, fixed)
	served, registered := registerFrames(v, checked)
	return Response{Document: raw(normalized), View: served, Frames: registered, checked: checked}
}

// fixtureTiles runs a test fixture on a converted document and returns the
// custom tiles it added, checked.
func fixtureTiles(doc *tiles.Document, f func(*tiles.Document)) []tiles.Tile {
	before := len(doc.Tiles)
	f(doc)
	_ = validator.CheckTiles(doc)
	var out []tiles.Tile
	for _, t := range doc.Tiles[before:] {
		if t.Type == tiles.TypeCustom {
			out = append(out, t)
		}
	}
	return out
}

// pageView is the review page's view of a review r, and its library,
// authored, added and fixture tiles as checked. A composition that is not a
// review view composition, or that cannot be used, gives the empty view
// with the added tiles; one that cannot be used is counted
// (tileview.views_ignored).
func pageView(r *view.Review, doc *tiles.Document, composition []byte, added []AddedTile, fixed []tiles.Tile) (*view.View, []tiles.Tile) {
	var c *view.Composition
	var library []tiles.LibraryTile
	var authored []tiles.AuthoredTile
	if len(composition) > 0 && view.IsComposition(composition) {
		parsed, err := view.Parse(composition)
		if err != nil {
			workstats.Inc("tileview.views_ignored")
		} else {
			c, library, authored = parsed, parsed.Library, parsed.Authored
		}
	}
	// The added tiles follow the composition's; a type the composition
	// already fills is shown once, as the composition has it.
	on := map[string]string{}
	notes := map[string]AddedTile{}
	have := map[string]bool{}
	for _, lib := range library {
		have[tiles.LibraryTileID(definitionName(lib.Definition))] = true
	}
	for _, a := range added {
		id := tiles.LibraryTileID(definitionName(a.Tile.Definition))
		if id == "" || have[id] {
			continue
		}
		have[id] = true
		library = append(library, a.Tile)
		on[id], notes[id] = a.Section, a
	}
	extras := extraTiles(doc, library, authored)
	for _, t := range fixed {
		if !slices.ContainsFunc(extras, func(have tiles.Tile) bool { return have.ID == t.ID }) {
			extras = append(extras, t)
		}
	}
	v := buildView(r, c, extras, on)
	for i := range v.Tiles {
		if note, ok := notes[v.Tiles[i].ID]; ok {
			v.Tiles[i].Added, v.Tiles[i].From, v.Tiles[i].Filling = true, note.From, note.Filling
		}
	}
	return &v, extras
}

// extraTiles checks the library and authored tiles against the converted
// document; a panic or a document that cannot take them gives none, counted
// as an ignored view.
func extraTiles(doc *tiles.Document, library []tiles.LibraryTile, authored []tiles.AuthoredTile) (out []tiles.Tile) {
	if len(library) == 0 && len(authored) == 0 {
		return nil
	}
	defer func() {
		if recover() != nil {
			workstats.Inc("tileview.views_ignored")
			out = nil
		}
	}()
	out, err := tiles.ExtraTiles(doc, library, authored)
	if err != nil {
		workstats.Inc("tileview.views_ignored")
		return nil
	}
	return out
}

// buildView is view.Build of the composition as view.Check reads it, the
// extra tiles' IDs being those it may name; a panic gives the empty view,
// counted.
func buildView(r *view.Review, c *view.Composition, extras []tiles.Tile, on map[string]string) (out view.View) {
	defer func() {
		if recover() != nil {
			workstats.Inc("tileview.views_ignored")
			out = view.Empty(r)
		}
	}()
	var checked *view.Composition
	if c != nil {
		ids := view.ExtraIDs(c)
		for _, t := range extras {
			if !slices.Contains(ids, t.ID) {
				ids = append(ids, t.ID)
			}
		}
		n := view.Check(r, c, ids)
		checked = &n
	}
	return view.Build(r, checked, extras, on)
}

// definitionName is the name a definition gives, or "".
func definitionName(definition json.RawMessage) string {
	var def struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(definition, &def)
	return def.Name
}

// registerFrames registers the frame document of each ready custom tile of
// the view and returns the view to serve, with every custom tile's
// definition in its served form, marked built in when it is a built-in
// library type's, and the registered definitions. A ready tile whose
// document cannot be made becomes unavailable, in both the view and
// checked: its frame would never load.
func registerFrames(v *view.View, checked []tiles.Tile) (*view.View, Frames) {
	var registered Frames
	for i := range v.Tiles {
		tile := &v.Tiles[i]
		if tile.Type != tiles.TypeCustom {
			continue
		}
		if tile.State == tiles.StateReady {
			def, problems := validator.CheckDefinition(tile.Definition)
			if len(problems) == 0 {
				_, err := frames.Add(def)
				if err == nil {
					registered = append(registered, def)
				} else {
					tile.State, tile.Problems, tile.Covers = tiles.StateUnavailable, []string{frameProblem(err)}, nil
					if j := slices.IndexFunc(checked, func(t tiles.Tile) bool { return t.ID == tile.ID }); j >= 0 {
						checked[j].State, checked[j].Problems, checked[j].Covers = tile.State, tile.Problems, nil
					}
				}
			}
		}
		builtin := library.IsBuiltin(tile.Definition)
		tile.Definition = ServedDefinition(tile.Definition)
		if builtin {
			tile.Definition = markBuiltin(tile.Definition)
		}
	}
	return v, registered
}

// frameProblem says why a ready tile's frame document could not be made,
// without quoting it.
func frameProblem(err error) string {
	if errors.Is(err, kit.ErrTooLarge) {
		return fmt.Sprintf("the tile's HTML is larger than %d KiB in UTF-8; make it smaller", kit.MaxAuthorHTML>>10)
	}
	return "the tile's frame document could not be made"
}

// ServedDefinition returns a custom tile's definition as the tile routes
// serve it: source.html is replaced by its length in bytes, so source is
// {"format", "bytes"}, and every other member is kept, digest included. The
// page needs the title, kit, sizes and digest; the frame route serves the
// HTML by digest. A definition that is not a JSON object is served as {}.
//
// A served definition is a projection, and the validator rejects it on
// purpose: it no longer matches its digest. Anything that needs the source
// (saving a type to a library, exporting it, the render check) reads it from
// the stored definition, never from a served answer.
func ServedDefinition(definition json.RawMessage) json.RawMessage {
	var members map[string]json.RawMessage
	if json.Unmarshal(definition, &members) != nil || members == nil {
		return json.RawMessage(`{}`)
	}
	// Only the daemon says a type is built in (markBuiltin).
	delete(members, "builtin")
	source, ok := members["source"]
	if !ok {
		return definition
	}
	var parts map[string]json.RawMessage
	_ = json.Unmarshal(source, &parts)
	served := map[string]any{"bytes": 0}
	if format, ok := parts["format"]; ok {
		served["format"] = format
	}
	var html string
	if json.Unmarshal(parts["html"], &html) == nil {
		served["bytes"] = len(html)
	}
	members["source"] = mustMarshal(served)
	return mustMarshal(members)
}

// ServedLibraryDefinition is ServedDefinition for a definition read from the
// library, built in when it is a built-in type's by its content's digest
// (library.IsBuiltin), as the tile routes serve it.
func ServedLibraryDefinition(definition json.RawMessage) json.RawMessage {
	served := ServedDefinition(definition)
	if library.IsBuiltin(definition) {
		served = markBuiltin(served)
	}
	return served
}

// markBuiltin adds "builtin": true to a served definition: the stored one
// is a built-in library type's, current or past, by its content's digest
// (library.IsBuiltin). The page names such a tile as baum's own.
func markBuiltin(served json.RawMessage) json.RawMessage {
	var members map[string]json.RawMessage
	if json.Unmarshal(served, &members) != nil || members == nil {
		return served
	}
	members["builtin"] = json.RawMessage("true")
	return mustMarshal(members)
}

// mustMarshal encodes decoded JSON, which cannot fail.
func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// raw is document as a JSON value, or null when it is not valid JSON.
func raw(document []byte) json.RawMessage {
	if !json.Valid(document) {
		return json.RawMessage("null")
	}
	return document
}

// Answer is an encoded response with the frames to register again whenever
// it is served.
type Answer struct {
	Body   etagjson.Body
	Frames Frames
}
