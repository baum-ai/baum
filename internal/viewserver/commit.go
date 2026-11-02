package viewserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/baum-ai/baum/internal/locallink"
)

// CommitFunc reads one commit's message body and changed files from this
// machine, for GET /api/commit. It is only ever asked about a commit the
// sessions answer lists, and it should stop when ctx ends.
type CommitFunc func(ctx context.Context, review Review, oid string) (locallink.CommitDetail, error)

// CommitPatchFunc reads one file's patch in a commit, for
// GET /api/commit-patch. commit is CommitFunc's available answer and file one
// of its files.
type CommitPatchFunc func(ctx context.Context, review Review, commit locallink.CommitDetail, file locallink.CommitFile) (locallink.PatchResult, error)

// handleCommit serves GET /api/commit?oid=<full commit id>: the commit's
// message body and the files it changed against its first parent (the empty
// tree for a root commit), as
//
//	{"oid":"...","body":"...","files":[{"path","old_path"?,"status","additions"?,"deletions"?}]}
//
// with additions and deletions left out for a binary file. A commit this
// machine cannot read is 200 with "files": null and a "code". An oid that is
// not one of the commits GET /api/commits lists for the current document is
// 404, whatever this machine holds, so a request never names another
// revision. Failures are 500 and 503 as for GET /api/patch.
func (s *Server) handleCommit(w http.ResponseWriter, r *http.Request) {
	oid := r.URL.Query().Get("oid")
	linked, ok := s.linkedCommit(r.Context(), oid)
	if !ok {
		return
	}
	if !linked {
		http.NotFound(w, r)
		return
	}
	job, err := s.commitDetail(r.Context(), oid)
	if !s.answerFailure(w, r, err, "a commit") {
		return
	}
	writeAnswer(w, r, job.body)
}

// handleCommitPatch serves GET /api/commit-patch?oid=<commit>&path=<path>:
// one file's patch in a commit, against its first parent (the empty tree for
// a root commit), as {"patch":"..."} from its first "@@" line or
// {"patch":null}, as GET /api/patch answers. A renamed file may be named by
// either path. The oid must be one GET /api/commit answers for, and the path
// one of that commit's changed files (or a renamed file's old path), else
// 404.
func (s *Server) handleCommitPatch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	oid, path := q.Get("oid"), q.Get("path")
	if path == "" || len(path) > maxPatchPathBytes {
		http.NotFound(w, r)
		return
	}
	linked, ok := s.linkedCommit(r.Context(), oid)
	if !ok {
		return
	}
	if !linked {
		http.NotFound(w, r)
		return
	}
	answer, err := s.commitDetail(r.Context(), oid)
	if !s.answerFailure(w, r, err, "a commit") {
		return
	}
	commit := answer.detail
	file, changed := commit.File(path)
	if !commit.Available || !changed {
		http.NotFound(w, r)
		return
	}
	cache, review := s.commitTarget()
	job, err := s.shared(r.Context(), cache, "patch\x00"+oid+"\x00"+file.Path, func() (*patchJob, bool, error) {
		return s.computeCommitPatch(review, commit, file)
	})
	if !s.answerFailure(w, r, err, "a commit's patch") {
		return
	}
	writeAnswer(w, r, job.body)
}

// linkedCommit reports whether oid is one of the commits the current
// sessions answer lists. ok is false when the request ended, or the server
// stopped, before the answer.
func (s *Server) linkedCommit(ctx context.Context, oid string) (linked, ok bool) {
	if oid == "" || len(oid) > 64 {
		return false, true
	}
	result, ok := s.sessionsResult(ctx)
	if !ok {
		return false, false
	}
	if !result.Available {
		return false, true
	}
	for _, commit := range result.Commits {
		if commit.OID == oid {
			return true, true
		}
	}
	return false, true
}

func (s *Server) commitTarget() (*patchCache, Review) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.commits, s.review
}

// commitDetail is the commit's answer, computed once at a time and kept
// while it is stable.
func (s *Server) commitDetail(ctx context.Context, oid string) (*patchJob, error) {
	cache, review := s.commitTarget()
	return s.shared(ctx, cache, "commit\x00"+oid, func() (*patchJob, bool, error) {
		return s.computeCommit(review, oid)
	})
}

func (s *Server) computeCommit(review Review, oid string) (*patchJob, bool, error) {
	if s.opts.Commit == nil || s.opts.Sample {
		job, err := encodeCommit(locallink.CommitDetail{OID: oid, Code: locallink.CodeError})
		return job, false, err
	}
	var detail locallink.CommitDetail
	err := s.withSlot(func(ctx context.Context) error {
		var err error
		detail, err = s.opts.Commit(ctx, review, oid)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	job, err := encodeCommit(detail)
	return job, detail.Stable(), err
}

func (s *Server) computeCommitPatch(review Review, commit locallink.CommitDetail, file locallink.CommitFile) (*patchJob, bool, error) {
	if s.opts.CommitPatch == nil || s.opts.Sample {
		body, err := encodePatch(locallink.PatchResult{Code: locallink.CodeError})
		return &patchJob{body: body}, false, err
	}
	var result locallink.PatchResult
	err := s.withSlot(func(ctx context.Context) error {
		var err error
		result, err = s.opts.CommitPatch(ctx, review, commit, file)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	body, err := encodePatch(result)
	return &patchJob{body: body}, result.Stable(), err
}

func encodeCommit(detail locallink.CommitDetail) (*patchJob, error) {
	answer := struct {
		OID   string                 `json:"oid"`
		Body  string                 `json:"body"`
		Files []locallink.CommitFile `json:"files"`
		Code  string                 `json:"code,omitempty"`
	}{OID: detail.OID, Body: detail.Body, Files: detail.Files, Code: detail.Code}
	if !detail.Available {
		answer.Body, answer.Files = "", nil
		if answer.Code == "" {
			answer.Code = locallink.CodeError
		}
	} else if answer.Files == nil {
		answer.Files = []locallink.CommitFile{}
	}
	body, err := json.Marshal(answer)
	if err != nil {
		return nil, err
	}
	return &patchJob{body: append(body, '\n'), detail: detail}, nil
}

// answerFailure writes the response for a failed computation and reports
// whether there was none, so the caller goes on to write the answer.
func (s *Server) answerFailure(w http.ResponseWriter, r *http.Request, err error, what string) bool {
	switch {
	case err == nil:
		return true
	case r.Context().Err() != nil:
	case errors.Is(err, errStopping), s.life.Err() != nil:
		// An empty 200 would read as an answer that does not exist and be kept.
		http.Error(w, "the server is stopping", http.StatusServiceUnavailable)
	default:
		_, _ = fmt.Fprintf(s.opts.Warn, "warning: cannot read %s: %v\n", what, err)
		http.Error(w, "cannot read this answer", http.StatusInternalServerError)
	}
	return false
}

func writeAnswer(w http.ResponseWriter, r *http.Request, body []byte) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}
