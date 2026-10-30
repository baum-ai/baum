package repos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/github"
)

// CurrentName is the lower-case owner/name a github.com repository has now;
// the daemon asks GitHub, which answers an earlier name it forwards with the
// current one. The registry itself contacts nothing.
type CurrentName func(ctx context.Context, repository string) (string, error)

// Handler exposes the registry API for the future local UI host. It does not
// listen or authenticate: the host MUST mount it behind its authenticated,
// host/origin-checked middleware. Never mount this on the public view server.
func (r *Registry) Handler() http.Handler { return r.HandlerWith(nil) }

// HandlerWith is Handler, also registering a folder for a repository when its
// remote uses an earlier name that current reports GitHub forwards to it.
func (r *Registry) HandlerWith(current CurrentName) http.Handler {
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
	writeStatus := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	write := func(w http.ResponseWriter, v any) { writeStatus(w, http.StatusOK, v) }
	decode := func(w http.ResponseWriter, req *http.Request, v any) bool {
		req.Body = http.MaxBytesReader(w, req.Body, 16<<10)
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			http.Error(w, "invalid JSON request", http.StatusBadRequest)
			return false
		}
		d := json.NewDecoder(bytes.NewReader(withoutDailyCap(raw)))
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
		// The whole settings object is replaced, so removing the automatic
		// choice must be explicit: null, never a missing field.
		if _, ok := fields["automatic"]; !ok {
			http.Error(w, "automatic is required (null for the default)", http.StatusBadRequest)
			return Settings{}, false
		}
		req.Body = io.NopCloser(bytes.NewReader(raw))
		var s Settings
		if !decode(w, req, &s) {
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
			Path       string    `json:"path"`
			Repository string    `json:"repository"`
			Settings   *Settings `json:"settings"`
		}
		if !decode(w, req, &input) {
			return
		}
		if input.Path == "" {
			http.Error(w, "path is required", 400)
			return
		}
		var c Clone
		var e error
		if input.Repository != "" {
			c, e = r.AddFor(req.Context(), input.Path, input.Repository, input.Settings)
			var mismatch *RemoteMismatchError
			if errors.As(e, &mismatch) && current != nil {
				switch alias, unchecked := forwarded(req.Context(), mismatch, current); {
				case alias != "":
					c, e = r.AddForwarded(req.Context(), input.Path, mismatch.Repository, alias, input.Settings)
				case unchecked:
					mismatch.Unchecked = true
				}
			}
		} else {
			c, e = r.Add(req.Context(), input.Path, input.Settings)
		}
		if e != nil {
			writeStatus(w, http.StatusBadRequest, map[string]string{"message": registerError(e)})
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

// forwarded is the first of a mismatch's remotes, origin's first, that GitHub
// forwards to the requested repository, "" when none does; unchecked when
// GitHub could not be asked about one.
func forwarded(ctx context.Context, mismatch *RemoteMismatchError, current CurrentName) (alias string, unchecked bool) {
	for _, remote := range mismatch.Remotes {
		if gh, e := github.ParseRepository(remote); e != nil || gh.Host != config.GitHubHost {
			continue
		}
		lookup, cancel := context.WithTimeout(ctx, 10*time.Second)
		name, e := current(lookup, remote)
		cancel()
		switch {
		case e == nil && name == mismatch.Repository:
			return remote, false
		case e != nil && !errors.Is(e, github.ErrNotFound):
			unchecked = true
		}
	}
	return "", unchecked
}

// registerError is what the person is told when a folder cannot be
// registered.
func registerError(e error) string {
	var mismatch *RemoteMismatchError
	switch {
	case errors.Is(e, ErrNotWorkingTree):
		return "This folder does not exist or is not a git working tree."
	case errors.As(e, &mismatch) && mismatch.Unchecked:
		return fmt.Sprintf("This folder is a clone of %s, not %s. baum could not check with GitHub whether that is an earlier name of %s; check the GitHub connection and try again.", strings.Join(mismatch.Remotes, ", "), mismatch.Repository, mismatch.Repository)
	case errors.As(e, &mismatch) && len(mismatch.Remotes) > 0:
		return fmt.Sprintf("This folder is a clone of %s, not %s. Choose a clone of %s.", strings.Join(mismatch.Remotes, ", "), mismatch.Repository, mismatch.Repository)
	case errors.As(e, &mismatch):
		return fmt.Sprintf("This folder has no GitHub remote. Choose a clone of %s.", mismatch.Repository)
	}
	return "cannot register path: check that it is an existing git working tree with a GitHub remote"
}

// withoutDailyCap drops the daily_cap an older app still sends, in a request
// or in its settings object: baum no longer has a spending cap, and such an
// app is not refused for it. Anything that is not a JSON object is left for
// the strict decoder to refuse.
func withoutDailyCap(raw []byte) []byte {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return raw
	}
	_, found := fields["daily_cap"]
	delete(fields, "daily_cap")
	var settings map[string]json.RawMessage
	if json.Unmarshal(fields["settings"], &settings) == nil && settings != nil {
		if _, ok := settings["daily_cap"]; ok {
			delete(settings, "daily_cap")
			fields["settings"], _ = json.Marshal(settings)
			found = true
		}
	}
	if !found {
		return raw
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return raw
	}
	return out

}
