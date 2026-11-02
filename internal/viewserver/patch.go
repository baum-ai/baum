package viewserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
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

// VersionsFunc reads the document of another saved version of the served
// document's pull request. A version that does not exist is ErrNoVersion.
type VersionsFunc func(ctx context.Context, version int) ([]byte, error)

// ErrNoVersion is the error a VersionsFunc returns for a version that does
// not exist.
var ErrNoVersion = errors.New("no such review version")

// errNotCited is a patch request the server does not answer: 404.
var errNotCited = errors.New("not a cited path of that version")

// patchCache holds the answers for one document, by the range and path they
// were read for, so versions of the pull request that compare the same
// commits share them. A document that is rewritten gets a new cache, so
// nothing answered for the old one is served. Commit and file reads
// use separate caches with the same sharing mechanism.
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

// patchVersion is what /api/patch reads of a saved version: the commits it
// compared and the paths of its cited hunks.
type patchVersion struct {
	review Review
	paths  map[string]struct{}
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

// handlePatch serves GET /api/patch?path=<repository-relative path>&version=N:
// the patch of one file a saved version of the document's pull request
// cites, between that version's merge base (its base when it records none)
// and its head, read from the clone containing the server's working
// directory. Without version, or with the served document's own, it is the
// served document; another version is read with Options.Versions. A renamed
// file is diffed with its old name, as the hosted service does.
//
// A path that is not the path of a hunk of that version is 404, whatever
// this machine holds, and so is a version that is not a positive number, does
// not exist or is of another pull request. Every other outcome is 200 with
// {"patch":"..."} (the file's diff from its first "@@" line, without git's
// header lines, masked as the stored hunks are) or {"patch":null}: a
// binary file, a submodule, a path the range
// does not change, commits or objects the clone lacks, a clone that is not
// the document's repository, a patch over locallink.MaxPatchBytes. The viewer
// treats null as no excerpt and keeps its link to GitHub. A failure that may
// pass, such as a git command that fails or runs out of time, or a version
// that cannot be read, is 500, and a server that is stopping is 503, so the
// viewer tries again the next time the excerpt is opened.
func (s *Server) handlePatch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	file := query.Get("path")
	target, err := s.patchTarget(r.Context(), file, query.Get("version"))
	switch {
	case r.Context().Err() != nil:
		return
	case errors.Is(err, errNotCited):
		http.NotFound(w, r)
		return
	case err != nil:
		_, _ = fmt.Fprintf(s.opts.Warn, "warning: cannot read a review version: %v\n", err)
		http.Error(w, "cannot read this version", http.StatusInternalServerError)
		return
	}
	body, err := s.patch(r.Context(), target.cache, target.review, file)
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

// patchTarget is what a patch request is answered from: the served
// document's cache and the review of the version asked for.
type patchTarget struct {
	cache  *patchCache
	review Review
}

// patchTarget finds the version a request names and checks that it cites
// file; errNotCited when it does not, or there is no such version.
func (s *Server) patchTarget(ctx context.Context, file, version string) (patchTarget, error) {
	if file == "" || len(file) > maxPatchPathBytes {
		return patchTarget{}, errNotCited
	}
	s.mu.RLock()
	target := patchTarget{cache: s.patches, review: s.review}
	paths, n := s.paths, s.review.Version
	if version != "" {
		parsed, err := strconv.Atoi(version)
		if err != nil || parsed <= 0 || strconv.Itoa(parsed) != version {
			s.mu.RUnlock()
			return patchTarget{}, errNotCited
		}
		n = parsed
	}
	saved := s.versions[n]
	s.mu.RUnlock()
	if n != target.review.Version {
		if saved == nil {
			var err error
			if saved, err = s.loadVersion(ctx, target.review, n); err != nil {
				return patchTarget{}, err
			}
			s.mu.Lock()
			if s.patches == target.cache {
				if s.versions == nil {
					s.versions = map[int]*patchVersion{}
				}
				s.versions[n] = saved
			}
			s.mu.Unlock()
		}
		target.review, paths = saved.review, saved.paths
	}
	if _, cited := paths[file]; !cited {
		return patchTarget{}, errNotCited
	}
	return target, nil
}

// loadVersion reads saved version n of the pull request current reviews. A
// version that is missing, too large or of another pull request is
// errNotCited.
func (s *Server) loadVersion(ctx context.Context, current Review, n int) (*patchVersion, error) {
	if s.opts.Versions == nil || s.opts.Sample {
		return nil, errNotCited
	}
	document, err := s.opts.Versions(ctx, n)
	if errors.Is(err, ErrNoVersion) {
		return nil, errNotCited
	}
	if err != nil {
		return nil, err
	}
	review := parseReview(document)
	if len(document) > MaxDocumentBytes || review.Version != n || review.PullRequest != current.PullRequest ||
		review.Repository == "" || !strings.EqualFold(review.Repository, current.Repository) {
		return nil, errNotCited
	}
	return &patchVersion{review: review, paths: hunkPaths(document)}, nil
}

// patchKey names a file's patch by the commits it is read between, which
// never change.
func patchKey(review Review, file string) string {
	start := review.MergeBase
	if start == "" {
		start = review.Base
	}
	return start + "\x00" + review.Head + "\x00" + file
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

// patch answers for one file of a version. Each file and range is computed
// once at a time, apart from the others, so versions that compare the same
// commits share it; an answer that can change when the clone is fixed is not
// kept, and neither is a failure.
func (s *Server) patch(ctx context.Context, cache *patchCache, review Review, file string) ([]byte, error) {
	job, err := s.shared(ctx, cache, patchKey(review, file), func() (*patchJob, bool, error) {
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
