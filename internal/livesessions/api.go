package livesessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/baum-ai/baum/internal/focus"
	"github.com/baum-ai/baum/internal/hookevents"
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
// {"available":true,"version","live","transcript","summary"?,"images"?}, the
// transcript as GET /api/sessions/{id} answers it, the session's latest
// compaction summary (masked) and the media types of the images pasted with
// each prompt, by its turn, all left out when the request names the current
// version (?version=), so a window polling a session that did not change
// receives a few bytes; or {"available":false,"live",
// "code":"transcript_unavailable","message"} with why it is not served. A
// session the tracker does not list is 404.
//
// GET /api/live-sessions/{agent}/{id}/transcript/images/{turn}/{n} is the
// n-th of those images (from 0), as it was pasted: images are not masked.
// One the transcript does not hold is 404.
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
	mux.HandleFunc("GET /api/live-sessions/{agent}/{id}/transcript/images/{turn}/{n}", t.handleImage)
	if t.Focus != nil {
		mux.HandleFunc("POST /api/live-sessions/{agent}/{id}/focus", t.focusHandler)
	}
	return mux
}

func (t *Transcripts) handleImage(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "image_not_found", "message": "The transcript holds no such image."})
		return
	}
	mediaType, data, err := t.Image(r.Context(), r.PathValue("agent"), r.PathValue("id"), r.PathValue("turn"), n)
	var missing *Unavailable
	switch {
	case errors.Is(err, ErrNotTracked):
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "live_session_not_found", "message": "This session is no longer listed."})
	case errors.Is(err, ErrNoImage), errors.As(err, &missing):
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "image_not_found", "message": "The transcript holds no such image."})
	case err != nil:
		// The request went away, or the file could not be read.
		if r.Context().Err() == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "image_unreadable", "message": "The image could not be read."})
		}
	default:
		// A pasted image never changes; only this window's session can ask.
		w.Header().Set("Cache-Control", "private, max-age=86400")
		w.Header().Set("Content-Type", mediaType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}
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
			Available  bool                `json:"available"`
			Version    string              `json:"version"`
			Live       bool                `json:"live"`
			Transcript json.RawMessage     `json:"transcript,omitempty"`
			Summary    string              `json:"summary,omitempty"`
			Images     map[string][]string `json:"images,omitempty"`
		}{Available: true, Version: answer.Version, Live: live, Transcript: answer.Body, Images: answer.Images}
		if r.URL.Query().Get("version") == answer.Version {
			out.Transcript, out.Images = nil, nil
		} else if t.Tracker != nil {
			if _, path, ok := t.Tracker.Lookup(answer.Session.Agent, answer.Session.ID); ok {
				_, _, out.Summary, _ = t.describe(key{answer.Session.Agent, answer.Session.ID}, path, false)
			}
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// Focuser brings a session forward from where its hooks said it runs;
// *focus.Focuser is one.
type Focuser interface {
	Predict(agent string, terminal *hookevents.Terminal) string
	Focus(ctx context.Context, agent, id string, terminal *hookevents.Terminal) focus.Result
}

// focusHandler serves POST /api/live-sessions/{agent}/{id}/focus: it brings a
// running session forward and answers {"focused":"session"|"window",
// "message"?}, the message saying what could not be done; or, when nothing
// was brought forward, 409 {"code":"focus_failed","message"} with why. A
// session the tracker does not list is 404.
func (t *Transcripts) focusHandler(w http.ResponseWriter, r *http.Request) {
	terminal, ok := t.Tracker.Terminal(r.PathValue("agent"), r.PathValue("id"))
	switch {
	case !ok:
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "live_session_not_found", "message": "This session is no longer listed."})
		return
	case terminal == nil:
		writeJSON(w, http.StatusConflict, map[string]any{"code": "focus_failed", "message": "Only a running session whose hooks said where it runs can be brought forward."})
		return
	}
	result := t.Focus.Focus(r.Context(), r.PathValue("agent"), r.PathValue("id"), terminal)
	if result.Precision == "" {
		writeJSON(w, http.StatusConflict, map[string]any{"code": "focus_failed", "message": result.Message})
		return
	}
	out := map[string]any{"focused": result.Precision}
	if result.Message != "" {
		out["message"] = result.Message
	}
	writeJSON(w, http.StatusOK, out)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
