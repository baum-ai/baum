package uiserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
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
	// Live serves /api/live-sessions: the agent sessions in progress, and
	// /api/live-sessions/{agent}/{id}/transcript: one of their transcripts.
	Live http.Handler
	// AgentRequests serves /api/agent-requests and /api/agent-messages: the
	// permission requests and questions agents wait on the app to answer, and
	// the messages queued for running sessions (internal/agentrequests).
	AgentRequests http.Handler
	// Clones serves /api/clones/found: the local clones found without asking.
	Clones http.Handler
	// Merge serves /api/prs/merge-options and /api/prs/merge: merging a pull
	// request from the app.
	Merge http.Handler
	// Review serves /api/prs/review: submitting a pull request review with
	// inline comments from the app.
	Review http.Handler
	// Chat serves /api/prs/chat: asking the agent that wrote a review about
	// the pull request, and the conversations so far (ChatHandler).
	Chat http.Handler
	// Update serves /api/prs/update-branch and /api/prs/rerun-checks, with
	// their -options reads: updating a pull request's branch and re-running its
	// failed workflow runs. With merging, submitting a review and a
	// fix's push, they are the GitHub writes the app makes.
	Update http.Handler
	// Groups serves /api/groups: review groups and their combined reviews
	// (internal/reviewgroups).
	Groups http.Handler
	// Inboxes serves /api/inboxes: the saved filters listed under Inbox in
	// the app. Without it the route reports the capability unavailable.
	Inboxes http.Handler
	// GitHubRepositories serves the account's repositories for picking
	// (/api/github/repositories) and cloning one
	// (/api/repositories/{owner}/{name}/clone and clone-target).
	GitHubRepositories http.Handler
	// PlanUsage serves /api/plan-usage: what remains of the Claude and Codex
	// plans.
	PlanUsage http.Handler
	// Analytics serves /api/analytics: what the agents did, cost and
	// delivered (internal/analytics).
	Analytics http.Handler
	// CurrentName lets POST /api/repos register a folder whose remote uses
	// an earlier name of the repository (repos.Registry.HandlerWith).
	CurrentName repos.CurrentName
	Changed     func()
	// Timeline serves /api/timeline/: the features and decisions of the
	// local decision log (internal/decisionlog).
	Timeline http.Handler
	// Decisions builds the handler of /api/decisions, the decisions captured
	// from agent sessions (internal/decisionlog), given the sessions a pull
	// request's saved review links, as its PR page lists them.
	Decisions func(LinkedSessions) http.Handler
	// DecisionModel resolves the decision model capture uses with the given
	// preferences, for GET and PUT /api/settings/decisions; nil reports none.
	DecisionModel func(context.Context, repos.Preferences) DecisionModel
	// DecisionsChanged, when set, is called after a change to what decision
	// capture does: its settings or a repository's. The daemon publishes the
	// app's decisions event with it.
	DecisionsChanged func()
	// Log receives what the API logs; nil is slog.Default.
	Log *slog.Logger
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
	registry := a.Registry.HandlerWith(a.CurrentName)
	mutate := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		registry.ServeHTTP(w, r)
		if r.Method != "GET" && r.Method != "HEAD" {
			if a.Changed != nil {
				a.Changed()
			}
			if a.DecisionsChanged != nil {
				a.DecisionsChanged()
			}
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
	mux.Handle("POST /api/prs/review", provider(a.Review, "review_unavailable"))
	chat := provider(a.Chat, "chat_unavailable")
	mux.Handle("GET /api/prs/chat", chat)
	mux.Handle("POST /api/prs/chat", chat)
	update := provider(a.Update, "update_unavailable")
	mux.Handle("GET /api/prs/update-branch-options", update)
	mux.Handle("POST /api/prs/update-branch", update)
	mux.Handle("GET /api/prs/rerun-checks-options", update)
	mux.Handle("POST /api/prs/rerun-checks", update)
	mux.Handle("GET /api/prs/review-threads", update)
	mux.Handle("POST /api/prs/resolve-thread", update)
	mux.Handle("GET /api/prs/failing-checks", update)
	mux.Handle("POST /api/prs/rerun-job", update)
	mux.Handle("/api/prs", provider(a.PRs, "discovery_unavailable"))
	mux.Handle("/api/prs/", provider(a.PRs, "discovery_unavailable"))
	mux.Handle("/api/background", provider(a.Background, "background_unavailable"))
	mux.Handle("/api/automatic", provider(a.Automatic, "automatic_unavailable"))
	mux.Handle("/api/live-sessions", provider(a.Live, "live_sessions_unavailable"))
	mux.Handle("/api/live-sessions/", provider(a.Live, "live_sessions_unavailable"))
	agentRequests := provider(a.AgentRequests, "agent_requests_unavailable")
	mux.Handle("/api/agent-requests", agentRequests)
	mux.Handle("/api/agent-requests/", agentRequests)
	mux.Handle("/api/agent-messages", agentRequests)
	mux.Handle("/api/agent-messages/", agentRequests)
	mux.Handle("/api/groups", provider(a.Groups, "groups_unavailable"))
	mux.Handle("/api/groups/", provider(a.Groups, "groups_unavailable"))
	mux.Handle("/api/inboxes", provider(a.Inboxes, "inboxes_unavailable"))
	mux.Handle("/api/clones/found", provider(a.Clones, "clones_unavailable"))
	mux.Handle("/api/clones/found/", provider(a.Clones, "clones_unavailable"))
	repositories := provider(a.GitHubRepositories, "github_repositories_unavailable")
	mux.Handle("GET /api/github/repositories", repositories)
	mux.Handle("GET /api/repositories/{owner}/{name}/clone-target", repositories)
	mux.Handle("GET /api/repositories/{owner}/{name}/clone", repositories)
	mux.Handle("POST /api/repositories/{owner}/{name}/clone", repositories)
	mux.Handle("/api/plan-usage", provider(a.PlanUsage, "plan_usage_unavailable"))
	mux.Handle("/api/analytics", provider(a.Analytics, "analytics_unavailable"))
	mux.Handle("/api/timeline/", provider(a.Timeline, "timeline_unavailable"))
	var decisions http.Handler
	if a.Decisions != nil {
		decisions = a.Decisions(a.linkedSessions(cache))
	}
	mux.Handle("/api/decisions", provider(decisions, "decisions_unavailable"))
	mux.Handle("/api/github-auth", provider(a.GitHubAuth, "github_auth_unavailable"))
	mux.Handle("/api/github-auth/", provider(a.GitHubAuth, "github_auth_unavailable"))
	mux.Handle("/api/index", provider(a.Index, "index_unavailable"))
	mux.Handle("/api/jobs", provider(a.Jobs, "runner_unavailable"))
	mux.Handle("/api/jobs/", provider(a.Jobs, "runner_unavailable"))
	mux.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) {
		// The app's backend and model, chosen during setup, override the file.
		cfg := a.Config
		source := ""
		if len(cfg.AgentEnv) > 0 {
			source = "file"
		}
		fixBackend, fixModel := "", ""
		if prefs, err := a.Registry.Preferences(r.Context()); err == nil {
			cfg = prefs.Resolve(cfg)
			if prefs.FixBackend != nil {
				fixBackend = string(*prefs.FixBackend)
			}
			if prefs.FixModel != nil {
				fixModel = *prefs.FixModel
			}
			if len(prefs.AgentEnv) > 0 {
				source = "app"
			}
		}
		writeJSON(w, struct {
			Backend        config.Backend    `json:"backend"`
			Model          string            `json:"model"`
			Budgets        config.Budgets    `json:"budgets"`
			Caps           map[string]int    `json:"caps"`
			AgentEnv       map[string]string `json:"agent_env"`
			AgentEnvSource string            `json:"agent_env_source"`
			FixBackend     string            `json:"fix_backend"`
			FixModel       string            `json:"fix_model"`
		}{cfg.Backend, cfg.Model, cfg.Budgets, cfg.Caps, cfg.AgentEnv, source, fixBackend, fixModel})
	})
	mux.HandleFunc("GET /api/settings/appearance", a.getAppearance)
	mux.HandleFunc("PUT /api/settings/appearance", a.putAppearance)
	mux.HandleFunc("GET /api/settings/decisions", a.getDecisionSettings)
	mux.HandleFunc("PUT /api/settings/decisions", a.putDecisionSettings)
	// The app's agent environment replaces the config file's; an empty one
	// goes back to the file's.
	mux.HandleFunc("PUT /api/settings/agent-env", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			AgentEnv map[string]string `json:"agent_env"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid JSON request", http.StatusBadRequest)
			return
		}
		if err := config.ValidateAgentEnv(input.AgentEnv); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(input.AgentEnv) == 0 {
			input.AgentEnv = nil
		}
		prefs, err := a.Registry.UpdatePreferences(r.Context(), func(p repos.Preferences) repos.Preferences {
			p.AgentEnv = input.AgentEnv
			return p
		})
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]map[string]string{"agent_env": prefs.Resolve(a.Config).AgentEnv})
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
