package repos

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// Handler exposes the registry API for the future local UI host. It does not
// listen or authenticate: the host MUST mount it behind its authenticated,
// host/origin-checked middleware. Never mount this on the public view server.
func (r *Registry) Handler() http.Handler {
	mux := http.NewServeMux()
	fail := func(w http.ResponseWriter, err error) {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrNotFound) {
			status = http.StatusNotFound
		}
		if errors.Is(err, ErrInvalidRepository) {
			status = http.StatusBadRequest
		}
		http.Error(w, http.StatusText(status), status)
	}
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(v)
	}
	decode := func(w http.ResponseWriter, req *http.Request, v any) bool {
		req.Body = http.MaxBytesReader(w, req.Body, 16<<10)
		d := json.NewDecoder(req.Body)
		d.DisallowUnknownFields()
		if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid JSON request", http.StatusBadRequest)
			return false
		}
		return true
	}
	// decodeSettings reads the whole settings object of a PUT.
	decodeSettings := func(w http.ResponseWriter, req *http.Request) (Settings, bool) {
		req.Body = http.MaxBytesReader(w, req.Body, 16<<10)
		raw, err := io.ReadAll(req.Body)
		var fields map[string]json.RawMessage
		if err != nil || json.Unmarshal(raw, &fields) != nil {
			http.Error(w, "invalid JSON request", http.StatusBadRequest)
			return Settings{}, false
		}
		// The whole settings object is replaced, so removing the cap or the
		// automatic choice must be explicit: null, never a missing field.
		for _, name := range []string{"automatic", "daily_cap"} {
			if _, ok := fields[name]; !ok {
				http.Error(w, name+" is required (null for the default)", http.StatusBadRequest)
				return Settings{}, false
			}
		}
		req.Body = io.NopCloser(bytes.NewReader(raw))
		var s Settings
		if !decode(w, req, &s) {
			return Settings{}, false
		}
		if e := s.Validate(); e != nil {
			http.Error(w, e.Error(), 400)
			return Settings{}, false
		}
		return s, true
	}
	mux.HandleFunc("GET /api/repos", func(w http.ResponseWriter, req *http.Request) {
		list, e := r.List(req.Context())
		if e != nil {
			fail(w, e)
			return
		}
		write(w, list)
	})
	// Whether a path can be registered, before registering it: the app's
	// Repositories page offers Add only for a clone this would accept.
	mux.HandleFunc("GET /api/repos/check", func(w http.ResponseWriter, req *http.Request) {
		path := req.URL.Query().Get("path")
		if path == "" {
			http.Error(w, "path required", http.StatusBadRequest)
			return
		}
		type answer struct {
			Valid      bool   `json:"valid"`
			Repository string `json:"repository,omitempty"`
			Registered bool   `json:"registered,omitempty"`
			Reason     string `json:"reason,omitempty"`
		}
		c, e := Inspect(req.Context(), path)
		if e != nil {
			write(w, answer{Reason: e.Error()})
			return
		}
		list, e := r.List(req.Context())
		if e != nil {
			fail(w, e)
			return
		}
		registered := false
		for _, existing := range list {
			if existing.Path == c.Path {
				registered = true
			}
		}
		write(w, answer{Valid: true, Repository: c.Repository, Registered: registered})
	})
	mux.HandleFunc("POST /api/repos", func(w http.ResponseWriter, req *http.Request) {
		var input struct {
			Path     string    `json:"path"`
			Settings *Settings `json:"settings"`
		}
		if !decode(w, req, &input) {
			return
		}
		if input.Path == "" {
			http.Error(w, "path is required", 400)
			return
		}
		if input.Settings != nil {
			if e := input.Settings.Validate(); e != nil {
				http.Error(w, e.Error(), 400)
				return
			}
		}
		c, e := r.Add(req.Context(), input.Path, input.Settings)
		if e != nil {
			http.Error(w, "cannot register path: check that it is an existing git working tree with a GitHub remote", 400)
			return
		}
		write(w, c)
	})
	mux.HandleFunc("DELETE /api/repos/{id}", func(w http.ResponseWriter, req *http.Request) {
		if e := r.Remove(req.Context(), req.PathValue("id")); e != nil {
			fail(w, e)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("PUT /api/repos/{id}/settings", func(w http.ResponseWriter, req *http.Request) {
		s, ok := decodeSettings(w, req)
		if !ok {
			return
		}
		if e := r.Update(req.Context(), req.PathValue("id"), s); e != nil {
			fail(w, e)
			return
		}
		write(w, s)
	})
	// Picked repositories, with or without a local clone.
	mux.HandleFunc("GET /api/repositories", func(w http.ResponseWriter, req *http.Request) {
		list, e := r.Repositories(req.Context())
		if e != nil {
			fail(w, e)
			return
		}
		write(w, list)
	})
	mux.HandleFunc("POST /api/repositories", func(w http.ResponseWriter, req *http.Request) {
		var input struct {
			Repository string    `json:"repository"`
			Settings   *Settings `json:"settings"`
		}
		if !decode(w, req, &input) {
			return
		}
		if input.Settings != nil {
			if e := input.Settings.Validate(); e != nil {
				http.Error(w, e.Error(), 400)
				return
			}
		}
		v, e := r.Pick(req.Context(), input.Repository, input.Settings)
		if e != nil {
			fail(w, e)
			return
		}
		write(w, v)
	})
	mux.HandleFunc("DELETE /api/repositories/{owner}/{name}", func(w http.ResponseWriter, req *http.Request) {
		if e := r.Unpick(req.Context(), req.PathValue("owner")+"/"+req.PathValue("name")); e != nil {
			fail(w, e)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("PUT /api/repositories/{owner}/{name}/settings", func(w http.ResponseWriter, req *http.Request) {
		s, ok := decodeSettings(w, req)
		if !ok {
			return
		}
		if e := r.UpdateRepository(req.Context(), req.PathValue("owner")+"/"+req.PathValue("name"), s); e != nil {
			fail(w, e)
			return
		}
		write(w, s)
	})
	return mux
}
