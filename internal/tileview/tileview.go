// Package tileview builds the answer of the daemon's tile routes: a review
// document and the tile document converted from it, in one JSON body.
package tileview

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync/atomic"

	"github.com/baum-ai/baum/internal/etagjson"
	"github.com/baum-ai/baum/internal/workstats"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/tiles"
)

// Response is the body of a tile route. Document is the baum.review/1
// document the other routes serve, with the stored-document rules applied and
// the derived hunk URLs added; Tiles is the tile document converted from
// exactly those bytes, or nil with Problem naming why it could not be. The
// problem never quotes the document.
type Response struct {
	Document json.RawMessage `json:"document"`
	Tiles    *tiles.Document `json:"tiles"`
	Problem  string          `json:"problem,omitempty"`
}

// validator checks the tiles of every document served. It is shared because
// it compiles its schemas once.
var validator = tiles.NewValidator()

// Warm compiles the schemas the tile checks use, which the first Build would
// otherwise do (about 0.4 s). The daemon calls it in the background at start-up
// so the first review a person opens does not wait for it.
func Warm() error { return validator.CheckTiles(&tiles.Document{}) }

// Builds counts the conversions Build has run, for tests.
var Builds atomic.Int64

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
// tests cover.
func Build(document, composition []byte, opts tiles.Options) Response {
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
	doc = compose(doc, composition)
	if err := validator.CheckTiles(doc); err != nil {
		return Response{Document: raw(normalized), Problem: "The tiles could not be checked."}
	}
	return Response{Document: raw(normalized), Tiles: doc}
}

// compose applies the stored composition to the converted document, or
// returns it as it is when there is none or none can be used.
func compose(doc *tiles.Document, composition []byte) *tiles.Document {
	if len(composition) == 0 || string(composition) == "null" {
		return doc
	}
	parsed, err := tiles.ParseComposition(composition)
	if err != nil {
		workstats.Inc("tileview.compositions_ignored")
		return doc
	}
	if parsed.Empty() {
		return doc
	}
	composed, err := tiles.Compose(doc, parsed)
	if err != nil {
		workstats.Inc("tileview.compositions_ignored")
		return doc
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

// Encode returns the encoded response with its ETag.
func Encode(r Response) (etagjson.Body, error) { return etagjson.Encode(r) }
