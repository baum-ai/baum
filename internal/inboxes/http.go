package inboxes

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// API serves /api/inboxes. It has no authentication; the daemon mounts it
// behind its authenticated host.
type API struct {
	Store *Store
	// Changed is called after every successful save.
	Changed func()
}

type listResponse struct {
	Available bool `json:"available"`
	State
}

type putRequest struct {
	Revision *int    `json:"revision"`
	Inboxes  []Inbox `json:"inboxes"`
}

// Handler routes /api/inboxes.
func (h *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/inboxes", h.get)
	mux.HandleFunc("PUT /api/inboxes", h.put)
	mux.HandleFunc("/api/inboxes", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", "GET, PUT")
		failure(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET or PUT.")
	})
	return mux
}

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func failure(w http.ResponseWriter, status int, code, message string) {
	respond(w, status, map[string]string{"code": code, "message": message})
}

func fail(w http.ResponseWriter, err error) {
	var refused *InvalidError
	var stale *StaleError
	var unreadable *UnreadableError
	switch {
	case errors.As(err, &refused):
		failure(w, http.StatusBadRequest, refused.Code, refused.Message)
	case errors.As(err, &stale):
		respond(w, http.StatusConflict, struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			Available bool   `json:"available"`
			State
		}{CodeStale, stale.Error(), true, stale.Current})
	case errors.As(err, &unreadable):
		failure(w, http.StatusInternalServerError, CodeUnreadable, "The stored inbox state cannot be read and was left untouched.")
	default:
		failure(w, http.StatusInternalServerError, "internal", "The inboxes could not be saved.")
	}
}

func (h *API) get(w http.ResponseWriter, _ *http.Request) {
	state, err := h.Store.Read()
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, listResponse{true, state})
}

func (h *API) put(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var req putRequest
	if err := decoder.Decode(&req); err != nil || req.Revision == nil {
		failure(w, http.StatusBadRequest, CodeInvalidRequest, "Send a JSON body with the revision and the inboxes.")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		failure(w, http.StatusBadRequest, CodeInvalidRequest, "Send one JSON request.")
		return
	}
	state, err := h.Store.Replace(*req.Revision, req.Inboxes)
	if err != nil {
		fail(w, err)
		return
	}
	if h.Changed != nil {
		h.Changed()
	}
	respond(w, http.StatusOK, listResponse{true, state})
}
