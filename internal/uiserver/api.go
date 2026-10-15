package uiserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/viewserver"
)

// LocalAPI composes the existing local services. It never selects a filesystem
// path supplied by HTTP: document selectors must name a registered clone ID.
// Providers for later capabilities may be installed without changing the
// authentication or instance boundary.
type LocalAPI struct {
	Registry         *repos.Registry
	Config           config.Config
	PRs, Index, Jobs http.Handler
	Changed          func()
}

func (a LocalAPI) Handler() http.Handler {
	mux := http.NewServeMux()
	registry := a.Registry.Handler()
	mutate := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		registry.ServeHTTP(w, r)
		if r.Method != "GET" && r.Method != "HEAD" && a.Changed != nil {
			a.Changed()
		}
	})
	mux.Handle("/api/repos", mutate)
	mux.Handle("/api/repos/", mutate)
	provider := func(h http.Handler, code string) http.Handler {
		if h != nil {
			return h
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" {
				http.Error(w, "capability unavailable", 503)
				return
			}
			writeJSON(w, map[string]any{"available": false, "code": code})
		})
	}
	mux.Handle("/api/prs", provider(a.PRs, "discovery_unavailable"))
	mux.Handle("/api/prs/", provider(a.PRs, "discovery_unavailable"))
	mux.Handle("/api/index", provider(a.Index, "index_unavailable"))
	mux.Handle("/api/jobs", provider(a.Jobs, "runner_unavailable"))
	mux.Handle("/api/jobs/", provider(a.Jobs, "runner_unavailable"))
	mux.HandleFunc("GET /api/settings", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, struct {
			Backend config.Backend `json:"backend"`
			Model   string         `json:"model"`
			Budgets config.Budgets `json:"budgets"`
			Caps    map[string]int `json:"caps"`
		}{a.Config.Backend, a.Config.Model, a.Config.Budgets, a.Config.Caps})
	})
	mux.HandleFunc("GET /api/diagnostics", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"local": true, "generation_runner": a.Jobs != nil, "discovery": a.PRs != nil, "index": a.Index != nil})
	})
	mux.HandleFunc("GET /api/versions", func(w http.ResponseWriter, r *http.Request) {
		c, key, _, ok := a.selector(w, r)
		if !ok {
			return
		}
		s, err := store.Open(c.CommonDir, "", false)
		if err != nil {
			apiError(w, err)
			return
		}
		versions, err := s.Versions(r.Context(), key)
		if err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, versions)
	})
	document := func(w http.ResponseWriter, r *http.Request) {
		c, key, n, ok := a.selector(w, r)
		if !ok {
			return
		}
		s, err := store.Open(c.CommonDir, "", false)
		if err != nil {
			apiError(w, err)
			return
		}
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
		if len(version.Document)>viewserver.MaxDocumentBytes {http.Error(w,"stored document too large",http.StatusRequestEntityTooLarge);return}
 if r.URL.Path == "/api/document" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(version.Document)
			return
		}
		patcher := new(locallink.Patcher)
		viewer, err := viewserver.New(viewserver.Options{Document: version.Document, Sessions: func(ctx context.Context, v viewserver.Review) (locallink.Result, error) {
			return locallink.Resolve(ctx, locallink.Request{Dir: c.Path, Repository: v.Repository, Head: v.Head, Base: v.Base, MergeBase: v.MergeBase})
		}, Patch: func(ctx context.Context, v viewserver.Review, path string) (locallink.PatchResult, error) {
			return patcher.Patch(ctx, locallink.PatchRequest{Dir: c.Path, Repository: v.Repository, Head: v.Head, Base: v.Base, MergeBase: v.MergeBase, Path: path})
		}})
		if err != nil {
			apiError(w, err)
			return
		}
		defer viewer.Close()
		viewer.Mux().ServeHTTP(w, r)
	}
	mux.HandleFunc("GET /api/document", document)
	mux.HandleFunc("GET /api/sessions", document)
	mux.HandleFunc("GET /api/sessions/{id}", document)
	mux.HandleFunc("GET /api/patch", document)
	mux.HandleFunc("GET /api/commits", func(w http.ResponseWriter, r *http.Request) {
		c, key, n, ok := a.selector(w, r)
		if !ok {
			return
		}
		s, err := store.Open(c.CommonDir, "", false)
		if err != nil {
			apiError(w, err)
			return
		}
		var v store.Version
		if n == 0 {
			v, err = s.Latest(r.Context(), key)
		} else {
			v, err = s.Get(r.Context(), key, n)
		}
		if err != nil {
			apiError(w, err)
			return
		}
		var doc struct {
			Review struct {
				Repository, Head, Base string
				MergeBase              string `json:"merge_base"`
			}
		}
		if json.Unmarshal(v.Document, &doc) != nil {
			http.Error(w, "invalid stored document", 500)
			return
		}
		result, err := locallink.Resolve(r.Context(), locallink.Request{Dir: c.Path, Repository: doc.Review.Repository, Head: doc.Review.Head, Base: doc.Review.Base, MergeBase: doc.Review.MergeBase})
		if err != nil {
			apiError(w, err)
			return
		}
		if !result.Available {
			writeJSON(w, result)
			return
		}
		writeJSON(w, result.Commits)
	})
	mux.HandleFunc("GET /api/attachments/{id}", func(w http.ResponseWriter, r *http.Request) {
		c, key, n, ok := a.selector(w, r)
		if !ok {
			return
		}
		s, err := store.Open(c.CommonDir, "", false)
		if err != nil {
			apiError(w, err)
			return
		}
		image, err := s.Attachment(r.Context(), key, n, r.PathValue("id"))
		if err != nil {
			apiError(w, err)
			return
		}
		w.Header().Set("Content-Type", image.ContentType)
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		_, _ = w.Write(image.Bytes)
	})

	return mux
}
func (a LocalAPI) selector(w http.ResponseWriter, r *http.Request) (repos.Clone, store.Key, int, bool) {
	q := r.URL.Query()
	id := q.Get("clone")
	number, err := strconv.Atoi(q.Get("pr"))
	n := 0
	var nerr error
	if raw := q.Get("version"); raw != "" {
		n, nerr = strconv.Atoi(raw)
	}
	if id == "" || err != nil || number <= 0 || nerr != nil || n < 0 {
		http.Error(w, "clone and positive pr required; version must be nonnegative", 400)
		return repos.Clone{}, store.Key{}, 0, false
	}
	clones, err := a.Registry.List(r.Context())
	if err != nil {
		apiError(w, err)
		return repos.Clone{}, store.Key{}, 0, false
	}
	for _, c := range clones {
		if c.ID == id {
			return c, store.Key{Repository: c.Repository, Number: number}, n, true
		}
	}
	http.NotFound(w, r)
	return repos.Clone{}, store.Key{}, 0, false
}
func apiError(w http.ResponseWriter, err error) {
	status := 500
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, repos.ErrNotFound) {
		status = 404
	}
	http.Error(w, http.StatusText(status), status)
}

// RegisteredRepository resolves a registered clone ID, without allowing a
// request to use arbitrary repository names or local paths.
func RegisteredRepository(ctx context.Context, registry *repos.Registry, id string) (string, error) {
	clones, err := registry.List(ctx)
	if err != nil {
		return "", err
	}
	for _, c := range clones {
		if c.ID == id {
			return strings.ToLower(c.Repository), nil
		}
	}
	return "", repos.ErrNotFound
}
