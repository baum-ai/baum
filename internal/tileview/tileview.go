// Package tileview builds the answer of the daemon's tile routes: a review
// document and the tile document converted from it, in one JSON body.
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
)

// Response is the body of a tile route. Document is the baum.review/1
// document the other routes serve, with the stored-document rules applied and
// the derived hunk URLs added; Tiles is the tile document converted from
// exactly those bytes, or nil with Problem naming why it could not be. The
// problem never quotes the document.
//
// A custom tile's definition is served without its HTML (ServedDefinition):
// the page loads the tile's frame document by its digest, which Frames
// registers. The served definition no longer matches its digest, so nothing
// may check or convert a served tile document again.
type Response struct {
	Document json.RawMessage `json:"document"`
	Tiles    *tiles.Document `json:"tiles"`
	Problem  string          `json:"problem,omitempty"`
	// Added says, of each tile the person added that the served document
	// shows, whether its data is from an earlier version and is being filled
	// for this one (Layout.Notes).
	Added []AddedNote `json:"added,omitempty"`
	// Frames are the definitions of the ready custom tiles, kept to register
	// them again when the answer is served from a cache.
	Frames Frames `json:"-"`
	// checked is the tile document as checked, before its custom tiles'
	// definitions were replaced by their served form; tests check it.
	checked *tiles.Document
}

// validator checks the tiles of every document served. It is shared because
// it compiles its schemas once.
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

// Builds counts the conversions Build has run, for tests.
var Builds atomic.Int64

// fixture, set only in a test binary (SetFixture), changes each converted
// tile document before its tiles are checked.
var fixture atomic.Pointer[func(*tiles.Document)]

// SetFixture makes Build run f on every tile document it converts, before
// its tiles are checked, and returns a function that removes it. It lets a
// test fixture serve custom tiles, which no review generation writes yet.
// It panics outside a test binary, so a release build cannot serve them.
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

// Build converts a stored or draft review document, with the composition
// stored beside it if any: the tile document is
// tiles.Compose(tiles.FromReview(document), composition). A nil or empty
// composition gives the converted document, byte for byte as before
// compositions existed. A composition that does not parse, or that Compose
// refuses, is ignored: the converted document is served with no problem
// shown, and the ignored composition is counted (workstats
// tileview.compositions_ignored). A tile that fails its checks is marked
// unavailable in the result; a conversion failure leaves the tile document
// nil. Build runs only the tile-level checks, not the full schema, which the
// tests cover. It registers the frame documents of the ready custom tiles
// and serves every custom tile's definition without its HTML, composed or
// not.
func Build(document, composition []byte, opts tiles.Options) Response {
	return BuildWith(document, composition, Layout{}, opts)
}

// Layout is a person's own layout of the pull request a version belongs to:
// their overlay (tiles.Overlay, nil for none) and the filled library tiles
// the host holds for the tiles they added, for this version.
type Layout struct {
	Overlay *tiles.Overlay
	Added   []tiles.LibraryTile
	// Notes say, of the added tiles, which show an earlier version's data or
	// none yet while they are filled for this version.
	Notes []AddedNote
}

// AddedNote is what the page says of a tile the person added: From is the
// version its data was written for when that is an earlier one (0 for this
// version's), and Filling that it is being filled for this version.
type AddedNote struct {
	Tile    string `json:"tile"`
	From    int    `json:"from,omitempty"`
	Filling bool   `json:"filling,omitempty"`
}

// LayoutKey names a layout for a cache key: the digest of its overlay's
// active batches, empty for a layout with no edits, so an answer built with
// one layout is never served for another.
func LayoutKey(layout Layout) string {
	if !layout.Overlay.Edited() {
		return ""
	}
	encoded, err := json.Marshal(struct {
		Batches []tiles.Batch       `json:"batches"`
		Added   []tiles.LibraryTile `json:"added,omitempty"`
		Notes   []AddedNote         `json:"notes,omitempty"`
	}{layout.Overlay.Active(), layout.Added, layout.Notes})
	if err != nil {
		return "unencodable"
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// BuildWith is Build with the person's layout applied: the composition the
// tile document is composed with is tiles.ApplyOverlay of the stored one (or
// of the converted layout, when there is none or it cannot be used) with
// their overlay. A layout with no edits gives exactly Build's answer. The
// ops a version drops (their tile is gone) are counted, never shown
// (workstats tileview.overlay_ops_dropped).
func BuildWith(document, composition []byte, layout Layout, opts tiles.Options) Response {
	Builds.Add(1)
	normalized, err := review.NormalizeStored(document)
	if err != nil {
		return Response{Document: raw(document), Problem: "The review could not be read as a review document."}
	}
	if derived, err := review.DeriveHunkURLs(normalized); err == nil {
		normalized = derived
	}
	doc, err := tiles.FromReview(normalized, opts)
	if err != nil {
		return Response{Document: raw(normalized), Problem: "The review could not be converted to tiles."}
	}
	if f := fixture.Load(); f != nil && *f != nil {
		(*f)(doc)
	}
	doc = compose(doc, composition, layout)
	if err := validator.CheckTiles(doc); err != nil {
		return Response{Document: raw(normalized), Problem: "The tiles could not be checked."}
	}
	served, registered := registerFrames(doc)
	return Response{Document: raw(normalized), Tiles: served, Added: notesShown(served, layout.Notes), Frames: registered, checked: doc}
}

// notesShown are the notes of the added tiles the served document shows.
func notesShown(doc *tiles.Document, notes []AddedNote) []AddedNote {
	if len(notes) == 0 || doc == nil {
		return nil
	}
	shown := map[string]bool{}
	for _, tile := range doc.Tiles {
		shown[tile.ID] = true
	}
	var out []AddedNote
	for _, note := range notes {
		if shown[note.Tile] && (note.From > 0 || note.Filling) {
			out = append(out, note)
		}
	}
	return out
}

// registerFrames registers the frame document of each ready custom tile and
// returns the document to serve, with every custom tile's definition in its
// served form, marked built in when it is a built-in library type's, and the
// registered definitions. A ready tile whose document
// cannot be made becomes unavailable, in both documents: its frame would
// never load.
func registerFrames(doc *tiles.Document) (*tiles.Document, Frames) {
	var registered Frames
	served := *doc
	served.Tiles = slices.Clone(doc.Tiles)
	for i := range served.Tiles {
		tile := &served.Tiles[i]
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
					doc.Tiles[i] = *tile
				}
			}
		}
		builtin := library.IsBuiltin(tile.Definition)
		tile.Definition = ServedDefinition(tile.Definition)
		if builtin {
			tile.Definition = markBuiltin(tile.Definition)
		}
	}
	return &served, registered
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

// applyComposition is tiles.Compose; tests replace it to force a failure.
var applyComposition = tiles.Compose

// compose applies the stored composition, with the person's layout, to the
// converted document, or returns it as it is when there is nothing to apply
// or nothing can be: a composition over tiles.MaxStoredComposition bytes,
// one that does not parse, one Compose refuses and one that makes it panic
// are ignored and counted, and the person's layout then applies to the
// converted layout.
func compose(doc *tiles.Document, composition []byte, layout Layout) (out *tiles.Document) {
	parsed := parseComposition(composition)
	if layout.Overlay.Edited() {
		if edited := applyLayout(doc, parsed, layout); edited != nil {
			return edited
		}
	}
	if parsed == nil || parsed.Empty() {
		return doc
	}
	defer func() {
		if recover() != nil {
			workstats.Inc("tileview.compositions_ignored")
			out = doc
		}
	}()
	composed, err := applyComposition(doc, parsed)
	if err != nil {
		workstats.Inc("tileview.compositions_ignored")
		return doc
	}
	return composed
}

// parseComposition reads a stored composition, nil for none; one that
// cannot be used is counted as ignored.
func parseComposition(composition []byte) *tiles.Composition {
	if len(composition) == 0 || string(composition) == "null" {
		return nil
	}
	if len(composition) > tiles.MaxStoredComposition {
		workstats.Inc("tileview.compositions_ignored")
		return nil
	}
	parsed, err := tiles.ParseComposition(composition)
	if err != nil {
		workstats.Inc("tileview.compositions_ignored")
		return nil
	}
	return parsed
}

// applyLayout composes the document with the person's layout replayed on the
// composition (or the converted layout), or returns nil when none of their
// edits applies to this version or composing fails, so the version is served
// as it would be without them.
func applyLayout(doc *tiles.Document, parsed *tiles.Composition, layout Layout) (out *tiles.Document) {
	defer func() {
		if recover() != nil {
			workstats.Inc("tileview.overlays_ignored")
			out = nil
		}
	}()
	c, report := tiles.ApplyOverlay(doc, parsed, layout.Overlay, layout.Added)
	if n := report.DroppedTotal(); n > 0 {
		workstats.Add("tileview.overlay_ops_dropped", int64(n))
	}
	if report.Applied == 0 {
		return nil
	}
	composed, err := applyComposition(doc, &c)
	if err != nil {
		workstats.Inc("tileview.overlays_ignored")
		return nil
	}
	return composed
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

// Encode returns the encoded response with its ETag.
func Encode(r Response) (Answer, error) {
	body, err := etagjson.Encode(r)
	return Answer{Body: body, Frames: r.Frames}, err
}
