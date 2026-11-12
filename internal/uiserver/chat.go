package uiserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/baum-ai/baum/internal/chat"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
)

// ChatQueue lists the stored conversations of the earlier Ask feature;
// *jobs.Queue implements it.
type ChatQueue interface {
	Conversations(ctx context.Context, clone string, pr int) ([]store.Conversation, error)
}

// ChatHandler serves GET /api/prs/chat: a pull request's stored
// conversations with the agent that wrote its review, read-only. The
// repository is a registered clone ID, as for /api/versions.
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
	return mux
}

// chatError answers {code, message}, which the app shows as it is.
func chatError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}

// chatFailure answers a refused listing with its code.
func chatFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, chat.ErrInvalid):
		chatError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, repos.ErrNotFound), errors.Is(err, chat.ErrRepositoryNotFound):
		chatError(w, http.StatusNotFound, "not_found", "The repository is not registered.")
	case errors.Is(err, jobs.ErrChatUnavailable):
		chatError(w, http.StatusServiceUnavailable, "chat_unavailable", "Chat is not available.")
	default:
		chatError(w, http.StatusInternalServerError, "internal", "baum could not read the conversations.")
	}
}
