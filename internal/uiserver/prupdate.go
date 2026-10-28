package uiserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/prupdate"
	"github.com/baum-ai/baum/internal/repos"
)

// PullRequestUpdater is the other GitHub writes the local app makes:
// updating a pull request's branch and re-running its failed workflow runs,
// each one the user confirmed, and resolving a review thread the user chose.
// prupdate.Service implements it.
type PullRequestUpdater interface {
	UpdateOptions(ctx context.Context, repository github.Repository, number int) (prupdate.UpdateOptions, error)
	UpdateBranch(ctx context.Context, repository github.Repository, number int, expectedHead string) error
	RerunOptions(ctx context.Context, repository github.Repository, number int) (prupdate.RerunOptions, error)
	RerunFailedChecks(ctx context.Context, repository github.Repository, number int, expectedHead string) (prupdate.RerunResult, error)
	ReviewThreads(ctx context.Context, repository github.Repository, number, unresolved int) (prupdate.Threads, error)
	ResolveThread(ctx context.Context, repository github.Repository, number int, id string) error
	FailingChecks(ctx context.Context, repository github.Repository, number int, head string) (prupdate.Checks, error)
}

// UpdateHandler serves /api/prs/update-branch(-options),
// /api/prs/rerun-checks(-options), /api/prs/review-threads,
// /api/prs/resolve-thread and /api/prs/failing-checks for picked repositories
// only. refresh
// re-reads the repository's pull requests after a write so the inbox shows the
// new state; its failure never turns a write into an error.
func UpdateHandler(registry *repos.Registry, updater PullRequestUpdater, refresh func(context.Context, github.Repository), changed func()) http.Handler {
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
	// options serves a read of what a write would do.
	options := func(read func(context.Context, github.Repository, int, url.Values) (any, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			number, err := strconv.Atoi(r.URL.Query().Get("pr"))
			if err != nil || number < 1 {
				http.Error(w, "a pull request number is required", http.StatusBadRequest)
				return
			}
			repo, ok := picked(w, r, r.URL.Query().Get("repository"))
			if !ok {
				return
			}
			answer, err := read(r.Context(), repo, number, r.URL.Query())
			if err != nil {
				updateFailure(w, err)
				return
			}
			writeJSON(w, answer)
		}
	}
	// written re-reads the repository after a write that succeeded.
	written := func(ctx context.Context, repo github.Repository) {
		if refresh != nil {
			refresh(ctx, repo)
		}
		if changed != nil {
			changed()
		}
	}
	// write serves a confirmed write against the head commit the user saw.
	write := func(do func(context.Context, github.Repository, int, string) (any, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, 4096)
			var body struct {
				Repository string `json:"repository"`
				PR         int    `json:"pr"`
				HeadSHA    string `json:"head_sha"`
			}
			d := json.NewDecoder(r.Body)
			d.DisallowUnknownFields()
			if d.Decode(&body) != nil || d.Decode(new(any)) != io.EOF || body.PR < 1 || body.HeadSHA == "" {
				http.Error(w, "repository, pr and head_sha are required", http.StatusBadRequest)
				return
			}
			repo, ok := picked(w, r, body.Repository)
			if !ok {
				return
			}
			answer, err := do(r.Context(), repo, body.PR, body.HeadSHA)
			if err != nil {
				updateFailure(w, err)
				return
			}
			written(r.Context(), repo)
			writeJSON(w, answer)
		}
	}
	mux := http.NewServeMux()
	// review-threads takes discovery's unresolved count, when the app knows
	// it, so a cached list that no longer matches is read again.
	mux.HandleFunc("GET /api/prs/review-threads", options(func(ctx context.Context, repo github.Repository, number int, query url.Values) (any, error) {
		unresolved, err := strconv.Atoi(query.Get("unresolved"))
		if err != nil {
			unresolved = -1
		}
		return updater.ReviewThreads(ctx, repo, number, unresolved)
	}))
	// failing-checks takes the head commit the app shows, so the list is of
	// the checks it counts as failing.
	mux.HandleFunc("GET /api/prs/failing-checks", options(func(ctx context.Context, repo github.Repository, number int, query url.Values) (any, error) {
		return updater.FailingChecks(ctx, repo, number, query.Get("head_sha"))
	}))
	mux.HandleFunc("POST /api/prs/resolve-thread", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		var body struct {
			Repository string `json:"repository"`
			PR         int    `json:"pr"`
			Thread     string `json:"thread"`
		}
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if d.Decode(&body) != nil || d.Decode(new(any)) != io.EOF || body.PR < 1 || body.Thread == "" {
			http.Error(w, "repository, pr and thread are required", http.StatusBadRequest)
			return
		}
		repo, ok := picked(w, r, body.Repository)
		if !ok {
			return
		}
		if err := updater.ResolveThread(r.Context(), repo, body.PR, body.Thread); err != nil {
			updateFailure(w, err)
			return
		}
		written(r.Context(), repo)
		writeJSON(w, map[string]bool{"resolved": true})
	})
	mux.HandleFunc("GET /api/prs/update-branch-options", options(func(ctx context.Context, repo github.Repository, number int, _ url.Values) (any, error) {
		return updater.UpdateOptions(ctx, repo, number)
	}))
	mux.HandleFunc("POST /api/prs/update-branch", write(func(ctx context.Context, repo github.Repository, number int, head string) (any, error) {
		return map[string]bool{"updating": true}, updater.UpdateBranch(ctx, repo, number, head)
	}))
	mux.HandleFunc("GET /api/prs/rerun-checks-options", options(func(ctx context.Context, repo github.Repository, number int, _ url.Values) (any, error) {
		return updater.RerunOptions(ctx, repo, number)
	}))
	mux.HandleFunc("POST /api/prs/rerun-checks", write(func(ctx context.Context, repo github.Repository, number int, head string) (any, error) {
		return updater.RerunFailedChecks(ctx, repo, number, head)
	}))
	return mux
}

// updateFailure answers with the refusal in words the app shows as is.
func updateFailure(w http.ResponseWriter, err error) {
	refusal := prupdate.AsError(err)
	status := http.StatusBadGateway
	switch refusal.Code {
	case prupdate.HeadMoved, prupdate.Conflict, prupdate.NotOpen, prupdate.UpToDate:
		status = http.StatusConflict
	case prupdate.Blocked, prupdate.NoRuns, prupdate.Fork:
		status = http.StatusUnprocessableEntity
	case prupdate.Forbidden:
		status = http.StatusForbidden
	case prupdate.NotFound:
		status = http.StatusNotFound
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(refusal)
}
