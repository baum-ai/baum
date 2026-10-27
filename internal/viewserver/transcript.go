package viewserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/internal/transcript"
	"github.com/baum-ai/baum/sessions/session"
)

// errNotLinked is a session id the sessions answer does not list.
var errNotLinked = errors.New("no such linked session")

// errStopping is a transcript asked for while the server stops.
var errStopping = errors.New("the server is stopping")

// transcriptJob is one build of a session's transcript, shared by every
// request for that session while it runs and kept once it succeeds.
type transcriptJob struct {
	done chan struct{}
	body []byte
	err  error
}

// handleTranscript serves GET /api/sessions/{id}: one session of the
// /api/sessions answer, shaped and bounded by package transcript, redacted
// with the engine "baum internal sessions --dump" uses, with the user's home directory
// written as ~. A session the answer does not list is not found, whatever
// this machine holds.
func (s *Server) handleTranscript(w http.ResponseWriter, r *http.Request) {
	entry, ok := s.sessionsAnswer(r.Context())
	if !ok {
		return // the client went away, or the server is stopping
	}
	body, err := s.transcript(r.Context(), entry, r.PathValue("id"))
	switch {
	case errors.Is(err, errNotLinked):
		http.NotFound(w, r)
		return
	case r.Context().Err() != nil, errors.Is(err, errStopping):
		return
	case err != nil:
		_, _ = fmt.Fprintf(s.opts.Warn, "warning: cannot show a session: %v\n", err)
		http.Error(w, "cannot read this session", http.StatusInternalServerError)
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

// transcript answers with the body for one session of entry's answer. Each
// session is built once, apart from the others, so a large one does not hold
// up the rest; a build that fails is tried again on the next request. A
// build outlives the request that started it, up to the server's life.
func (s *Server) transcript(ctx context.Context, entry *sessionsEntry, id string) ([]byte, error) {
	meta, graph, ok := linkedSession(entry.result, id)
	if !ok {
		return nil, errNotLinked
	}
	entry.transcriptsMu.Lock()
	job := entry.transcripts[id]
	start := job == nil
	if start {
		s.sessionsMu.Lock()
		closed := s.sessionsClosed
		if !closed {
			s.compute.Add(1)
		}
		s.sessionsMu.Unlock()
		if closed {
			entry.transcriptsMu.Unlock()
			return nil, errStopping
		}
		job = &transcriptJob{done: make(chan struct{})}
		if entry.transcripts == nil {
			entry.transcripts = map[string]*transcriptJob{}
		}
		entry.transcripts[id] = job
	}
	entry.transcriptsMu.Unlock()
	if start {
		go func() {
			defer s.compute.Done()
			job.body, job.err = s.buildTranscript(s.life, meta, graph)
			if job.err != nil {
				entry.transcriptsMu.Lock()
				if entry.transcripts[id] == job {
					delete(entry.transcripts, id)
				}
				entry.transcriptsMu.Unlock()
			}
			close(job.done)
		}()
	}
	select {
	case <-job.done:
		return job.body, job.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.done:
		return nil, errStopping
	}
}

// buildTranscript renders the session as every local transcript is
// rendered (transcript.Renderer): shaped and bounded, then redacted, with the
// home directory written as ~.
func (s *Server) buildTranscript(ctx context.Context, meta transcript.Meta, graph *session.Graph) ([]byte, error) {
	return s.render.Render(ctx, graph, meta)
}

// linkedSession finds id among the answer's sessions, with its graph and how
// it is linked.
func linkedSession(result locallink.Result, id string) (transcript.Meta, *session.Graph, bool) {
	if !result.Available || id == "" {
		return transcript.Meta{}, nil, false
	}
	graph := result.Graphs[id]
	if graph == nil {
		return transcript.Meta{}, nil, false
	}
	for _, linked := range result.Sessions {
		if linked.ID != id {
			continue
		}
		meta := transcript.Meta{ID: id, Vendor: linked.Vendor, Branch: linked.Branch}
		for _, commit := range result.Commits {
			for _, link := range commit.Links {
				if link.Session == id {
					meta.Commits++
					meta.Confidence = max(meta.Confidence, link.Confidence)
					break
				}
			}
		}
		return meta, graph, true
	}
	return transcript.Meta{}, nil, false
}
