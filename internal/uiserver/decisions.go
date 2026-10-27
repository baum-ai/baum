package uiserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/viewserver"
)

// agentOfVendor names a linked session's vendor as the hooks name its agent.
var agentOfVendor = map[string]string{"claude_code": "claude-code", "codex": "codex", "cursor": "cursor"}

// LinkedSession is a session a pull request's saved review links on this
// machine, named as the hooks name its agent.
type LinkedSession struct{ Agent, ID, Title string }

// LinkedSessions lists the sessions a pull request's latest saved review
// links, and when that review was generated (zero without one).
type LinkedSessions func(ctx context.Context, repository string, number int) ([]LinkedSession, time.Time, error)

// linkedSessions lists the sessions the latest saved review of a pull request
// links on this machine, as its PR page's sessions list does (sharing that
// computation), and when the review was generated. A pull request without a
// saved review, or whose sessions cannot be linked, links none.
func (a LocalAPI) linkedSessions(cache *documentCache) LinkedSessions {
	return func(ctx context.Context, repository string, number int) ([]LinkedSession, time.Time, error) {
		clones, err := a.Registry.List(ctx)
		if err != nil {
			return nil, time.Time{}, err
		}
		for _, c := range clones {
			if !strings.EqualFold(c.Repository, repository) || !c.Available {
				continue
			}
			s, err := store.Open(c.CommonDir, "", false)
			if err != nil {
				continue
			}
			version, err := s.Latest(ctx, store.Key{Repository: c.Repository, Number: number})
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, time.Time{}, err
			}
			if len(version.Document) > viewserver.MaxDocumentBytes {
				return nil, time.Time{}, nil
			}
			var parsed struct {
				Review struct {
					GeneratedAt time.Time `json:"generated_at"`
				} `json:"review"`
			}
			_ = json.Unmarshal(version.Document, &parsed)
			if !acquire(ctx, cache.requests) {
				return nil, time.Time{}, ctx.Err()
			}
			defer func() { <-cache.requests }()
			viewer, release, err := cache.get(c, version.Document)
			if err != nil {
				return nil, parsed.Review.GeneratedAt, nil
			}
			defer release()
			result, ok := viewer.LinkedSessions(ctx)
			if !ok {
				return nil, time.Time{}, context.Cause(ctx)
			}
			var linked []LinkedSession
			for _, session := range result.Sessions {
				if agent, known := agentOfVendor[session.Vendor]; known {
					linked = append(linked, LinkedSession{Agent: agent, ID: session.ID, Title: session.Title})
				}
			}
			return linked, parsed.Review.GeneratedAt, nil
		}
		return nil, time.Time{}, nil
	}
}

// decisionSettings are the app's decision capture preferences as the app
// edits them: a switch, a model (empty is the backend's default) and an
// optional daily cap in US dollars.
type decisionSettings struct {
	Enabled  bool     `json:"enabled"`
	Model    string   `json:"model"`
	DailyCap *float64 `json:"daily_cap"`
}

func decisionSettingsOf(p repos.Preferences) decisionSettings {
	d := p.Decisions
	out := decisionSettings{Enabled: d.Enabled == nil || *d.Enabled, DailyCap: d.DailyCap}
	if d.Model != nil {
		out.Model = *d.Model
	}
	return out
}

// putDecisionSettings serves PUT /api/settings/decisions, which replaces the
// whole decision capture preferences.
func (a LocalAPI) putDecisionSettings(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Enabled  *bool    `json:"enabled"`
		Model    *string  `json:"model"`
		DailyCap *float64 `json:"daily_cap"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF || input.Enabled == nil {
		http.Error(w, "invalid JSON request", http.StatusBadRequest)
		return
	}
	var invalid error
	prefs, err := a.Registry.UpdatePreferences(r.Context(), func(p repos.Preferences) repos.Preferences {
		next := p
		next.Decisions = repos.DecisionPreferences{DailyCap: input.DailyCap}
		if !*input.Enabled {
			next.Decisions.Enabled = new(false)
		}
		if input.Model != nil && strings.TrimSpace(*input.Model) != "" {
			next.Decisions.Model = new(strings.TrimSpace(*input.Model))
		}
		if invalid = next.Validate(); invalid != nil {
			return p
		}
		return next
	})
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if invalid != nil {
		http.Error(w, invalid.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]decisionSettings{"decisions": decisionSettingsOf(prefs)})
}
