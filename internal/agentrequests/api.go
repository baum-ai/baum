package agentrequests

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// Handler serves the app's routes, which the daemon mounts behind the same
// authentication as every application route:
//
//   - GET /api/agent-requests: {"requests","messages","settings"}, the
//     pending requests oldest first and the queued messages;
//   - POST /api/agent-requests/{id}/answer with a Reply: {"ok":true}, 409
//     request_gone for a request no longer pending, 400 bad_request for a
//     reply that does not fit it;
//   - PUT /api/agent-requests/settings with Settings: the settings saved;
//   - POST /api/agent-messages with {"agent","session_id","text"}: queues
//     it, 404 session_not_running for a session no Stop hook would come
//     from;
//   - DELETE /api/agent-messages/{agent}/{id}: removes a queued message.
func (s *Store) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/agent-requests", func(w http.ResponseWriter, _ *http.Request) {
		requests, messages := s.List()
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, struct {
			Requests []Request `json:"requests"`
			Messages []Message `json:"messages"`
			Settings Settings  `json:"settings"`
		}{requests, messages, s.Settings()})
	})
	mux.HandleFunc("POST /api/agent-requests/{id}/answer", func(w http.ResponseWriter, r *http.Request) {
		var reply Reply
		if !readJSON(w, r, &reply) {
			return
		}
		switch err := s.Answer(r.PathValue("id"), reply); {
		case errors.Is(err, ErrGone):
			writeError(w, http.StatusConflict, "request_gone", err.Error())
		case err != nil:
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		default:
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		}
	})
	mux.HandleFunc("PUT /api/agent-requests/settings", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			CodexApprovals *bool `json:"codex_approvals"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		if input.CodexApprovals == nil {
			writeError(w, http.StatusBadRequest, "bad_request", "codex_approvals is required")
			return
		}
		settings, err := s.SetSettings(Settings{CodexApprovals: *input.CodexApprovals})
		if err != nil {
			s.logger().Error("agent request settings not saved", "error", err)
			writeError(w, http.StatusInternalServerError, "settings_not_saved", "The settings could not be saved.")
			return
		}
		writeJSON(w, http.StatusOK, settings)
	})
	mux.HandleFunc("POST /api/agent-messages", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Agent     string `json:"agent"`
			SessionID string `json:"session_id"`
			Text      string `json:"text"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		switch err := s.Queue(input.Agent, input.SessionID, input.Text); {
		case errors.Is(err, ErrNotRunning):
			writeError(w, http.StatusNotFound, "session_not_running", "This session is not running, so it cannot be sent a message.")
		case err != nil:
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		default:
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		}
	})
	mux.HandleFunc("DELETE /api/agent-messages/{agent}/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.DeleteMessage(r.PathValue("agent"), r.PathValue("id"))
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	return mux
}

// readJSON decodes a small JSON body, answering 400 when it is not one.
func readJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		writeError(w, http.StatusBadRequest, "bad_request", "The request is not valid JSON.")
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
