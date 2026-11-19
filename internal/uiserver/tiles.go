package uiserver

import (
	"context"
	"net/http"

	"github.com/baum-ai/baum/internal/addedtiles"
	"github.com/baum-ai/baum/internal/etagjson"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/tiledrafts"
	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/internal/viewserver"
	"github.com/baum-ai/baum/tiles"
)

// reviewRead is a request's saved review: the clone and store it is read
// from, and the store's revision of the version the request names.
type reviewRead struct {
	clone    repos.Clone
	store    *store.Store
	key      store.Key
	n        int
	number   int
	revision int64
}

// selectReview resolves the selector of GET /api/document (answering its
// 400, 404 and 500) and the store revision of the version it names.
func (a LocalAPI) selectReview(w http.ResponseWriter, r *http.Request) (reviewRead, bool) {
	c, key, n, ok := a.selector(w, r)
	if !ok {
		return reviewRead{}, false
	}
	s, err := store.Open(c.CommonDir, "", false)
	if err != nil {
		apiError(w, err)
		return reviewRead{}, false
	}
	number, revision, err := s.Revision(r.Context(), key, n)
	if err != nil {
		apiError(w, err)
		return reviewRead{}, false
	}
	return reviewRead{clone: c, store: s, key: key, n: n, number: number, revision: revision}, true
}

// revisionKey names the version's bytes without reading them.
func (rr reviewRead) revisionKey() string {
	return revisionKey(rr.clone, rr.store, rr.key, rr.number, rr.revision)
}

// version reads the version the request names, answering what a failed or
// oversized read is.
func (rr reviewRead) version(ctx context.Context, w http.ResponseWriter) (store.Version, bool) {
	var version store.Version
	var err error
	if rr.n == 0 {
		version, err = rr.store.Latest(ctx, rr.key)
	} else {
		version, err = rr.store.Get(ctx, rr.key, rr.n)
	}
	if err != nil {
		apiError(w, err)
		return store.Version{}, false
	}
	if len(version.Document) > viewserver.MaxDocumentBytes {
		http.Error(w, "stored document too large", http.StatusRequestEntityTooLarge)
		return store.Version{}, false
	}
	return version, true
}

// tileLibrary serves GET /api/tile-library: the built-in library tile types
// this version ships (tileview.Library), as {"types": [...]}, each with its
// name, title, use-when line, glance line, sizes, kit and digest, never its
// source. The review page names a custom tile whose digest is listed as
// baum's own.
func tileLibrary(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"types": tileview.Library()})
}

// tilesHandler serves GET /api/tiles: the version as a baum.review/2
// document (tileview.Serve: a baum.review/2 version as stored, a
// baum.review/1 version converted with its stored composition, if any),
// with the tiles the person added to the pull request (LocalAPI.Added)
// merged into its tiles, as {"document", "problem"?, "drafts"?}, with the
// selectors, errors and no-store ETag of GET /api/document. Answers are kept
// under the version's store revision, which changes with every write to its
// row, composition included, the added tiles' key (with their fills), which
// changes with every add, remove and fill, and the pending cards of tiles
// being made in the session panel (drafts, on the latest version only),
// which change as the tile is checked.
func (a LocalAPI) tilesHandler(slots chan struct{}, answers *tileview.Cache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !acquire(r.Context(), slots) {
			return
		}
		defer func() { <-slots }()
		read, ok := a.selectReview(w, r)
		if !ok {
			return
		}
		added, fills, addedKey := a.addedState(r.Context(), read.clone.Repository, read.key.Number)
		// The latest version shows the pending cards of tiles being made in
		// the session panel.
		var drafts []tiledrafts.Draft
		if read.n == 0 {
			drafts = a.Drafts.List(addedtiles.Key(read.clone.Repository, read.key.Number))
		}
		draftsKey := tiledrafts.Key(drafts)
		withAdded := func(key string) string {
			if key == "" {
				return key
			}
			if addedKey != "" {
				key += "\x00added\x00" + addedKey
			}
			if draftsKey != "" {
				key += "\x00drafts\x00" + draftsKey
			}
			return key
		}
		answer, found := answers.Get(withAdded(read.revisionKey()))
		if !found {
			version, ok := read.version(r.Context(), w)
			if !ok {
				return
			}
			latest := read.n == 0
			if !latest {
				if number, _, err := read.store.Revision(r.Context(), read.key, 0); err == nil {
					latest = number == version.Metadata.Version
				}
			}
			held := a.addedFor(r.Context(), read.clone, read.key.Number, version, latest, added, fills)
			var err error
			response := tileview.Serve(version.Document, version.Composition, held, tiles.Options{})
			if response.Problem == "" {
				response.Drafts = drafts
			}
			if answer, err = tileview.Encode(response); err != nil {
				apiError(w, err)
				return
			}
			answers.Put(withAdded(revisionKey(read.clone, read.store, read.key, version.Metadata.Version, version.Revision)), answer)
		}
		etagjson.Serve(w, r, answer.Body)
	}
}
