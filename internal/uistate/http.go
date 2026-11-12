package uistate

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// Handler is mounted only behind the daemon's Host, authentication and CSRF boundary.
func (s *Store) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/ui-state/{kind}", s.get)
	mux.HandleFunc("PUT /api/ui-state/{kind}", s.put)
	mux.HandleFunc("POST /api/ui-state/review/comments", s.addComment)
	return mux
}

// addComment appends one comment to a pull request's pending review:
// {repository, pr, body, target?, place?, head?}. It answers 201 with the
// comment's id and the record's new revision, never the record.
func (s *Store) addComment(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	var input struct {
		Repository string `json:"repository"`
		PR         int    `json:"pr"`
		ReviewComment
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || input.Repository == "" || input.PR < 1 {
		respond(w, http.StatusBadRequest, map[string]string{"code": "invalid_ui_state", "message": "A comment names its pull request (repository and pr) and has a body."})
		return
	}
	id, record, err := s.AddReviewComment(ReviewKey(input.Repository, input.PR), input.ReviewComment)
	if errors.Is(err, ErrInvalid) {
		respond(w, http.StatusBadRequest, map[string]string{"code": "invalid_ui_state", "message": strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": ")})
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusCreated, struct {
		ID       string `json:"id"`
		Revision int    `json:"revision"`
		Message  string `json:"message"`
	}{id, record.Revision, "Added the comment to your pending review."})
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
