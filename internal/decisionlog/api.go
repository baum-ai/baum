package decisionlog

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/livesessions"
	"github.com/baum-ai/baum/internal/locallink"
)

// Why capture does not run for a session (SessionDecisions.CaptureOff).
const (
	// CaptureOffPaused is the automatic master switch off (the tray's pause).
	CaptureOffPaused = "paused"
	// CaptureOffSwitch is the decisions switch off.
	CaptureOffSwitch = "off"
	// CaptureOffRepository is the session's repository turning it off.
	CaptureOffRepository = "repository_off"
	// CaptureOffNotPicked is a repository that is not picked, or none.
	CaptureOffNotPicked = "not_picked"
)

// LinkedSessions lists the sessions the pull request's latest saved review
// links on this machine (as its PR page's sessions list does), and when that
// review was generated, zero without one.
type LinkedSessions func(ctx context.Context, repository string, number int) ([]locallink.Session, time.Time, error)

// API serves GET /api/decisions: what the app shows of the captured
// decisions. Like the other local APIs it does not authenticate; mount it
// only behind the daemon's authenticated, Host/Origin-checked middleware.
type API struct {
	Store *Store
	// Registry, when set, says whether capture runs for a session's
	// repository, as the capture gate reads it.
	Registry Registry
	// Live lists the tracked sessions; nil is none.
	Live func() []livesessions.Session
	// Linked is nil to link none.
	Linked LinkedSessions
	// Log is nil for slog.Default.
	Log *slog.Logger
}

// agentOfVendor names a linked session's vendor as the hooks name its agent.
var agentOfVendor = map[string]string{"claude_code": livesessions.AgentClaudeCode, "codex": livesessions.AgentCodex, "cursor": livesessions.AgentCursor}

// PullRequestSession is a session of a pull request with its decisions.
type PullRequestSession struct {
	SessionDecisions
	// Title is the linked session's; a live one's is in the live list.
	Title string `json:"title,omitempty"`
	// Live is a session the tracker follows that is not over yet.
	Live bool `json:"live"`
}

// PullRequestDecisions is the answer for a pull request.
type PullRequestDecisions struct {
	Sessions []PullRequestSession `json:"sessions"`
	// ReviewGeneratedAt is when the saved review the provisional marks are
	// relative to was generated; null without one.
	ReviewGeneratedAt *time.Time `json:"review_generated_at"`
}

// Handler serves GET /api/decisions?agent=&session= (one session) and
// GET /api/decisions?repository=&pr= (the sessions of a pull request: those
// the tracker links to it, ended ones included, and those its saved review
// links). Decisions dated after that review was generated are provisional.
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/decisions", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		agent, session := q.Get("agent"), q.Get("session")
		repository := q.Get("repository")
		number, numberErr := strconv.Atoi(q.Get("pr"))
		var body any
		var err error
		switch {
		case agent != "" && session != "" && repository == "":
			body, err = a.session(r.Context(), SessionKey{agent, session})
		case repository != "" && numberErr == nil && number > 0 && agent == "" && session == "":
			body, err = a.pullRequest(r.Context(), repository, number)
		default:
			http.Error(w, "agent and session, or repository and a positive pr, required", http.StatusBadRequest)
			return
		}
		if err != nil {
			if r.Context().Err() == nil {
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(body)
	})
	return mux
}

func (a *API) live() []livesessions.Session {
	if a.Live == nil {
		return nil
	}
	return a.Live()
}

func (a *API) session(ctx context.Context, key SessionKey) (map[string]SessionDecisions, error) {
	views, err := a.Store.Sessions(ctx, key)
	if err != nil {
		return nil, err
	}
	view := views[0]
	// The gate reads the repository the session works in now: the tracker's,
	// else the one it was last captured in.
	repository := view.Repository
	for _, s := range a.live() {
		if s.Agent == key.Agent && s.ID == key.ID && s.Repository != "" {
			repository = s.Repository
		}
	}
	// An unreadable registry leaves the reason out rather than the answer.
	if view.CaptureOff, err = a.captureOff(ctx, repository); err != nil {
		log := a.Log
		if log == nil {
			log = slog.Default()
		}
		log.Warn("decisions: the capture gate was not read", "error", err)
		view.CaptureOff = ""
	}
	return map[string]SessionDecisions{"session": view}, nil
}

// captureOff reads the capture gate (Settings.DecisionsEnabled) for a
// repository: why capture does not run there, empty when it does or without
// a registry.
func (a *API) captureOff(ctx context.Context, repository string) (string, error) {
	if a.Registry == nil {
		return "", nil
	}
	prefs, err := a.Registry.Preferences(ctx)
	if err != nil {
		return "", err
	}
	switch {
	case !prefs.Automatic:
		return CaptureOffPaused, nil
	case prefs.Decisions.Enabled != nil && !*prefs.Decisions.Enabled:
		return CaptureOffSwitch, nil
	case repository == "":
		return CaptureOffNotPicked, nil
	}
	settings, picked, err := a.Registry.RepositorySettings(ctx, repository)
	switch {
	case err != nil:
		return "", err
	case !picked:
		return CaptureOffNotPicked, nil
	case !settings.DecisionsEnabled(prefs):
		return CaptureOffRepository, nil
	}
	return "", nil
}

func (a *API) pullRequest(ctx context.Context, repository string, number int) (PullRequestDecisions, error) {
	answer := PullRequestDecisions{Sessions: []PullRequestSession{}}
	var keys []SessionKey
	sessions := map[SessionKey]PullRequestSession{}
	add := func(key SessionKey, title string, live bool) {
		if _, ok := sessions[key]; !ok {
			keys = append(keys, key)
		}
		s := sessions[key]
		s.Title = cmp.Or(s.Title, title)
		s.Live = s.Live || live
		sessions[key] = s
	}
	// Ended sessions the tracker still holds stay, so a session that ended
	// after the review keeps its provisional decisions on the page.
	for _, s := range a.live() {
		if s.PR == number && strings.EqualFold(s.Repository, repository) {
			add(SessionKey{s.Agent, s.ID}, "", s.State != livesessions.StateEnded)
		}
	}
	var generated time.Time
	if a.Linked != nil {
		linked, at, err := a.Linked(ctx, repository, number)
		if err != nil {
			return answer, err
		}
		for _, l := range linked {
			if agent, known := agentOfVendor[l.Vendor]; known {
				add(SessionKey{agent, l.ID}, l.Title, false)
			}
		}
		generated = at
	}
	if !generated.IsZero() {
		answer.ReviewGeneratedAt = &generated
	}
	views, err := a.Store.Sessions(ctx, keys...)
	if err != nil {
		return answer, err
	}
	for i, view := range views {
		if !generated.IsZero() {
			for j := range view.Decisions {
				view.Decisions[j].Provisional = view.Decisions[j].DecidedAt.After(generated)
			}
		}
		s := sessions[keys[i]]
		s.SessionDecisions = view
		answer.Sessions = append(answer.Sessions, s)
	}
	return answer, nil
}
