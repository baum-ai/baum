package automatic

import (
	"encoding/json"
	"io"
	"net/http"

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
			Enabled                 *bool `json:"enabled"`
			CursorAgentAcknowledged *bool `json:"cursor_agent_acknowledged"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid JSON request", http.StatusBadRequest)
			return
		}
		_, err := s.Registry.UpdatePreferences(r.Context(), func(p repos.Preferences) repos.Preferences {
			if input.Enabled != nil {
				p.Automatic = *input.Enabled
			}
			if input.CursorAgentAcknowledged != nil {
				p.CursorAgentAcknowledged = *input.CursorAgentAcknowledged
			}
			return p
		})
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
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
