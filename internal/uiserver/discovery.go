package uiserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/repos"
)

// DiscoveryCache is shared with the review index. Cached must never contact
// GitHub; polling runs in the background or an authenticated same-origin mutation.
// A refresh the person asks for polls now, without waiting for the backoff.
type DiscoveryCache interface {
	Cached(context.Context, github.Repository) (discovery.Snapshot, error)
	PollNow(context.Context, github.Repository) (discovery.Snapshot, error)
}

func DiscoveryHandler(registry *repos.Registry, service DiscoveryCache, changed func()) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/prs", func(w http.ResponseWriter, r *http.Request) {
		list, err := registry.Repositories(r.Context())
		if err != nil {
			apiError(w, err)
			return
		}
		seen := map[string]bool{}
		out := []discovery.Snapshot{}
		for _, picked := range list {
			if seen[picked.Repository] {
				continue
			}
			seen[picked.Repository] = true
			repo, err := github.ParseRepository(picked.Repository)
			if err != nil {
				apiError(w, err)
				return
			}
			snapshot, err := service.Cached(r.Context(), repo)
			if err != nil {
				snapshot.Repository = repo.String()
				snapshot.Stale = true
				snapshot.Error = "cache_unavailable"
			}
			out = append(out, snapshot)
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("POST /api/prs/refresh", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		var body struct {
			Clone      string `json:"clone"`
			Repository string `json:"repository"`
		}
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if d.Decode(&body) != nil || d.Decode(new(any)) != io.EOF || (body.Clone == "") == (body.Repository == "") {
			http.Error(w, "exactly one of clone or repository is required", http.StatusBadRequest)
			return
		}
		var name string
		var err error
		if body.Clone != "" {
			name, err = RegisteredRepository(r.Context(), registry, body.Clone)
		} else {
			name, err = pickedRepository(r.Context(), registry, body.Repository)
		}
		if err != nil {
			apiError(w, err)
			return
		}
		repo, err := github.ParseRepository(name)
		if err != nil {
			apiError(w, err)
			return
		}
		snapshot, err := service.PollNow(r.Context(), repo)
		if err != nil {
			snapshot.Repository = repo.String()
			snapshot.Stale = true
			if snapshot.Error == "" {
				snapshot.Error = "discovery_unavailable"
			}
		}
		if changed != nil {
			changed()
		}
		writeJSON(w, snapshot)
	})
	return mux
}
