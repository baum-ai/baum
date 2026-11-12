package console

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/baum-ai/baum/internal/agentcontext"
)

// maxBody bounds a request body: a message and its page.
const maxBody = MaxMessageBytes + 64<<10

// Summary is a conversation as the panel's picker lists it.
type Summary struct {
	ID            string    `json:"id"`
	Title         string    `json:"title"`
	Agent         string    `json:"agent"`
	Model         string    `json:"model"`
	Mode          string    `json:"mode"`
	State         string    `json:"state"`
	Directory     string    `json:"directory"`
	DirectoryNote string    `json:"directory_note,omitempty"`
	Repository    string    `json:"repository,omitempty"`
	PullRequest   int       `json:"pull_request,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
	// Session is the agent's session as live sessions and MCP consumers key
	// it, "agent:id", once known.
	Session string `json:"session,omitempty"`
}

func summarize(c Conversation) Summary {
	out := Summary{ID: c.ID, Title: c.Title, Agent: c.Agent, Model: c.Model, Mode: c.Mode, State: c.State, Directory: c.Directory,
		DirectoryNote: c.DirectoryNote, Repository: c.Repository, PullRequest: c.PullRequest, UpdatedAt: c.UpdatedAt}
	if c.Session != "" {
		out.Session = agentcontext.SessionKey(c.Agent, c.Session)
	}
	return out
}

// Detail is one conversation as the panel shows it: all it keeps, its
// session key, and the running or last turn since the daemon started.
type Detail struct {
	Conversation
	SessionKey string    `json:"session_key,omitempty"`
	Progress   *Progress `json:"progress"`
}

func (s *Service) detail(c Conversation) Detail {
	d := Detail{Conversation: c}
	if c.Session != "" {
		d.SessionKey = agentcontext.SessionKey(c.Agent, c.Session)
	}
	if p, ok := s.Progress(c.ID); ok {
		d.Progress = &p
	}
	return d
}

// Handler serves the session panel's routes under /api/console/ (see
// ARCHITECTURE.md, "Session panel conversations"). The app server checks
// the launch capability or cookie, the Host, and that a change is a
// same-origin JSON request, as for every other route.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/console/conversations", func(w http.ResponseWriter, r *http.Request) {
		list, err := s.List(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		out := make([]Summary, 0, len(list))
		for _, c := range list {
			out = append(out, summarize(c))
		}
		writeJSON(w, http.StatusOK, map[string]any{"conversations": out})
	})
	mux.HandleFunc("POST /api/console/conversations", func(w http.ResponseWriter, r *http.Request) {
		var input CreateRequest
		if !decode(w, r, &input) {
			return
		}
		c, err := s.Create(r.Context(), input)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, s.detail(c))
	})
	mux.HandleFunc("GET /api/console/conversations/{id}", func(w http.ResponseWriter, r *http.Request) {
		c, err := s.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s.detail(c))
	})
	mux.HandleFunc("POST /api/console/conversations/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Text string                    `json:"text"`
			Page *agentcontext.PageContext `json:"page,omitempty"`
		}
		if !decode(w, r, &input) {
			return
		}
		c, err := s.Send(r.Context(), r.PathValue("id"), input.Text, input.Page)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, s.detail(c))
	})
	mux.HandleFunc("POST /api/console/conversations/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if !decode(w, r, &struct{}{}) {
			return
		}
		id := r.PathValue("id")
		if err := s.Stop(r.Context(), id); err != nil {
			writeError(w, err)
			return
		}
		c, err := s.Get(r.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s.detail(c))
	})
	mux.HandleFunc("POST /api/console/conversations/{id}/close", func(w http.ResponseWriter, r *http.Request) {
		if !decode(w, r, &struct{}{}) {
			return
		}
		c, err := s.Close(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s.detail(c))
	})
	mux.HandleFunc("PUT /api/console/page", func(w http.ResponseWriter, r *http.Request) {
		var page agentcontext.PageContext
		if !decode(w, r, &page) {
			return
		}
		if err := s.SetPage(page); err != nil {
			writeError(w, invalidPage(err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"page": page})
	})
	mux.HandleFunc("GET /api/console/conversations/{id}/proposals", func(w http.ResponseWriter, r *http.Request) {
		list, err := s.Proposals(r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"proposals": list})
	})
	mux.HandleFunc("POST /api/console/conversations/{id}/proposals/{proposal}/save", func(w http.ResponseWriter, r *http.Request) {
		var input agentcontext.SaveInput
		if !decode(w, r, &input) {
			return
		}
		if err := s.SaveProposal(r.Context(), r.PathValue("id"), r.PathValue("proposal"), input); err != nil {
			writeRefusal(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"saved": true})
	})
	mux.HandleFunc("POST /api/console/conversations/{id}/proposals/{proposal}/confirm", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Revision string `json:"revision"`
		}
		if !decode(w, r, &input) {
			return
		}
		outcome, current, err := s.ConfirmProposal(r.Context(), r.PathValue("id"), r.PathValue("proposal"), input.Revision)
		if errors.Is(err, agentcontext.ErrOperationChanged) {
			// Nothing ran: the app shows the new summary and asks again.
			writeJSON(w, http.StatusConflict, map[string]any{"code": "operation_changed", "message": err.Error(), "proposal": current})
			return
		}
		if err != nil {
			writeRefusal(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"outcome": outcome})
	})
	mux.HandleFunc("POST /api/console/conversations/{id}/proposals/{proposal}/dismiss", func(w http.ResponseWriter, r *http.Request) {
		if !decode(w, r, &struct{}{}) {
			return
		}
		if err := s.DismissProposal(r.PathValue("id"), r.PathValue("proposal")); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"dismissed": true})
	})
	return mux
}

// Unavailable answers the panel's routes while no Service runs (its
// conversations did not open), as the local API answers other capabilities
// it lacks: a read is 200 {"available":false,"code":"console_unavailable"},
// and anything else 503 with the routes' error shape and that code.
func Unavailable() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			writeJSON(w, http.StatusOK, map[string]any{"available": false, "code": "console_unavailable"})
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"code": "console_unavailable",
			"message": "The session panel is unavailable: its conversations could not be opened. The daemon's log says why."})
	})
}

// decode reads one JSON value into v; an empty body leaves v as it is, so
// the routes that take nothing accept none. It answers 400 itself.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	err := d.Decode(v)
	switch err {
	case io.EOF:
		return true
	case nil:
		if d.Decode(new(any)) == io.EOF {
			return true
		}
		err = errors.New("one JSON value expected")
	}
	writeJSON(w, http.StatusBadRequest, map[string]string{"code": CodeInvalidRequest, "message": "invalid JSON request: " + err.Error()})
	return false
}

// writeError answers err with its status and a stable code.
func writeError(w http.ResponseWriter, err error) {
	status, code := classify(err)
	writeJSON(w, status, map[string]string{"code": code, "message": err.Error()})
}

// writeRefusal is writeError for a save or a confirm, where an error no
// case names is the page's own refusal of the value (its proposer's), for
// the person to read.
func writeRefusal(w http.ResponseWriter, err error) {
	status, code := classify(err)
	if status == http.StatusInternalServerError {
		status, code = http.StatusUnprocessableEntity, "refused"
	}
	writeJSON(w, status, map[string]string{"code": code, "message": err.Error()})
}

// classify is the status and code err is answered with.
func classify(err error) (int, string) {
	status, code := http.StatusInternalServerError, "internal_error"
	var bad *InvalidError
	switch {
	case errors.As(err, &bad):
		status, code = http.StatusBadRequest, bad.Code
	case errors.Is(err, ErrNotFound):
		status, code = http.StatusNotFound, "conversation_not_found"
	case errors.Is(err, ErrBusy):
		status, code = http.StatusConflict, "busy"
	case errors.Is(err, ErrClosed):
		status, code = http.StatusConflict, "closed"
	case errors.Is(err, ErrShuttingDown):
		status, code = http.StatusServiceUnavailable, "shutting_down"
	case errors.Is(err, agentcontext.ErrUnknownProposal):
		status, code = http.StatusNotFound, "proposal_not_found"
	case errors.Is(err, agentcontext.ErrNotCurrentPage):
		// The rule is the MCP server's: a proposal is saved or run only on
		// the page it was made on.
		status, code = http.StatusConflict, "not_current_page"
	case errors.Is(err, agentcontext.ErrStale):
		status, code = http.StatusConflict, "stale"
	case errors.Is(err, agentcontext.ErrNotADiff), errors.Is(err, agentcontext.ErrNotAnOperation):
		status, code = http.StatusBadRequest, "wrong_kind"
	case errors.Is(err, agentcontext.ErrNoProposer), errors.Is(err, agentcontext.ErrUnknownOperation):
		status, code = http.StatusConflict, "not_offered"
	case errors.Is(err, agentcontext.ErrUnknownConsumer):
		status, code = http.StatusConflict, "conversation_closed"
	}
	return status, code
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
