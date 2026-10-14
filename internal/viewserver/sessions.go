package viewserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/locallink"
)

// sessionsTimeout bounds one computation of /api/sessions. A store or clone
// that cannot be read in this time is unavailable, not a request that never
// answers.
const sessionsTimeout = 2 * time.Minute

// failureTTL is how long an answer that failed (an error or a timeout) is
// kept, so a viewer reloading in a loop does not start the same slow
// computation again each time. Other unavailable answers are not kept: they
// are cheap to reach and a person can fix them, for example by fetching.
var failureTTL = 30 * time.Second

// computeGrace is how long Serve waits for a computation to wind down after
// the server is told to stop.
var computeGrace = 10 * time.Second

// sessionsEntry is one computation of the sessions answer, shared by every
// request that arrives while it runs and, when the answer is available, by
// every later request for the same pull request.
type sessionsEntry struct {
	key    string
	done   chan struct{}
	result locallink.Result
	// expires is when a failed answer is worked out again; zero for any
	// other, which is kept until the document's pull request changes.
	expires time.Time
	// transcripts are the session transcripts served from this answer, by
	// session id, built once each.
	transcriptsMu sync.Mutex
	transcripts   map[string]*transcriptJob
}

// parseReview reads the pull request a document names. A document that does
// not parse, or lacks the fields, yields the zero Review.
func parseReview(document []byte) Review {
	var parsed struct {
		Review struct {
			Repository  string `json:"repository"`
			PullRequest int    `json:"pull_request"`
			Head        string `json:"head"`
			Base        string `json:"base"`
			MergeBase   string `json:"merge_base"`
		} `json:"review"`
	}
	if json.Unmarshal(document, &parsed) != nil {
		return Review{}
	}
	r := parsed.Review
	return Review{Repository: r.Repository, PullRequest: r.PullRequest, Head: r.Head, Base: r.Base, MergeBase: r.MergeBase}
}

func (s *Server) currentReview() Review {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.review
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	result, ok := s.sessionsResult(r.Context())
	if !ok {
		return // the client went away, or the server is stopping
	}
	body, err := json.Marshal(result)
	if err != nil {
		http.Error(w, "cannot encode sessions", http.StatusInternalServerError)
		return
	}
	body = append(body, '\n')
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

// sessionsResult answers for the document being served. The first request
// for a pull request starts the computation; requests that arrive meanwhile
// wait for that one. Only an available answer is kept: one that is not (the
// clone lacks the commits, no remote matches) is worked out again on the next
// request, so fetching the commits or adding the remote takes effect without
// restarting. It reports false when ctx ends before an answer.
func (s *Server) sessionsResult(ctx context.Context) (locallink.Result, bool) {
	entry, ok := s.sessionsAnswer(ctx)
	if !ok {
		return locallink.Result{}, false
	}
	return entry.result, true
}

// sessionsAnswer is sessionsResult's entry, finished, for what else is
// kept beside the answer.
func (s *Server) sessionsAnswer(ctx context.Context) (*sessionsEntry, bool) {
	fixed := func(result locallink.Result) (*sessionsEntry, bool) {
		return &sessionsEntry{result: result}, true
	}
	switch {
	case s.opts.Sample:
		return fixed(locallink.Unavailable(locallink.CodeSample, "the sample document has no sessions on this machine"))
	case s.opts.Sessions == nil:
		return fixed(locallink.Unavailable(locallink.CodeError, "this server does not link sessions"))
	}
	review := s.currentReview()
	if review.Repository == "" || review.Head == "" || review.Base == "" {
		return fixed(locallink.Unavailable(locallink.CodeNoHead, "the document does not name its repository and head and base commits"))
	}
	key := fmt.Sprintf("%s\x00%s\x00%s\x00%s", review.Repository, review.Head, review.Base, review.MergeBase)

	s.sessionsMu.Lock()
	if s.sessionsClosed {
		s.sessionsMu.Unlock()
		return nil, false
	}
	entry := s.sessions
	start := entry == nil || entry.key != key
	if !start {
		select {
		case <-entry.done: // expires is only read once the computation is over
			start = !entry.expires.IsZero() && time.Now().After(entry.expires)
		default:
		}
	}
	if start {
		entry = &sessionsEntry{key: key, done: make(chan struct{})}
		s.sessions = entry
		s.compute.Add(1)
	}
	s.sessionsMu.Unlock()
	if start {
		go s.computeSessions(entry, review)
	}

	select {
	case <-entry.done:
		return entry, true
	case <-ctx.Done():
		return nil, false
	case <-s.done:
		return nil, false
	}
}

func (s *Server) computeSessions(entry *sessionsEntry, review Review) {
	defer s.compute.Done()
	ctx, cancel := context.WithTimeout(s.life, sessionsTimeout)
	defer cancel()
	result, err := s.opts.Sessions(ctx, review)
	if err != nil {
		_, _ = fmt.Fprintf(s.opts.Warn, "warning: cannot link sessions: %v\n", err)
		result = locallink.Unavailable(locallink.CodeError, "the sessions on this machine could not be read")
		entry.expires = time.Now().Add(failureTTL)
	}
	entry.result = result
	if !result.Available && entry.expires.IsZero() {
		s.sessionsMu.Lock()
		if s.sessions == entry {
			s.sessions = nil
		}
		s.sessionsMu.Unlock()
	}
	close(entry.done)
}

// finishSessions stops new computations and waits, for a bounded time, for the
// ones running to wind down: they hold a private repository in the temporary
// directory that is removed only when they return. It runs after the
// server's context has ended, which cancels them.
func (s *Server) finishSessions() {
	s.sessionsMu.Lock()
	s.sessionsClosed = true
	s.sessionsMu.Unlock()
	finished := make(chan struct{})
	go func() {
		s.compute.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(computeGrace):
		_, _ = fmt.Fprintln(s.opts.Warn, "warning: a sessions computation did not stop in time")
	}
}
