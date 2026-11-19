package uiserver

import (
	"context"
	"net/http"
	"strconv"

	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/githubreads"
	"github.com/baum-ai/baum/internal/repos"
)

// GitHubReads is the read-only GitHub answers baum's MCP tools give.
// *githubreads.Service implements it.
type GitHubReads interface {
	Threads(ctx context.Context, repository github.Repository, number int, state string) (githubreads.Threads, error)
	Timeline(ctx context.Context, repository github.Repository, number int) (githubreads.Timeline, error)
	Issue(ctx context.Context, repository github.Repository, number int) (githubreads.Issue, error)
}

// GitHubReadsHandler serves GET /api/prs/threads, /api/prs/timeline and
// /api/issue for picked repositories only.
func GitHubReadsHandler(registry *repos.Registry, reads GitHubReads) http.Handler {
	// read serves one answer about a number given as param.
	read := func(param string, answer func(ctx context.Context, repo github.Repository, number int, r *http.Request) (any, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			number, err := strconv.Atoi(r.URL.Query().Get(param))
			if err != nil || number < 1 {
				http.Error(w, "a positive "+param+" number is required", http.StatusBadRequest)
				return
			}
			if r.URL.Path == "/api/prs/threads" {
				switch r.URL.Query().Get("state") {
				case "", githubreads.Unresolved, githubreads.Resolved, githubreads.All:
				default:
					http.Error(w, "state must be unresolved, resolved or all", http.StatusBadRequest)
					return
				}
			}
			name, err := pickedRepository(r.Context(), registry, r.URL.Query().Get("repository"))
			if err != nil {
				apiError(w, err)
				return
			}
			repo, err := github.ParseRepository(name)
			if err != nil {
				apiError(w, err)
				return
			}
			out, err := answer(r.Context(), repo, number, r)
			if err != nil {
				updateFailure(w, err)
				return
			}
			writeJSON(w, out)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/prs/threads", read("pr", func(ctx context.Context, repo github.Repository, number int, r *http.Request) (any, error) {
		return reads.Threads(ctx, repo, number, r.URL.Query().Get("state"))
	}))
	mux.HandleFunc("GET /api/prs/timeline", read("pr", func(ctx context.Context, repo github.Repository, number int, _ *http.Request) (any, error) {
		return reads.Timeline(ctx, repo, number)
	}))
	mux.HandleFunc("GET /api/issue", read("number", func(ctx context.Context, repo github.Repository, number int, _ *http.Request) (any, error) {
		return reads.Issue(ctx, repo, number)
	}))
	return mux
}
