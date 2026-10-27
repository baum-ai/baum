package decisionlog

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/livesessions"
)

// API serves GET /api/decisions: what the app shows of the captured
// decisions. Like the other local APIs it does not authenticate; mount it
// only behind the daemon's authenticated, Host/Origin-checked middleware.
type API struct {
	Store *Store
	// Live lists the tracked sessions; nil is none.
	Live func() []livesessions.Session
	// Linked lists the sessions the pull request's latest saved review links
	// on this machine, and when that review was generated (zero without
	// one); nil links none.
	Linked func(ctx context.Context, repository string, number int) ([]Linked, time.Time, error)
}

// Linked is a session a saved review links, with its title.
type Linked struct {
	SessionKey
	Title string
}

// PullRequestSession is a session of a pull request with its decisions.
type PullRequestSession struct {
	SessionDecisions
	// Title is the linked session's; a live one's is in the live list.
	Title string `json:"title,omitempty"`
	// Live is a session the tracker follows, not over yet.
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
// the tracker links to it now and those its saved review links). Decisions
// dated after that review was generated are provisional.
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
			var list []SessionDecisions
			list, err = a.Store.Sessions(r.Context(), SessionKey{agent, session})
			if err == nil {
				body = map[string]SessionDecisions{"session": list[0]}
			}
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
	if a.Live != nil {
		for _, s := range a.Live() {
			if s.State != livesessions.StateEnded && s.PR == number && strings.EqualFold(s.Repository, repository) {
				add(SessionKey{s.Agent, s.ID}, "", true)
			}
		}
	}
	var generated time.Time
	if a.Linked != nil {
		linked, at, err := a.Linked(ctx, repository, number)
		if err != nil {
			return answer, err
		}
		for _, l := range linked {
			add(l.SessionKey, l.Title, false)
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
