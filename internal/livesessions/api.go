package livesessions

import (
	"encoding/json"
	"errors"
	"net/http"
)

// Handler serves GET /api/live-sessions: {"sessions": [...]} with the
// Session of each tracked session, the most recently active first. The
// daemon mounts it behind the same authentication as every application
// route.
func (t *Tracker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/live-sessions", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Sessions []Session `json:"sessions"`
		}{t.Sessions()})
	})
	return mux
}

// Handler serves the tracker's list (Tracker.Handler) and GET
// /api/live-sessions/{agent}/{id}/transcript: the transcript of a session
// the list has, for as long as it has it. The answer is
// {"available":true,"version","live","transcript"}, the transcript as GET
// /api/sessions/{id} answers it, without "transcript" when the request names
// the current version (?version=), so a window polling a session that did not
// change receives a few bytes; or {"available":false,"live",
// "code":"transcript_unavailable","message"} with why it is not served. A
// session the tracker does not list is 404.
func (t *Transcripts) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /api/live-sessions", t.Tracker.Handler())
	mux.HandleFunc("GET /api/live-sessions/{agent}/{id}/transcript", func(w http.ResponseWriter, r *http.Request) {
		answer, err := t.Get(r.Context(), r.PathValue("agent"), r.PathValue("id"))
		w.Header().Set("Cache-Control", "no-store")
		live := answer.Session.State != StateEnded
		var missing *Unavailable
		switch {
		case errors.Is(err, ErrNotTracked):
			writeJSON(w, http.StatusNotFound, map[string]any{"code": "live_session_not_found", "message": "This session is no longer listed."})
		case errors.As(err, &missing):
			writeJSON(w, http.StatusOK, map[string]any{"available": false, "live": live, "code": "transcript_unavailable", "message": missing.Reason})
		case err != nil:
			// The request went away.
		default:
			out := struct {
				Available  bool            `json:"available"`
				Version    string          `json:"version"`
				Live       bool            `json:"live"`
				Transcript json.RawMessage `json:"transcript,omitempty"`
			}{true, answer.Version, live, answer.Body}
			if r.URL.Query().Get("version") == answer.Version {
				out.Transcript = nil
			}
			writeJSON(w, http.StatusOK, out)
		}
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
