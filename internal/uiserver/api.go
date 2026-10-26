package uiserver

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/doctor"
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
	Background       http.Handler
	GitHubAuth       http.Handler
	// Automatic serves /api/automatic: the automatic generation preferences
	// and per-repository status.
	Automatic http.Handler
	// Live serves /api/live-sessions: the agent sessions in progress.
	Live http.Handler
	// Clones serves /api/clones/found: the local clones found without asking.
	Clones http.Handler
	// Merge serves /api/prs/merge-options and /api/prs/merge: merging a pull
	// request from the app.
	Merge http.Handler
	// Update serves /api/prs/update-branch and /api/prs/rerun-checks, with
	// their -options reads: updating a pull request's branch and re-running its
	// failed workflow runs. With merging and a fix's push, they are the
	// GitHub writes the app makes.
	Update http.Handler
	// Groups serves /api/groups: review groups and their combined reviews
	// (internal/reviewgroups).
	Groups http.Handler
	// GitHubRepositories serves the account's repositories for picking
	// (/api/github/repositories) and cloning one
	// (/api/repositories/{owner}/{name}/clone and clone-target).
	GitHubRepositories http.Handler
	// PlanUsage serves /api/plan-usage: what remains of the Claude and Codex
	// plans.
	PlanUsage http.Handler
	Changed   func()
	// Diagnostics builds the report served at /api/diagnostics; nil serves the
	// capability fields with a null report. It must honour its context.
	Diagnostics func(context.Context) doctor.Report
	// DiagnosticsTimeout bounds one shared run; zero means
	// defaultDiagnosticsTimeout.
	DiagnosticsTimeout time.Duration

	// diagnosticsWaiting is a test hook, called once a request is waiting on
	// the shared run.
	diagnosticsWaiting func()
}

const defaultDiagnosticsTimeout = 45 * time.Second

var errDiagnosticsTimeout = errors.New("diagnostics timed out")

// LocalHandler owns bounded per-document viewer resources.
type LocalHandler struct {
	http.Handler
	documents *documentCache
}

func (h *LocalHandler) Close() { h.documents.close() }
func (a LocalAPI) Handler() *LocalHandler {
	cache := newDocumentCache()
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
	mux.Handle("/api/repositories", mutate)
	mux.Handle("/api/repositories/", mutate)
	provider := func(h http.Handler, code string) http.Handler {
		if h != nil {
			return h
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" {
				http.Error(w, "capability unavailable", http.StatusServiceUnavailable)
				return
			}
			writeJSON(w, map[string]any{"available": false, "code": code})
		})
	}
	merge := provider(a.Merge, "merge_unavailable")
	mux.Handle("GET /api/prs/merge-options", merge)
	mux.Handle("POST /api/prs/merge", merge)
	update := provider(a.Update, "update_unavailable")
	mux.Handle("GET /api/prs/update-branch-options", update)
	mux.Handle("POST /api/prs/update-branch", update)
	mux.Handle("GET /api/prs/rerun-checks-options", update)
	mux.Handle("POST /api/prs/rerun-checks", update)
	mux.Handle("/api/prs", provider(a.PRs, "discovery_unavailable"))
	mux.Handle("/api/prs/", provider(a.PRs, "discovery_unavailable"))
	mux.Handle("/api/background", provider(a.Background, "background_unavailable"))
	mux.Handle("/api/automatic", provider(a.Automatic, "automatic_unavailable"))
	mux.Handle("/api/live-sessions", provider(a.Live, "live_sessions_unavailable"))
	mux.Handle("/api/groups", provider(a.Groups, "groups_unavailable"))
	mux.Handle("/api/groups/", provider(a.Groups, "groups_unavailable"))
	mux.Handle("/api/clones/found", provider(a.Clones, "clones_unavailable"))
	mux.Handle("/api/clones/found/", provider(a.Clones, "clones_unavailable"))
	repositories := provider(a.GitHubRepositories, "github_repositories_unavailable")
	mux.Handle("GET /api/github/repositories", repositories)
	mux.Handle("GET /api/repositories/{owner}/{name}/clone-target", repositories)
	mux.Handle("GET /api/repositories/{owner}/{name}/clone", repositories)
	mux.Handle("POST /api/repositories/{owner}/{name}/clone", repositories)
	mux.Handle("/api/plan-usage", provider(a.PlanUsage, "plan_usage_unavailable"))
	mux.Handle("/api/github-auth", provider(a.GitHubAuth, "github_auth_unavailable"))
	mux.Handle("/api/github-auth/", provider(a.GitHubAuth, "github_auth_unavailable"))
	mux.Handle("/api/index", provider(a.Index, "index_unavailable"))
	mux.Handle("/api/jobs", provider(a.Jobs, "runner_unavailable"))
	mux.Handle("/api/jobs/", provider(a.Jobs, "runner_unavailable"))
	mux.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) {
		// The app's backend and model, chosen during setup, override the file.
		cfg := a.Config
		if prefs, err := a.Registry.Preferences(r.Context()); err == nil {
			cfg = prefs.Resolve(cfg)
		}
		writeJSON(w, struct {
			Backend config.Backend `json:"backend"`
			Model   string         `json:"model"`
			Budgets config.Budgets `json:"budgets"`
			Caps    map[string]int `json:"caps"`
		}{cfg.Backend, cfg.Model, cfg.Budgets, cfg.Caps})
	})
	// Runs are shared only while one is in flight; a later request runs the
	// checks again. That is deliberate: the endpoint is local, authenticated
	// and user-triggered, so there is no cache.
	var diagnostics singleflight.Group
	mux.HandleFunc("GET /api/diagnostics", func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{"local": true, "generation_runner": a.Jobs != nil, "discovery": a.PRs != nil, "index": a.Index != nil, "report": nil}
		if a.Diagnostics != nil {
			timeout := a.DiagnosticsTimeout
			if timeout <= 0 {
				timeout = defaultDiagnosticsTimeout
			}
			// Concurrent requests share one run, which a single client
			// disconnecting does not cancel.
			result := diagnostics.DoChan("report", func() (any, error) {
				ctx, cancel := context.WithTimeout(context.Background(), timeout)
				defer cancel()
				done := make(chan doctor.Report, 1)
				go func() { done <- a.Diagnostics(ctx) }()
				select {
				case report := <-done:
					return report, nil
				case <-ctx.Done():
					return nil, errDiagnosticsTimeout
				}
			})
			if a.diagnosticsWaiting != nil {
				a.diagnosticsWaiting()
			}
			select {
			case <-r.Context().Done():
				return
			case res := <-result:
				if res.Err != nil {
					http.Error(w, res.Err.Error(), http.StatusGatewayTimeout)
					return
				}
				body["report"] = res.Val
			}
		}
		writeJSON(w, body)
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
		if r.URL.Path == "/api/document" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(version.Document)
			return
		}
		viewer, release, err := cache.get(c, version.Document)
		if err != nil {
			apiError(w, err)
			return
		}
		defer release()
		viewer.Mux().ServeHTTP(w, r)
	}
	mux.HandleFunc("GET /api/document", document)
	mux.HandleFunc("GET /api/sessions", document)
	mux.HandleFunc("GET /api/sessions/{id}", document)
	mux.HandleFunc("GET /api/patch", document)
	mux.HandleFunc("GET /api/commits", document)
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

	return &LocalHandler{Handler: mux, documents: cache}
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
		http.Error(w, "clone and positive pr required; version must be nonnegative", http.StatusBadRequest)
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

// pickedRepository resolves a picked repository by owner/name, with or without
// a clone.
func pickedRepository(ctx context.Context, registry *repos.Registry, name string) (string, error) {
	list, err := registry.Repositories(ctx)
	if err != nil {
		return "", err
	}
	for _, r := range list {
		if strings.EqualFold(r.Repository, name) {
			return strings.ToLower(r.Repository), nil
		}
	}
	return "", repos.ErrNotFound
}
