package automatic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/baum-ai/baum/agent/claude"
	"github.com/baum-ai/baum/agent/codex"
	"github.com/baum-ai/baum/internal/agentdirs"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/repos"
)

// PreferencesInput is the body of PUT /api/automatic: each field set is a
// change, each nil one keeps the saved choice.
type PreferencesInput struct {
	Enabled      *bool   `json:"enabled,omitempty"`
	Backend      *string `json:"backend,omitempty"`
	Model        *string `json:"model,omitempty"`
	FixBackend   *string `json:"fix_backend,omitempty"`
	FixModel     *string `json:"fix_model,omitempty"`
	Effort       *string `json:"effort,omitempty"`
	FixEffort    *string `json:"fix_effort,omitempty"`
	PullRequests *string `json:"pull_requests,omitempty"`
	// The Claude Code configuration directories of reviews and fixes; empty
	// goes back to the environment's. The route checks each with
	// agentdirs.Agent.Choose before the save.
	ClaudeConfigDir    *string `json:"claude_config_dir,omitempty"`
	FixClaudeConfigDir *string `json:"fix_claude_config_dir,omitempty"`
	// The Codex homes of reviews and fixes, likewise.
	CodexHome    *string `json:"codex_home,omitempty"`
	FixCodexHome *string `json:"fix_codex_home,omitempty"`
}

// EditPreferences is the change PUT /api/automatic makes of the saved
// preferences p, or the reason the route refuses it (400) and p is kept.
func EditPreferences(p repos.Preferences, input PreferencesInput) (repos.Preferences, error) {
	next := p
	if input.Enabled != nil {
		next.Automatic = *input.Enabled
	}
	if input.Backend != nil {
		backend, err := config.ParseBackend(*input.Backend)
		if err != nil {
			return p, err
		}
		next.Backend = &backend
	}
	if input.Model != nil {
		model := strings.TrimSpace(*input.Model)
		next.Model = &model
	}
	if input.FixBackend != nil {
		if strings.TrimSpace(*input.FixBackend) == "" {
			// Back to the same agent as reviews.
			next.FixBackend, next.FixModel, next.FixEffort = nil, nil, nil
			next.FixClaudeConfigDir, next.FixCodexHome = nil, nil
		} else {
			backend, err := config.ParseBackend(*input.FixBackend)
			if err != nil {
				return p, err
			}
			next.FixBackend = &backend
		}
	}
	if input.FixModel != nil {
		model := strings.TrimSpace(*input.FixModel)
		next.FixModel = &model
		// Following reviews again sends an empty model, which is no choice.
		if model == "" && next.FixBackend == nil {
			next.FixModel = nil
		}
	}
	for _, e := range []struct{ input, target **string }{{&input.Effort, &next.Effort}, {&input.FixEffort, &next.FixEffort}} {
		if *e.input == nil {
			continue
		}
		var err error
		if *e.target, err = config.ParseEffort(**e.input); err != nil {
			return p, err
		}
	}
	if input.ClaudeConfigDir != nil {
		next.ClaudeConfigDir = nonEmpty(*input.ClaudeConfigDir)
	}
	// Following reviews again sends an empty directory, which is no choice;
	// one without a fix agent is refused below.
	if input.FixClaudeConfigDir != nil {
		next.FixClaudeConfigDir = nonEmpty(*input.FixClaudeConfigDir)
	}
	if input.CodexHome != nil {
		next.CodexHome = nonEmpty(*input.CodexHome)
	}
	if input.FixCodexHome != nil {
		next.FixCodexHome = nonEmpty(*input.FixCodexHome)
	}
	if input.PullRequests != nil {
		next.PullRequests = *input.PullRequests
		if next.PullRequests == "both" {
			next.PullRequests = ""
		}
	}
	if err := next.ValidateChange(p); err != nil {
		return p, err
	}
	return next, nil
}

// SavePreferences saves input as PUT /api/automatic does, when check (if set)
// accepts the preferences it is saved over: check runs in the writing
// transaction, so it and the write are one step. An error of check or of
// EditPreferences saves nothing and is returned as it is. After a save it
// does what the route does after one: cancels queued automatic jobs that are
// no longer allowed, wakes the scheduler and tells the app. It does not check
// configuration directories as the route does (agentdirs.Agent.Choose), so a
// caller sets none.
func (s *Scheduler) SavePreferences(ctx context.Context, input PreferencesInput, check func(repos.Preferences) error) error {
	_, err := s.Registry.UpdatePreferencesIf(ctx, func(p repos.Preferences) (repos.Preferences, error) {
		if check != nil {
			if err := check(p); err != nil {
				return p, err
			}
		}
		return EditPreferences(p, input)
	})
	if err != nil {
		return err
	}
	s.preferencesChanged(ctx)
	return nil
}

// preferencesChanged follows a saved change of the automatic preferences.
func (s *Scheduler) preferencesChanged(ctx context.Context) {
	// Turning off (the tray's pause included) cancels queued automatic
	// jobs before answering; running ones finish.
	if s.Queue != nil {
		s.Queue.CancelDisallowedAutomatic()
	}
	s.Wake()
	// Other windows and the desktop app learn of the change.
	if o, err := s.Overview(ctx); err == nil {
		s.notify(o.Repositories)
	}
}

// nonEmpty is a pointer to s, or nil when it is empty.
func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Handler serves GET and PUT /api/automatic and POST
// /api/automatic/catch-up (CatchUp). Like the registry handler it
// does not authenticate: mount it only behind the daemon's authenticated,
// Host/Origin-checked middleware.
func (s *Scheduler) Handler() http.Handler {
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, r *http.Request) {
		o, err := s.Overview(r.Context())
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(o)
	}
	mux.HandleFunc("GET /api/automatic", write)
	mux.HandleFunc("PUT /api/automatic", func(w http.ResponseWriter, r *http.Request) {
		var input PreferencesInput
		r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid JSON request", http.StatusBadRequest)
			return
		}
		var invalid error
		// The directories are checked, and remembered for reading their
		// sessions, before the choice is stored.
		claudeSignedIn, codexSignedIn := s.ClaudeSignedIn, s.CodexSignedIn
		if claudeSignedIn == nil {
			claudeSignedIn = claude.SignedInWithConfigDir(s.ClaudeBinary)
		}
		if codexSignedIn == nil {
			codexSignedIn = codex.SignedInWithHome(s.CodexBinary)
		}
		for _, choice := range []struct {
			agent    agentdirs.Agent
			given    *string
			signedIn agentdirs.SignedIn
		}{
			{agentdirs.Claude, input.ClaudeConfigDir, claudeSignedIn},
			{agentdirs.Claude, input.FixClaudeConfigDir, claudeSignedIn},
			{agentdirs.Codex, input.CodexHome, codexSignedIn},
			{agentdirs.Codex, input.FixCodexHome, codexSignedIn},
		} {
			if choice.given == nil {
				continue
			}
			dir, err := choice.agent.Choose(r.Context(), *choice.given, choice.signedIn)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			*choice.given = dir
		}
		_, err := s.Registry.UpdatePreferences(r.Context(), func(p repos.Preferences) repos.Preferences {
			var next repos.Preferences
			if next, invalid = EditPreferences(p, input); invalid != nil {
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
		s.preferencesChanged(r.Context())
		write(w, r)
	})
	// Desktop setup asks for this once, when it finishes. The polls can take
	// a while, so it answers at once and the catch-up does not end with the
	// request.
	mux.HandleFunc("POST /api/automatic/catch-up", func(w http.ResponseWriter, r *http.Request) {
		go func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Minute)
			defer cancel()
			if err := s.CatchUp(ctx, SetupCatchUp); err != nil {
				s.log().Warn("setup catch-up failed", "error", err)
			}
		}()
		w.WriteHeader(http.StatusAccepted)
	})
	return mux
}
