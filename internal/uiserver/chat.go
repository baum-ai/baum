package uiserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/baum-ai/baum/internal/chat"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
)

// ChatQueue queues questions to a review's agent and lists the
// conversations; *jobs.Queue implements it.
type ChatQueue interface {
	Ask(ctx context.Context, ask chat.Ask) (jobs.ChatQueued, error)
	Conversations(ctx context.Context, clone string, pr int) ([]store.Conversation, error)
}

// maxChatRequest bounds a question's request body: a question of at most
// chat.MaxQuestionRunes characters and a focus with its anchors.
const maxChatRequest = 64 << 10

// ChatHandler serves GET and POST /api/prs/chat: a pull request's
// conversations with the agent that wrote its review, and asking a question.
// The repository is a registered clone ID, as for /api/versions.
func ChatHandler(registry *repos.Registry, queue ChatQueue) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/prs/chat", func(w http.ResponseWriter, r *http.Request) {
		clone := r.URL.Query().Get("clone")
		pr, err := strconv.Atoi(r.URL.Query().Get("pr"))
		if clone == "" || err != nil || pr < 1 {
			chatError(w, http.StatusBadRequest, "invalid_request", "clone and a positive pr are required")
			return
		}
		if _, err := RegisteredRepository(r.Context(), registry, clone); err != nil {
			chatFailure(w, err)
			return
		}
		list, err := queue.Conversations(r.Context(), clone, pr)
		if err != nil {
			chatFailure(w, err)
			return
		}
		writeJSON(w, map[string]any{"conversations": list})
	})
	mux.HandleFunc("POST /api/prs/chat", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxChatRequest)
		var body struct {
			Clone          string      `json:"clone"`
			PR             int         `json:"pr"`
			Version        int         `json:"version"`
			ConversationID string      `json:"conversation_id"`
			Question       string      `json:"question"`
			Focus          *chat.Focus `json:"focus"`
		}
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if d.Decode(&body) != nil || d.Decode(new(any)) != io.EOF {
			chatError(w, http.StatusBadRequest, "invalid_request", "the question is not valid JSON of the expected shape")
			return
		}
		if body.Clone == "" || body.PR < 1 || body.Version < 1 {
			chatError(w, http.StatusBadRequest, "invalid_request", "clone, a positive pr and a positive version are required")
			return
		}
		if _, err := RegisteredRepository(r.Context(), registry, body.Clone); err != nil {
			chatFailure(w, err)
			return
		}
		queued, err := queue.Ask(r.Context(), chat.Ask{Clone: body.Clone, PR: body.PR, Version: body.Version, Conversation: body.ConversationID, Question: body.Question, Focus: body.Focus})
		if err != nil {
			chatFailure(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(queued)
	})
	return mux
}

// chatError answers {code, message}, which the app shows as it is.
func chatError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}

// chatFailure answers a refused question or listing with its code.
func chatFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, chat.ErrInvalid):
		chatError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, repos.ErrNotFound), errors.Is(err, chat.ErrRepositoryNotFound):
		chatError(w, http.StatusNotFound, "not_found", "The repository is not registered.")
	case errors.Is(err, chat.ErrNotFound):
		chatError(w, http.StatusNotFound, "not_found", "That conversation does not exist for this pull request.")
	case errors.Is(err, chat.ErrNoReview):
		chatError(w, http.StatusNotFound, "no_review", "There is no saved review of this pull request at that version.")
	case errors.Is(err, chat.ErrNeedsClone):
		chatError(w, http.StatusConflict, jobs.ReasonNeedsClone, "Asking about a review needs a local clone of the repository.")
	case errors.Is(err, chat.ErrUnsupportedBackend):
		chatError(w, http.StatusUnprocessableEntity, "unsupported_backend", "Only a review written by Claude Code or Codex on this computer can be asked about.")
	case errors.Is(err, chat.ErrBusy):
		chatError(w, http.StatusConflict, "busy", "The agent is still answering the last question of this conversation.")
	case errors.Is(err, jobs.ErrRestarting):
		w.Header().Set("Retry-After", "5")
		chatError(w, http.StatusServiceUnavailable, "restarting", jobs.ErrRestarting.Error())
	case errors.Is(err, jobs.ErrChatUnavailable):
		chatError(w, http.StatusServiceUnavailable, "chat_unavailable", "Chat is not available.")
	default:
		chatError(w, http.StatusInternalServerError, "internal", "baum could not read or store the conversation.")
	}
}
