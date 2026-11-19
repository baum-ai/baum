package uiserver

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/internal/repos"
)

const (
	defaultCloneLogLimit = 30
	maxCloneLogCommits   = gitlocal.MaxLogCommits
)

var revisionParam = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// cloneRoutes serves read-only git history of a registered clone, which the
// MCP tools read, all with clone=<registered clone id> and revisions as 7 to
// 64 lower-case hex commit ids:
//
//   - GET /api/clone/log?rev=&path=&limit=&since= lists the commits reachable
//     from rev that touch any path (none means all), newest first.
//   - GET /api/clone/blame?rev=&path=&start=&end= attributes lines of a file.
//   - GET /api/clone/file?rev=&path= reads a file at rev.
//   - GET /api/clone/changed?base=&head= lists what a pull request with that
//     head on that base changes.
//
// Nothing is fetched: a commit the clone lacks is 422 commit_missing. Errors
// are {"code","message"}.
func (a LocalAPI) cloneRoutes(mux *http.ServeMux) {
	slots := make(chan struct{}, maxLocalReads)
	// serve resolves the clone, bounds the git work and opens the repo.
	serve := func(handle func(w http.ResponseWriter, r *http.Request, clone repos.Clone, repo *gitlocal.Repo) error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			clone, ok := a.cloneOf(w, r)
			if !ok {
				return
			}
			if !acquire(r.Context(), slots) {
				return
			}
			defer func() { <-slots }()
			repo, code, err := locallink.OpenClone(r.Context(), clone.Path, clone.Repository)
			if err != nil || code != "" {
				cloneFailure(w, r, code, err)
				return
			}
			defer func() { _ = repo.Close() }()
			if err := handle(w, r, clone, repo); err != nil {
				cloneFailure(w, r, "", err)
			}
		}
	}
	mux.HandleFunc("GET /api/clone/log", serve(func(w http.ResponseWriter, r *http.Request, _ repos.Clone, repo *gitlocal.Repo) error {
		q := r.URL.Query()
		rev := q.Get("rev")
		paths := q["path"]
		limit := defaultCloneLogLimit
		if raw := q.Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 {
				return errBadParameter
			}
			limit = min(n, maxCloneLogCommits)
		}
		var since time.Time
		if raw := q.Get("since"); raw != "" {
			var err error
			if since, err = time.Parse("2006-01-02", raw); err != nil {
				if since, err = time.Parse(time.RFC3339, raw); err != nil {
					return errBadParameter
				}
			}
		}
		if !revisionParam.MatchString(rev) {
			return errBadParameter
		}
		oid, err := repo.ResolveCommit(r.Context(), rev)
		if err != nil {
			return err
		}
		commits, truncated, err := repo.Log(r.Context(), oid, paths, limit, since)
		if err != nil {
			return err
		}
		out := make([]cloneCommit, 0, len(commits))
		for _, c := range commits {
			out = append(out, cloneCommit{SHA: c.SHA, Parents: c.Parents, Author: c.Author, Date: c.Date, Subject: c.Subject})
		}
		if paths == nil {
			paths = []string{}
		}
		writeJSON(w, cloneLog{Rev: oid, Paths: paths, Commits: out, Truncated: truncated})
		return nil
	}))
	mux.HandleFunc("GET /api/clone/blame", serve(func(w http.ResponseWriter, r *http.Request, _ repos.Clone, repo *gitlocal.Repo) error {
		q := r.URL.Query()
		start, startErr := strconv.Atoi(q.Get("start"))
		end, endErr := strconv.Atoi(q.Get("end"))
		if !revisionParam.MatchString(q.Get("rev")) || startErr != nil || endErr != nil {
			return errBadParameter
		}
		oid, err := repo.ResolveCommit(r.Context(), q.Get("rev"))
		if err != nil {
			return err
		}
		lines, err := repo.Blame(r.Context(), oid, q.Get("path"), start, end)
		if err != nil {
			return err
		}
		out := make([]cloneBlameLine, 0, len(lines))
		for _, l := range lines {
			out = append(out, cloneBlameLine{Line: l.Line, SHA: l.SHA, Author: l.Author, Date: l.Date, Summary: l.Summary, Text: l.Text})
		}
		writeJSON(w, cloneBlame{Rev: oid, Path: q.Get("path"), Start: start, End: end, Lines: out})
		return nil
	}))
	mux.HandleFunc("GET /api/clone/file", func(w http.ResponseWriter, r *http.Request) {
		clone, ok := a.cloneOf(w, r)
		if !ok {
			return
		}
		q := r.URL.Query()
		rev, path := q.Get("rev"), q.Get("path")
		if !revisionParam.MatchString(rev) || path == "" {
			cloneFailure(w, r, "", errBadParameter)
			return
		}
		if !acquire(r.Context(), slots) {
			return
		}
		defer func() { <-slots }()
		result, err := locallink.ReadFile(r.Context(), locallink.FileRequest{Dir: clone.Path, Repository: clone.Repository, Head: rev, Path: path})
		if err != nil {
			cloneFailure(w, r, "", err)
			return
		}
		out := cloneFile{Path: path, Rev: rev, Truncated: result.Truncated}
		if result.Available {
			out.Content = &result.Content
		} else {
			out.Code = result.Code
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("GET /api/clone/changed", serve(func(w http.ResponseWriter, r *http.Request, _ repos.Clone, repo *gitlocal.Repo) error {
		q := r.URL.Query()
		if !revisionParam.MatchString(q.Get("base")) || !revisionParam.MatchString(q.Get("head")) {
			return errBadParameter
		}
		base, err := repo.ResolveCommit(r.Context(), q.Get("base"))
		if err != nil {
			return err
		}
		head, err := repo.ResolveCommit(r.Context(), q.Get("head"))
		if err != nil {
			return err
		}
		mergeBase, files, truncated, err := repo.ChangedSince(r.Context(), base, head)
		if err != nil {
			return err
		}
		if files == nil {
			files = []string{}
		}
		writeJSON(w, cloneChanged{Base: base, Head: head, MergeBase: mergeBase, Files: files, Truncated: truncated})
		return nil
	}))
}

type cloneCommit struct {
	SHA     string   `json:"sha"`
	Parents []string `json:"parents"`
	Author  string   `json:"author"`
	Date    string   `json:"date"`
	Subject string   `json:"subject"`
}

type cloneLog struct {
	Rev       string        `json:"rev"`
	Paths     []string      `json:"paths"`
	Commits   []cloneCommit `json:"commits"`
	Truncated bool          `json:"truncated"`
}

type cloneBlameLine struct {
	Line    int    `json:"line"`
	SHA     string `json:"sha"`
	Author  string `json:"author"`
	Date    string `json:"date"`
	Summary string `json:"summary"`
	Text    string `json:"text"`
}

type cloneBlame struct {
	Rev   string           `json:"rev"`
	Path  string           `json:"path"`
	Start int              `json:"start"`
	End   int              `json:"end"`
	Lines []cloneBlameLine `json:"lines"`
}

type cloneFile struct {
	Path      string  `json:"path"`
	Rev       string  `json:"rev"`
	Content   *string `json:"content"`
	Truncated bool    `json:"truncated"`
	Code      string  `json:"code,omitempty"`
}

type cloneChanged struct {
	Base      string   `json:"base"`
	Head      string   `json:"head"`
	MergeBase string   `json:"merge_base"`
	Files     []string `json:"files"`
	Truncated bool     `json:"truncated"`
}

var errBadParameter = errors.New("a parameter is missing or malformed")

// cloneOf finds the registered clone clone= names, as cloneByID.
func (a LocalAPI) cloneOf(w http.ResponseWriter, r *http.Request) (repos.Clone, bool) {
	return a.cloneByID(w, r, r.URL.Query().Get("clone"))
}

// cloneByID finds the registered clone id names: a missing id is 400 and an
// unknown one 404.
func (a LocalAPI) cloneByID(w http.ResponseWriter, r *http.Request, id string) (repos.Clone, bool) {
	if id == "" {
		cloneError(w, http.StatusBadRequest, "invalid", "clone is required")
		return repos.Clone{}, false
	}
	clones, err := a.Registry.List(r.Context())
	if err != nil {
		cloneError(w, http.StatusInternalServerError, "failed", "the clone could not be read")
		return repos.Clone{}, false
	}
	for _, c := range clones {
		if c.ID == id {
			return c, true
		}
	}
	cloneError(w, http.StatusNotFound, "clone_unknown", "no registered clone has that id")
	return repos.Clone{}, false
}

func cloneError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	writeJSON(w, map[string]string{"code": code, "message": message})
}

// cloneFailure answers an error from a clone read, or the code OpenClone gave
// for a clone that cannot answer. Git's own messages stay out of the answer.
func cloneFailure(w http.ResponseWriter, r *http.Request, code string, err error) {
	switch {
	case code != "":
		cloneError(w, http.StatusUnprocessableEntity, code, "the clone cannot answer: "+code)
	case errors.Is(err, errBadParameter), errors.Is(err, gitlocal.ErrInvalidInput):
		cloneError(w, http.StatusBadRequest, "invalid", "a parameter is missing or malformed, or names a path or lines the revision does not hold")
	case errors.Is(err, gitlocal.ErrAmbiguous):
		cloneError(w, http.StatusBadRequest, "invalid", "the abbreviated commit id is ambiguous")
	case errors.Is(err, gitlocal.ErrObjectNotFound):
		cloneError(w, http.StatusUnprocessableEntity, "commit_missing", "the clone does not hold that commit; fetch it in the clone first")
	case errors.Is(err, gitlocal.ErrPartialClone):
		cloneError(w, http.StatusUnprocessableEntity, "partial_clone", "the clone is a partial clone that lacks the objects this needs")
	case errors.Is(err, bundle.ErrOutputTooLarge):
		cloneError(w, http.StatusUnprocessableEntity, "too_large", "the answer is too large")
	case errors.Is(err, context.Canceled) && r.Context().Err() != nil:
	default:
		cloneError(w, http.StatusInternalServerError, "failed", "the clone could not be read")
	}
}
