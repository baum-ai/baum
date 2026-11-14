package uiserver

import (
	"context"
	"net/http"

	"github.com/baum-ai/baum/internal/etagjson"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
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

// tilesHandler serves GET /api/tiles: the review document and the tile
// document converted from it and composed with the version's stored
// composition, if any (the answers are kept under the version's store
// revision, which changes with every write to its row, composition included), with the selectors, errors and no-store ETag of
// GET /api/document. Its document is the normalised review
// (review.NormalizeStored), which differs from the stored one on the plain
// route only where a stored-document rule applies.
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
		body, found := answers.Get(read.revisionKey())
		if !found {
			version, ok := read.version(r.Context(), w)
			if !ok {
				return
			}
			var err error
			if body, err = tileview.Encode(tileview.Build(version.Document, version.Composition, tiles.Options{})); err != nil {
				apiError(w, err)
				return
			}
			answers.Put(revisionKey(read.clone, read.store, read.key, version.Metadata.Version, version.Revision), body)
		}
		etagjson.Serve(w, r, body)
	}
}
