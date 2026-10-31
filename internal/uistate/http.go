package uistate

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// Handler is mounted only behind the daemon's Host, authentication and CSRF boundary.
func (s *Store) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/ui-state/{kind}", s.get)
	mux.HandleFunc("PUT /api/ui-state/{kind}", s.put)
	return mux
}

type response struct {
	Revision int             `json:"revision"`
	Value    json.RawMessage `json:"value"`
}

func public(record Record) response { return response{record.Revision, record.Value} }

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, err error) {
	var stale *StaleError
	switch {
	case errors.As(err, &stale):
		respond(w, http.StatusConflict, struct {
			Code string `json:"code"`
			response
		}{"stale_ui_state", public(stale.Current)})
	case errors.Is(err, ErrInvalid):
		respond(w, http.StatusBadRequest, map[string]string{"code": "invalid_ui_state", "message": "The saved state request is invalid."})
	default:
		respond(w, http.StatusInternalServerError, map[string]string{"code": "ui_state_unreadable", "message": "Your saved state could not be read or saved. Existing data was left untouched."})
	}
}
func (s *Store) get(w http.ResponseWriter, r *http.Request) {
	record, err := s.Read(r.PathValue("kind"), r.URL.Query().Get("key"))
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, public(record))
}
func (s *Store) put(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
	var input struct {
		Revision *int            `json:"revision"`
		Mutation string          `json:"mutation_id"`
		Value    json.RawMessage `json:"value"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || input.Revision == nil {
		fail(w, ErrInvalid)
		return
	}
	record, err := s.Replace(r.PathValue("kind"), r.URL.Query().Get("key"), *input.Revision, input.Mutation, input.Value)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, public(record))
}
