package uiserver

import (
	"net/http"
	"sync"

	"github.com/baum-ai/baum/internal/etagjson"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/internal/viewserver"
	"github.com/baum-ai/baum/tiles"
)

const (
	// maxTileAnswers and maxTileBytes bound the tile answers kept, by count
	// and by size; the least recently used goes first.
	maxTileAnswers = 16
	maxTileBytes   = 64 << 20
)

// tileAnswer is one encoded GET /api/tiles body, kept under a store revision.
type tileAnswer struct {
	key  string
	body etagjson.Body
}

// tileCache keeps encoded answers by store revision (revisionKey), so a
// version asked for again is answered without being read or converted.
type tileCache struct {
	mu      sync.Mutex
	answers []tileAnswer // least recently used first
	bytes   int
}

func (c *tileCache) get(key string) (etagjson.Body, bool) {
	if key == "" {
		return etagjson.Body{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, a := range c.answers {
		if a.key == key {
			c.answers = append(append(c.answers[:i:i], c.answers[i+1:]...), a)
			return a.body, true
		}
	}
	return etagjson.Body{}, false
}

func (c *tileCache) put(key string, body etagjson.Body) {
	if key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, old := range c.answers {
		if old.key == key {
			c.bytes -= len(old.body.Data)
			c.answers = append(c.answers[:i:i], c.answers[i+1:]...)
			break
		}
	}
	c.answers = append(c.answers, tileAnswer{key, body})
	c.bytes += len(body.Data)
	for len(c.answers) > 1 && (len(c.answers) > maxTileAnswers || c.bytes > maxTileBytes) {
		c.bytes -= len(c.answers[0].body.Data)
		c.answers = c.answers[1:]
	}
}

// tilesHandler serves GET /api/tiles: the document of GET /api/document and
// the tile document converted from it, with the same selectors, errors and
// no-store ETag.
func (a LocalAPI) tilesHandler(cache *documentCache, answers *tileCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !acquire(r.Context(), cache.requests) {
			return
		}
		defer func() { <-cache.requests }()
		c, key, n, ok := a.selector(w, r)
		if !ok {
			return
		}
		s, err := store.Open(c.CommonDir, "", false)
		if err != nil {
			apiError(w, err)
			return
		}
		number, revision, err := s.Revision(r.Context(), key, n)
		if err != nil {
			apiError(w, err)
			return
		}
		body, found := answers.get(revisionKey(c, s, key, number, revision))
		if !found {
			var version store.Version
			if n == 0 {
				version, err = s.Latest(r.Context(), key)
			} else {
				version, err = s.Get(r.Context(), key, n)
			}
			if err != nil {
				apiError(w, err)
				return
			}
			if len(version.Document) > viewserver.MaxDocumentBytes {
				http.Error(w, "stored document too large", http.StatusRequestEntityTooLarge)
				return
			}
			if body, err = tileview.Encode(tileview.Build(version.Document, tiles.Options{})); err != nil {
				apiError(w, err)
				return
			}
			answers.put(revisionKey(c, s, key, version.Metadata.Version, version.Revision), body)
		}
		etagjson.Serve(w, r, body)
	}
}
