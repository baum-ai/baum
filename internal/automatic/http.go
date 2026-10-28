package automatic

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/repos"
)

// Handler serves GET and PUT /api/automatic. Like the registry handler it
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
		var input struct {
			Enabled      *bool   `json:"enabled"`
			Backend      *string `json:"backend"`
			Model        *string `json:"model"`
			PullRequests *string `json:"pull_requests"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid JSON request", http.StatusBadRequest)
			return
		}
		var invalid error
		_, err := s.Registry.UpdatePreferences(r.Context(), func(p repos.Preferences) repos.Preferences {
			next := p
			if input.Enabled != nil {
				next.Automatic = *input.Enabled
			}
			if input.Backend != nil {
				backend, err := config.ParseBackend(*input.Backend)
				if err != nil {
					invalid = err
					return p
				}
				next.Backend = &backend
			}
			if input.Model != nil {
				model := strings.TrimSpace(*input.Model)
				next.Model = &model
			}
			if input.PullRequests != nil {
				next.PullRequests = *input.PullRequests
				if next.PullRequests == "both" {
					next.PullRequests = ""
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
		// Turning off (the tray's pause included) cancels queued automatic
		// jobs before answering; running ones finish.
		if s.Queue != nil {
			s.Queue.CancelDisallowedAutomatic()
		}
		s.Wake()
		// Other windows and the desktop app learn of the change.
		if o, err := s.Overview(r.Context()); err == nil {
			s.notify(o.Repositories)
		}
		write(w, r)
	})
	return mux
}
