package uiserver

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/viewserver"
)

// factsRoutes serves a pull request's facts, the page it shows while it has
// no saved review, beside the routes that serve a saved version:
//
//   - GET /api/facts?clone=&pr=&head= answers the facts (review.Facts) at the
//     head the page shows, built when due (internal/facts).
//   - GET /api/facts/sessions, /api/facts/sessions/{id}, /api/facts/patch,
//     /api/facts/commits, /api/facts/commit and /api/facts/commit-patch answer
//     what /api/sessions, /api/patch, /api/commits, /api/commit and
//     /api/commit-patch answer for a version, for the facts last built: they never
//     read GitHub, and are 404 before the facts are, or when an optional
//     head= names another head than the facts describe.
//
// Facts are never a version: /api/versions and /api/document do not see them.
func (a LocalAPI) factsRoutes(mux *http.ServeMux, cache *documentCache) {
	mux.HandleFunc("GET /api/facts", func(w http.ResponseWriter, r *http.Request) {
		if a.Facts == nil {
			factsError(w, http.StatusServiceUnavailable, "facts_unavailable", "facts are unavailable", time.Time{})
			return
		}
		c, key, n, ok := a.selector(w, r)
		if !ok {
			return
		}
		if n != 0 {
			http.Error(w, "facts have no version", http.StatusBadRequest)
			return
		}
		document, err := a.Facts.Facts(r.Context(), c, key.Number, r.URL.Query().Get("head"))
		if limited, ok := errors.AsType[*github.RateLimitedError](err); ok {
			factsError(w, http.StatusServiceUnavailable, "rate_limited", limited.Error(), limited.ResumeAt)
			return
		}
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			factsError(w, http.StatusBadGateway, "failed", err.Error(), time.Time{})
			return
		}
		if len(document) > viewserver.MaxDocumentBytes {
			http.Error(w, "facts too large", http.StatusRequestEntityTooLarge)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(document)
	})
	local := func(w http.ResponseWriter, r *http.Request) {
		if a.Facts == nil {
			http.NotFound(w, r)
			return
		}
		if !acquire(r.Context(), cache.requests) {
			return
		}
		defer func() { <-cache.requests }()
		c, key, _, ok := a.selector(w, r)
		if !ok {
			return
		}
		// A page names the head of the facts it shows, so it is never
		// answered for facts it was not shown.
		document, head := a.Facts.Cached(c, key.Number)
		if asked := r.URL.Query().Get("head"); document == nil || (asked != "" && asked != head) || len(document) > viewserver.MaxDocumentBytes {
			http.NotFound(w, r)
			return
		}
		viewer, release, err := cache.get(c, document)
		if err != nil {
			apiError(w, err)
			return
		}
		defer release()
		// The viewer serves the facts as it serves a document, at its own
		// paths.
		routed := r.Clone(r.Context())
		routed.URL.Path = "/api/" + strings.TrimPrefix(r.URL.Path, "/api/facts/")
		routed.URL.RawPath = ""
		viewer.Mux().ServeHTTP(w, routed)
	}
	mux.HandleFunc("GET /api/facts/sessions", local)
	mux.HandleFunc("GET /api/facts/sessions/{id}", local)
	mux.HandleFunc("GET /api/facts/patch", local)
	mux.HandleFunc("GET /api/facts/commits", local)
	mux.HandleFunc("GET /api/facts/commit", local)
	mux.HandleFunc("GET /api/facts/commit-patch", local)
}

func factsError(w http.ResponseWriter, status int, code, message string, resume time.Time) {
	body := map[string]any{"code": code, "message": message}
	if !resume.IsZero() {
		body["resume_at"] = resume.UTC()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	writeJSON(w, body)
}
