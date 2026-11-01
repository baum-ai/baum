package viewserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/locallink"
)

// patchTimeout bounds one patch computation, which outlives the request that
// started it, up to the server's life.
const patchTimeout = time.Minute

// maxPatchPathBytes bounds the path a request may name; no cited path is
// longer than a file system allows.
const maxPatchPathBytes = 4096

// maxConcurrentPatches bounds the git reads GET /api/patch runs at once, so a
// page that opens many excerpts does not start a process for each.
const maxConcurrentPatches = 4

// PatchFunc reads the patch of one file of the document's pull request from
// this machine. It may run twice at once for the same file when the document
// changes during a computation, and it should stop when ctx ends.
type PatchFunc func(ctx context.Context, review Review, path string) (locallink.PatchResult, error)

// patchCache holds the answers for one document, by path (or, for the
// commits' answers, by commit and path). A document that is
// rewritten gets a new cache, so nothing answered for the old one is served.
type patchCache struct {
	mu   sync.Mutex
	jobs map[string]*patchJob
}

// patchJob is one computation of a file's patch, shared by every request for
// the file while it runs and, when the answer is stable, afterwards.
type patchJob struct {
	done chan struct{}
	body []byte
	// detail is the commit a GET /api/commit answer is about.
	detail locallink.CommitDetail
	err    error
}

// hunkPaths reads the paths the document's cited hunks are in
// (index.hunks[].path). A document that does not parse has none.
func hunkPaths(document []byte) map[string]struct{} {
	var parsed struct {
		Index struct {
			Hunks []struct {
				Path string `json:"path"`
			} `json:"hunks"`
		} `json:"index"`
	}
	paths := map[string]struct{}{}
	if json.Unmarshal(document, &parsed) != nil {
		return paths
	}
	for _, hunk := range parsed.Index.Hunks {
		if hunk.Path != "" {
			paths[hunk.Path] = struct{}{}
		}
	}
	return paths
}

// handlePatch serves GET /api/patch?path=<repository-relative path>: the patch
// of one file the current document cites, between the document's merge base
// (its base when it records none) and its head, read from the clone containing
// the server's working directory. A renamed file is diffed with its old name,
// as the hosted service does.
//
// A path that is not the path of a hunk of the current document is 404,
// whatever this machine holds. Every other outcome is 200 with {"patch":"..."}
// (the file's diff from its first "@@" line, without git's header lines) or
// {"patch":null}: a binary file, a submodule, a path the range does not
// change, commits or objects the clone lacks, a clone that is not the
// document's repository, a patch over locallink.MaxPatchBytes. The viewer
// treats null as no excerpt and keeps its link to GitHub. A failure that may
// pass, such as a git command that fails or runs out of time, is 500, and a
// server that is stopping is 503, so the viewer tries again the next time the
// excerpt is opened.
func (s *Server) handlePatch(w http.ResponseWriter, r *http.Request) {
	file := r.URL.Query().Get("path")
	cache, review, ok := s.patchTarget(file)
	if !ok {
		http.NotFound(w, r)
		return
	}
	body, err := s.patch(r.Context(), cache, review, file)
	switch {
	case r.Context().Err() != nil:
		return
	case errors.Is(err, errStopping), err != nil && s.life.Err() != nil:
		// An empty 200 would read as a patch that does not exist and be kept.
		http.Error(w, "the server is stopping", http.StatusServiceUnavailable)
		return
	case err != nil:
		_, _ = fmt.Fprintf(s.opts.Warn, "warning: cannot read a patch: %v\n", err)
		http.Error(w, "cannot read this patch", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

// patchTarget is the cache and review to answer for file, and false when the
// current document does not cite file.
func (s *Server) patchTarget(file string) (*patchCache, Review, bool) {
	if file == "" || len(file) > maxPatchPathBytes {
		return nil, Review{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, cited := s.paths[file]; !cited {
		return nil, Review{}, false
	}
	return s.patches, s.review, true
}

func encodePatch(result locallink.PatchResult) ([]byte, error) {
	var patch *string
	if result.Available {
		patch = &result.Patch
	}
	body, err := json.Marshal(struct {
		Patch *string `json:"patch"`
	}{patch})
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

// patch answers for one file. Each file is computed once at a time, apart from
// the others; an answer that can change when the clone is fixed is not kept,
// and neither is a failure.
func (s *Server) patch(ctx context.Context, cache *patchCache, review Review, file string) ([]byte, error) {
	job, err := s.shared(ctx, cache, file, func() (*patchJob, bool, error) {
		body, keep, err := s.computePatch(review, file)
		return &patchJob{body: body}, keep, err
	})
	if err != nil {
		return nil, err
	}
	return job.body, nil
}

// shared runs compute for key in cache once at a time: requests that arrive
// while it runs wait for it, and its answer is kept for later ones when
// compute reports keep. A failure is never kept. The computation outlives the
// request that started it, up to the server's life.
func (s *Server) shared(ctx context.Context, cache *patchCache, key string, compute func() (*patchJob, bool, error)) (*patchJob, error) {
	cache.mu.Lock()
	job := cache.jobs[key]
	start := job == nil
	if start {
		s.sessionsMu.Lock()
		closed := s.sessionsClosed
		if !closed {
			s.compute.Add(1)
		}
		s.sessionsMu.Unlock()
		if closed {
			cache.mu.Unlock()
			return nil, errStopping
		}
		job = &patchJob{done: make(chan struct{})}
		cache.jobs[key] = job
	}
	cache.mu.Unlock()
	if start {
		go func() {
			defer s.compute.Done()
			answer, keep, err := compute()
			if err == nil && answer != nil {
				job.body, job.detail = answer.body, answer.detail
			} else if err == nil {
				err = errors.New("no answer")
			}
			job.err = err
			if !keep || err != nil {
				cache.mu.Lock()
				if cache.jobs[key] == job {
					delete(cache.jobs, key)
				}
				cache.mu.Unlock()
			}
			close(job.done)
		}()
	}
	select {
	case <-job.done:
		if job.err != nil {
			return nil, job.err
		}
		return job, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.done:
		return nil, errStopping
	}
}

// withSlot runs read with one of the maxConcurrentPatches slots and a context
// bounded by patchTimeout and the server's life.
func (s *Server) withSlot(read func(ctx context.Context) error) error {
	ctx, cancel := context.WithTimeout(s.life, patchTimeout)
	defer cancel()
	select {
	case s.patchSlots <- struct{}{}:
		defer func() { <-s.patchSlots }()
	case <-ctx.Done():
		if s.life.Err() != nil {
			return errStopping
		}
		return ctx.Err()
	}
	return read(ctx)
}

func (s *Server) computePatch(review Review, file string) ([]byte, bool, error) {
	if s.opts.Patch == nil || s.opts.Sample {
		body, err := encodePatch(locallink.PatchResult{Code: locallink.CodeError})
		return body, false, err
	}
	var result locallink.PatchResult
	err := s.withSlot(func(ctx context.Context) error {
		var err error
		result, err = s.opts.Patch(ctx, review, file)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	body, err := encodePatch(result)
	return body, result.Stable(), err
}
