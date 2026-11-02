package viewserver

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/baum-ai/baum/internal/locallink"
)

// FileFunc reads one file at the document's head (review.Head) from this
// machine, for GET /api/file. It is only ever asked about a path the document
// cites, and it should stop when ctx ends.
type FileFunc func(ctx context.Context, review Review, path string) (locallink.FileResult, error)

// handleFile serves GET /api/file?path=<repository-relative path>: one file
// the current document cites, as it is at the document's head, read from the
// clone, as
//
//	{"path":"...","content":"..."|null,"truncated":false}
//
// content holds at most locallink.MaxFileBytes of the file's text, with
// invalid UTF-8 dropped, and truncated says the file goes on past it. A path
// the document does not cite (see locallink.CitedFiles) is 404 without running git,
// whatever this machine holds. A directory, a submodule, a path the head
// lacks, a binary file, a clone that cannot answer and the built-in sample
// are 200 with "content": null; the app then offers GitHub instead. Failures
// are 500 and 503 as for GET /api/patch.
func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	cache, review, ok := s.fileTarget(path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	job, err := s.shared(r.Context(), cache, path, func() (*patchJob, bool, error) {
		return s.computeFile(review, path)
	})
	if !s.answerFailure(w, r, err, "a file") {
		return
	}
	writeAnswer(w, r, job.body)
}

// fileTarget is the cache and review to answer for path, and false when the
// current document does not cite path.
func (s *Server) fileTarget(path string) (*patchCache, Review, bool) {
	if path == "" || len(path) > maxPatchPathBytes {
		return nil, Review{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, cited := s.files[path]; !cited {
		return nil, Review{}, false
	}
	return s.fileReads, s.review, true
}

func (s *Server) computeFile(review Review, path string) (*patchJob, bool, error) {
	if s.opts.File == nil || s.opts.Sample {
		body, err := encodeFile(path, locallink.FileResult{Code: locallink.CodeError})
		return &patchJob{body: body}, false, err
	}
	var result locallink.FileResult
	err := s.withSlot(func(ctx context.Context) error {
		var err error
		result, err = s.opts.File(ctx, review, path)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	body, err := encodeFile(path, result)
	return &patchJob{body: body}, result.Stable(), err
}

func encodeFile(path string, result locallink.FileResult) ([]byte, error) {
	answer := struct {
		Path      string  `json:"path"`
		Content   *string `json:"content"`
		Truncated bool    `json:"truncated"`
	}{Path: path}
	if result.Available {
		answer.Content = &result.Content
		answer.Truncated = result.Truncated
	}
	body, err := json.Marshal(answer)
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}
