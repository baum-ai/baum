package uiserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/repos"
)

// PullRequestMerger is the GitHub write of merging a pull request the user
// confirmed. *github.Client implements it.
type PullRequestMerger interface {
	MergeOptions(ctx context.Context, repository github.Repository, number int) (github.MergeOptions, error)
	MergePullRequest(ctx context.Context, repository github.Repository, number int, method, expectedHead string) error
}

// MergeHandler serves /api/prs/merge-options and /api/prs/merge for picked
// repositories only. refresh re-reads the repository's pull requests after a
// merge so the inbox drops it; its failure never turns a merge into an error.
func MergeHandler(registry *repos.Registry, merger PullRequestMerger, refresh func(context.Context, github.Repository), changed func()) http.Handler {
	picked := func(w http.ResponseWriter, r *http.Request, name string) (github.Repository, bool) {
		name, err := pickedRepository(r.Context(), registry, name)
		if err == nil {
			var repo github.Repository
			if repo, err = github.ParseRepository(name); err == nil {
				return repo, true
			}
		}
		apiError(w, err)
		return github.Repository{}, false
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/prs/merge-options", func(w http.ResponseWriter, r *http.Request) {
		number, err := strconv.Atoi(r.URL.Query().Get("pr"))
		if err != nil || number < 1 {
			http.Error(w, "a pull request number is required", http.StatusBadRequest)
			return
		}
		repo, ok := picked(w, r, r.URL.Query().Get("repository"))
		if !ok {
			return
		}
		options, err := merger.MergeOptions(r.Context(), repo, number)
		if err != nil {
			mergeFailure(w, err)
			return
		}
		writeJSON(w, options)
	})
	mux.HandleFunc("POST /api/prs/merge", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		var body struct {
			Repository string `json:"repository"`
			PR         int    `json:"pr"`
			Method     string `json:"method"`
			HeadSHA    string `json:"head_sha"`
		}
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if d.Decode(&body) != nil || d.Decode(new(any)) != io.EOF || body.PR < 1 || body.HeadSHA == "" {
			http.Error(w, "repository, pr, method and head_sha are required", http.StatusBadRequest)
			return
		}
		switch body.Method {
		case github.MergeMethodMerge, github.MergeMethodSquash, github.MergeMethodRebase:
		default:
			http.Error(w, "unknown merge method", http.StatusBadRequest)
			return
		}
		repo, ok := picked(w, r, body.Repository)
		if !ok {
			return
		}
		if err := merger.MergePullRequest(r.Context(), repo, body.PR, body.Method, body.HeadSHA); err != nil {
			mergeFailure(w, err)
			return
		}
		if refresh != nil {
			refresh(r.Context(), repo)
		}
		if changed != nil {
			changed()
		}
		writeJSON(w, map[string]bool{"merged": true})
	})
	return mux
}

// mergeFailure answers with GitHub's refusal in words the app shows as is.
func mergeFailure(w http.ResponseWriter, err error) {
	var refusal *github.MergeError
	if !errors.As(err, &refusal) {
		refusal = &github.MergeError{Code: github.MergeFailed, Message: "baum could not reach GitHub. Check your connection and try again."}
	}
	status := http.StatusBadGateway
	switch refusal.Code {
	case github.MergeHeadMoved, github.MergeConflict, github.MergeNotOpen:
		status = http.StatusConflict
	case github.MergeBlocked, github.MergeMethod:
		status = http.StatusUnprocessableEntity
	case github.MergeForbidden:
		status = http.StatusForbidden
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(refusal)
}
