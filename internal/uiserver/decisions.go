package uiserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/viewserver"
)

func (a LocalAPI) logger() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.Default()
}

// LinkedSessions lists the sessions a pull request's latest saved review
// links on this machine, and when that review was generated (zero without
// one). It is an alias, so decisionlog.API takes it as it is.
type LinkedSessions = func(ctx context.Context, repository string, number int) ([]locallink.Session, time.Time, error)

// linkedSessions lists the sessions the latest saved review of a pull request
// links on this machine, as its PR page's sessions list does (sharing that
// computation), and when the review was generated. A pull request without a
// saved review links none; one whose sessions cannot be linked links none and
// is logged.
func (a LocalAPI) linkedSessions(cache *documentCache) LinkedSessions {
	return func(ctx context.Context, repository string, number int) ([]locallink.Session, time.Time, error) {
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
			var parsed struct {
				Review struct {
					GeneratedAt time.Time `json:"generated_at"`
				} `json:"review"`
			}
			generated := func() time.Time {
				_ = json.Unmarshal(version.Document, &parsed)
				return parsed.Review.GeneratedAt
			}
			unlinked := func(reason string, args ...any) ([]locallink.Session, time.Time, error) {
				a.logger().Warn("decisions: the saved review's sessions were not linked", append([]any{"repository", repository, "pr", number, "reason", reason}, args...)...)
				return nil, generated(), nil
			}
			if len(version.Document) > viewserver.MaxDocumentBytes {
				return unlinked("document too large")
			}
			if !acquire(ctx, cache.requests) {
				return nil, time.Time{}, ctx.Err()
			}
			defer func() { <-cache.requests }()
			viewer, release, err := cache.get(c, version.Document)
			if err != nil {
				return unlinked("viewer", "error", err)
			}
			defer release()
			result, ok := viewer.LinkedSessions(ctx)
			if !ok {
				if ctx.Err() != nil {
					return nil, time.Time{}, context.Cause(ctx)
				}
				return unlinked("viewer stopped")
			}
			if !result.Available && result.Code == locallink.CodeError {
				return unlinked(result.Reason)
			}
			return result.Sessions, generated(), nil
		}
		return nil, time.Time{}, nil
	}
}

// decisionSettings are the app's decision capture preferences as the app
// edits them: a switch, the data extraction backend (empty follows the
// reviews') and model (empty is the backend's default).
type decisionSettings struct {
	Enabled bool   `json:"enabled"`
	Backend string `json:"backend"`
	Model   string `json:"model"`
	Effort  string `json:"effort"`
}

// DecisionModel is the backend and decision model capture uses
// (decisionlog.EffectiveModel), and whether the chosen model is ignored.
type DecisionModel struct {
	Backend string `json:"effective_backend"`
	// Undetermined is a backend whose checks did not answer in time.
	Undetermined bool   `json:"backend_undetermined"`
	Model        string `json:"effective_model"`
	Ignored      bool   `json:"model_ignored"`
}

// decisionView is GET /api/settings/decisions: the settings and what
// capture makes of them.
type decisionView struct {
	decisionSettings
	DecisionModel
}

func (a LocalAPI) decisionView(ctx context.Context, p repos.Preferences) decisionView {
	view := decisionView{decisionSettings: decisionSettingsOf(p)}
	if a.DecisionModel != nil {
		view.DecisionModel = a.DecisionModel(ctx, p)
	}
	return view
}

// getDecisionSettings serves GET /api/settings/decisions. Resolving an auto
// backend runs the backends' checks, so it is apart from GET /api/settings.
func (a LocalAPI) getDecisionSettings(w http.ResponseWriter, r *http.Request) {
	prefs, err := a.Registry.Preferences(r.Context())
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]decisionView{"decisions": a.decisionView(r.Context(), prefs)})
}

func decisionSettingsOf(p repos.Preferences) decisionSettings {
	d := p.Decisions
	out := decisionSettings{Enabled: d.Enabled == nil || *d.Enabled}
	if d.Backend != nil {
		out.Backend = string(*d.Backend)
	}
	if d.Model != nil {
		out.Model = *d.Model
	}
	if d.Effort != nil {
		out.Effort = *d.Effort
	}
	return out
}

// putDecisionSettings serves PUT /api/settings/decisions, which replaces the
// whole decision capture preferences, except that a missing backend or
// effort keeps the saved one, as an app from before them sends neither.
func (a LocalAPI) putDecisionSettings(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Enabled *bool   `json:"enabled"`
		Backend *string `json:"backend"`
		Model   *string `json:"model"`
		Effort  *string `json:"effort"`
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
		next.Decisions = repos.DecisionPreferences{Backend: p.Decisions.Backend, Effort: p.Decisions.Effort}
		if !*input.Enabled {
			next.Decisions.Enabled = new(false)
		}
		if input.Backend != nil {
			next.Decisions.Backend = nil
			if *input.Backend != "" {
				next.Decisions.Backend = new(config.Backend(*input.Backend))
			}
		}
		if input.Model != nil && strings.TrimSpace(*input.Model) != "" {
			next.Decisions.Model = new(strings.TrimSpace(*input.Model))
		}
		if input.Effort != nil {
			next.Decisions.Effort = nil
			if *input.Effort != "" {
				next.Decisions.Effort = input.Effort
			}
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
	if a.DecisionsChanged != nil {
		a.DecisionsChanged()
	}
	writeJSON(w, map[string]decisionView{"decisions": a.decisionView(r.Context(), prefs)})
}
