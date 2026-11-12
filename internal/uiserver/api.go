package uiserver

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/baum-ai/baum/internal/agentplugins"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/doctor"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/viewserver"
	"github.com/baum-ai/baum/sessions"
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
	// Console serves /api/console/: the session panel's conversations and
	// their proposals (internal/console). Without it the routes report the
	// capability unavailable.
	Console http.Handler
	// Inbox serves /api/inbox: the queue the daemon builds, with which saved
	// inboxes hold each pull request (internal/inboxqueue).
	Inbox http.Handler
	// UIState keeps inbox bookkeeping and review drafts across browser origins.
	UIState http.Handler
	// GitHubRepositories serves the account's repositories for picking
	// (/api/github/repositories) and cloning one
	// (/api/repositories/{owner}/{name}/clone and clone-target).
	GitHubRepositories http.Handler
	// PlanUsage serves /api/plan-usage: what remains of the Claude and Codex
	// plans.
	PlanUsage http.Handler
	// PlanUsageCached returns the plans PlanUsage last read, without reading
	// again: what CachedPlans filters for the MCP server, which must never
	// cause a fetch.
	PlanUsageCached func() []json.RawMessage
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
	// Sessions are the session stores a pull request page's linked sessions
	// are read from; the zero value reads this computer's default stores.
	Sessions sessions.Options
	// Facts builds what GET /api/facts answers: a pull request's facts for
	// its page while it has no saved review (internal/facts). Without it the
	// route reports the capability unavailable.
	Facts FactsSource
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
	// ConcurrencyChanged, when set, applies a new job concurrency
	// (repos.Preferences.JobConcurrency) to the running queue.
	ConcurrencyChanged func(int)
	// Log receives what the API logs; nil is slog.Default.
	Log *slog.Logger
	// Diagnostics builds the report served at /api/diagnostics; nil serves the
	// capability fields with a null report. It must honour its context.
	Diagnostics func(context.Context) doctor.Report
	// HookTrust checks Codex session reporting without running a conversation.
	HookTrust func(context.Context) agentplugins.HookTrust
	// DiagnosticsTimeout bounds one shared run; zero means
	// defaultDiagnosticsTimeout.
	DiagnosticsTimeout time.Duration
	// Version is this baum's version, named in the "Report a problem" link
	// that /api/diagnostics returns.
	Version string

	// diagnosticsWaiting is a test hook, called once a request is waiting on
	// the shared run.
	diagnosticsWaiting func()
}

// FactsSource builds and keeps pull requests' facts (review.Facts);
// *facts.Service implements it.
type FactsSource interface {
	// Facts answers the pull request's facts at head, the head the page
	// shows, building them when they are due.
	Facts(ctx context.Context, clone repos.Clone, number int, head string) ([]byte, error)
	// Cached is the facts last built for the pull request and the head they
	// describe, nil when none; it never reads GitHub.
	Cached(clone repos.Clone, number int) ([]byte, string)
}

const defaultDiagnosticsTimeout = 45 * time.Second

var errDiagnosticsTimeout = errors.New("diagnostics timed out")

// LocalHandler owns bounded per-document viewer resources.
type LocalHandler struct {
	http.Handler
	documents *documentCache
}

func (h *LocalHandler) InvalidateSessions() { h.documents.invalidateSessions() }

func (h *LocalHandler) Close() { h.documents.close() }
func (a LocalAPI) Handler() *LocalHandler {
	cache := newDocumentCache()
	cache.sessions = a.Sessions
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
	mux.Handle("GET /api/prs/checks", update)
	mux.Handle("GET /api/prs/check-log", update)
	mux.Handle("GET /api/prs/description", update)
	mux.Handle("GET /api/prs/conflicts", update)
	mux.Handle("POST /api/prs/rerun-job", update)
	mux.Handle("/api/prs", provider(a.PRs, "discovery_unavailable"))
	mux.Handle("/api/prs/", provider(a.PRs, "discovery_unavailable"))
	mux.Handle("/api/background", provider(a.Background, "background_unavailable"))
	mux.Handle("GET /api/debug/stats", debugStats())
	mux.Handle("GET /api/debug/pprof/", debugProfiles())
	mux.Handle("/api/automatic", provider(a.Automatic, "automatic_unavailable"))
	mux.Handle("POST /api/automatic/catch-up", provider(a.Automatic, "automatic_unavailable"))
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
	mux.Handle("/api/console/", provider(a.Console, "console_unavailable"))
	mux.Handle("/api/inbox", provider(a.Inbox, "inbox_unavailable"))
	mux.Handle("/api/ui-state/", provider(a.UIState, "ui_state_unavailable"))
	mux.Handle("/api/clones/found", provider(a.Clones, "clones_unavailable"))
	mux.Handle("/api/clones/found/", provider(a.Clones, "clones_unavailable"))
	repositories := provider(a.GitHubRepositories, "github_repositories_unavailable")
	mux.Handle("GET /api/github/repositories", repositories)
	mux.Handle("GET /api/repositories/{owner}/{name}/clone-target", repositories)
	mux.Handle("GET /api/repositories/{owner}/{name}/clone", repositories)
	mux.Handle("POST /api/repositories/{owner}/{name}/clone", repositories)
	if a.PlanUsage != nil {
		mux.HandleFunc("/api/plan-usage", a.planUsageHandler)
	} else {
		mux.Handle("/api/plan-usage", provider(nil, "plan_usage_unavailable"))
	}
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
		fixBackend, fixModel, fixEffort := "", "", ""
		concurrency := repos.MaxJobConcurrency
		prefs, prefsErr := a.Registry.Preferences(r.Context())
		if prefsErr == nil {
			concurrency = prefs.Concurrency()
			cfg = prefs.Resolve(cfg)
			if prefs.FixBackend != nil {
				fixBackend = string(*prefs.FixBackend)
			}
			if prefs.FixModel != nil {
				fixModel = *prefs.FixModel
			}
			if prefs.FixEffort != nil {
				fixEffort = *prefs.FixEffort
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
			Effort         string            `json:"effort"`
			FixEffort      string            `json:"fix_effort"`
			JobConcurrency int               `json:"job_concurrency"`
			// The time limits in force, in whole minutes.
			ReviewTimeLimit int `json:"review_time_limit_minutes"`
			RunTimeLimit    int `json:"run_time_limit_minutes"`
			// The app's saved choices could not be read, so the answer is
			// the config file's alone: not what a run started now would use
			// once they can be read again.
			PreferencesUnavailable bool `json:"preferences_unavailable,omitempty"`
		}{cfg.Backend, cfg.Model, cfg.Budgets, cfg.Caps, cfg.AgentEnv, source, fixBackend, fixModel, cfg.Effort, fixEffort, concurrency,
			minutes(cfg.Budgets.ReviewTimeout), minutes(cfg.Budgets.RunTimeout), prefsErr != nil})
	})
	// The time limits apply to runs that start after the change.
	mux.HandleFunc("PUT /api/settings/time-limits", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			ReviewTimeLimit int `json:"review_time_limit_minutes"`
			RunTimeLimit    int `json:"run_time_limit_minutes"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid JSON request", http.StatusBadRequest)
			return
		}
		if err := checkTimeLimits(input.ReviewTimeLimit, input.RunTimeLimit); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		prefs, err := a.updatePreferences(r.Context(), nil, func(p repos.Preferences) repos.Preferences {
			return setTimeLimits(p, input.ReviewTimeLimit, input.RunTimeLimit)
		})
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		budgets := prefs.Resolve(a.Config).Budgets
		writeJSON(w, map[string]int{"review_time_limit_minutes": minutes(budgets.ReviewTimeout), "run_time_limit_minutes": minutes(budgets.RunTimeout)})
	})
	mux.HandleFunc("GET /api/settings/panel", a.getPanel)
	mux.HandleFunc("PUT /api/settings/panel", a.putPanel)
	mux.HandleFunc("GET /api/settings/appearance", a.getAppearance)
	mux.HandleFunc("PUT /api/settings/appearance", a.putAppearance)
	mux.HandleFunc("GET /api/settings/plan-usage", a.getPlanUsageSettings)
	mux.HandleFunc("PUT /api/settings/plan-usage", a.putPlanUsageSettings)
	mux.HandleFunc("GET /api/settings/decisions", a.getDecisionSettings)
	mux.HandleFunc("PUT /api/settings/decisions", a.putDecisionSettings)
	// Custom prompts; a run resolves them when it starts, so no change
	// notification is needed.
	mux.HandleFunc("GET /api/settings/prompts", func(w http.ResponseWriter, r *http.Request) {
		a.Registry.ServePrompts(w, r, repos.GlobalPrompts)
	})
	mux.HandleFunc("PUT /api/settings/prompts/{id}", func(w http.ResponseWriter, r *http.Request) {
		a.Registry.ServePromptPut(w, r, repos.GlobalPrompts, r.PathValue("id"))
	})
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
		prefs, err := a.updatePreferences(r.Context(), nil, func(p repos.Preferences) repos.Preferences {
			return setAgentEnv(p, input.AgentEnv)
		})
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]map[string]string{"agent_env": prefs.Resolve(a.Config).AgentEnv})
	})
	// How many jobs run at once applies to the running queue at once
	// (saveJobConcurrency).
	mux.HandleFunc("PUT /api/settings/job-concurrency", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			JobConcurrency int `json:"job_concurrency"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid JSON request", http.StatusBadRequest)
			return
		}
		if err := checkJobConcurrency(input.JobConcurrency); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		prefs, err := a.saveJobConcurrency(r.Context(), input.JobConcurrency, nil)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]int{"job_concurrency": prefs.Concurrency()})
	})
	// Each fresh read starts Codex processes, so answers are cached for a few
	// minutes; ?refresh=1 reads again.
	hookTrust := newHookTrustCache(a.HookTrust)
	mux.HandleFunc("GET /api/agent-hooks/codex", func(w http.ResponseWriter, r *http.Request) {
		if a.HookTrust == nil {
			writeJSON(w, map[string]bool{"available": false})
			return
		}
		writeJSON(w, hookTrust.Get(r.URL.Query().Get("refresh") == "1"))
	})
	var diagnostics singleflight.Group
	diagnosticsReports := newDiagnosticsCache()
	mux.HandleFunc("GET /api/diagnostics", func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{"local": true, "generation_runner": a.Jobs != nil, "discovery": a.PRs != nil, "index": a.Index != nil, "report": nil}
		// The report's paths are this user's; the app shows the home directory as ~.
		if home, err := os.UserHomeDir(); err == nil {
			body["home"] = home
		}
		if a.Diagnostics != nil {
			timeout := a.DiagnosticsTimeout
			if timeout <= 0 {
				timeout = defaultDiagnosticsTimeout
			}
			// Concurrent requests share one run, which a single client
			// disconnecting does not cancel.
			result := diagnosticsReports.doChan(&diagnostics, r.URL.Query().Get("refresh") != "", func() (any, error) {
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
				if report, ok := res.Val.(doctor.Report); ok {
					home, _ := os.UserHomeDir()
					info := doctor.IssueInfo{
						Version: cmp.Or(a.Version, "unknown"),
						OS:      runtime.GOOS + "/" + runtime.GOARCH,
						Home:    home,
					}
					// The tray and window menu open the link; the app's
					// Report a problem dialog builds its own from the parts.
					body["report_issue_url"] = doctor.IssueURL(report, info)
					body["report_issue"] = report.PublicIssueParts(info)
				}
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
		// The store revision names the document without reading it, so a
		// kept viewer answers without the document being read or hashed.
		number, revision, err := s.Revision(r.Context(), key, n)
		if err != nil {
			apiError(w, err)
			return
		}
		viewer, release, found, err := cache.cached(r.Context(), revisionKey(c, s, key, number, revision))
		if !found && err == nil {
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
			viewer, release, err = cache.get(r.Context(), c, version.Document, revisionKey(c, s, key, version.Metadata.Version, version.Revision))
		}
		if err != nil {
			if r.Context().Err() == nil {
				apiError(w, err)
			}
			return
		}
		defer release()
		// The version asked for is the document the viewer serves, so the
		// viewer answers it as its own.
		routed := r.Clone(r.Context())
		query := routed.URL.Query()
		query.Del("version")
		routed.URL.RawQuery = query.Encode()
		viewer.Mux().ServeHTTP(w, routed)
	}
	mux.HandleFunc("GET /api/document", document)
	mux.HandleFunc("GET /api/sessions", document)
	mux.HandleFunc("GET /api/sessions/{id}", document)
	mux.HandleFunc("GET /api/patch", document)
	mux.HandleFunc("GET /api/commits", document)
	mux.HandleFunc("GET /api/commit", document)
	mux.HandleFunc("GET /api/commit-patch", document)
	mux.HandleFunc("GET /api/file", document)
	a.factsRoutes(mux, cache)
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

// minutes is a time limit in whole minutes, rounded up.
func minutes(d config.Duration) int {
	return int((time.Duration(d) + time.Minute - 1) / time.Minute)
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
