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

// Handler serves the tracker's list as Tracker.Handler does, each session
// with its title (Listed), and GET
// /api/live-sessions/{agent}/{id}/transcript: the transcript of a session
// the list has, for as long as it has it. The answer is
// {"available":true,"version","live","transcript","summary"?}, the transcript
// as GET /api/sessions/{id} answers it and the session's latest compaction
// summary (masked), both left out when the request names the current version
// (?version=), so a window polling a session that did not
// change receives a few bytes; or {"available":false,"live",
// "code":"transcript_unavailable","message"} with why it is not served. A
// session the tracker does not list is 404.
func (t *Transcripts) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/live-sessions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, struct {
			Sessions []Listed `json:"sessions"`
		}{t.listed()})
	})
	mux.HandleFunc("POST /api/live-sessions/{agent}/{id}/end", t.handleEnd)
	mux.HandleFunc("POST /api/live-sessions/{agent}/{id}/message", t.handleMessage)
	mux.HandleFunc("GET /api/live-sessions/{agent}/{id}/transcript", func(w http.ResponseWriter, r *http.Request) {
		answer, err := t.Get(r.Context(), r.PathValue("agent"), r.PathValue("id"))
		t.WriteAnswer(w, r, answer, err)
	})
	return mux
}

// WriteAnswer writes a transcript answer as the live sessions' transcript
// route does (Handler), for any route that serves one: the summary is the
// tracked session's.
func (t *Transcripts) WriteAnswer(w http.ResponseWriter, r *http.Request, answer Answer, err error) {
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
			Summary    string          `json:"summary,omitempty"`
		}{Available: true, Version: answer.Version, Live: live, Transcript: answer.Body}
		if r.URL.Query().Get("version") == answer.Version {
			out.Transcript = nil
		} else if t.Tracker != nil {
			if _, path, ok := t.Tracker.Lookup(answer.Session.Agent, answer.Session.ID); ok {
				_, _, out.Summary, _ = t.describe(key{answer.Session.Agent, answer.Session.ID}, path, false)
			}
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
