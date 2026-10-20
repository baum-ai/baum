package viewserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/internal/transcript"
	"github.com/baum-ai/baum/redact"
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

// buildTranscript shapes and bounds the graph first, so nothing past the
// limits is ever redacted or held, then redacts what is left: each text as
// it is kept, before it is cut, and the encoded transcript as a whole, every
// string and key of it. The home directory is written as ~ last.
func (s *Server) buildTranscript(ctx context.Context, meta transcript.Meta, graph *session.Graph) ([]byte, error) {
	engine, err := s.redactor()
	if err != nil {
		return nil, err
	}
	shaped, err := transcript.Build(graph, meta, transcript.DefaultLimits, func(text string) (string, error) {
		masked, _, err := engine.RedactContext(ctx, text)
		return masked, err
	})
	if err != nil {
		return nil, err
	}
	body, err := transcript.Encode(shaped)
	if err != nil {
		return nil, fmt.Errorf("encoding the transcript: %w", err)
	}
	body, _, err = engine.RedactJSON(ctx, body)
	if err != nil {
		return nil, err
	}
	return replaceHome(body, s.homes), nil
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

// redactor compiles the redaction rules on the first transcript asked for.
func (s *Server) redactor() (*redact.Engine, error) {
	s.engineOnce.Do(func() { s.engine, s.engineErr = redact.New() })
	return s.engine, s.engineErr
}

// userHome is the home directory: $HOME (or its platform's equivalent), else
// the account's, else none.
func userHome() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	if account, err := user.Current(); err == nil {
		return account.HomeDir
	}
	return ""
}

// homeForm is one way a JSON text can spell the home directory.
type homeForm struct {
	text []byte
	// fold matches regardless of case, as Windows paths do.
	fold bool
}

// homeForms lists the spellings of home to replace: as given (trailing
// separators dropped) and as its symbolic links resolve, each as is and
// JSON-escaped; for a Windows path also with forward slashes, matched
// regardless of case. A home of one character or none (the root) yields
// nothing.
func homeForms(home string) []homeForm {
	var paths []string
	add := func(path string) {
		path = strings.TrimRight(path, `/\`)
		if len(path) >= 2 && !strings.HasSuffix(path, ":") {
			for _, seen := range paths {
				if seen == path {
					return
				}
			}
			paths = append(paths, path)
		}
	}
	if home == "" {
		return nil
	}
	add(filepath.Clean(home))
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		add(resolved)
	}
	var forms []homeForm
	for _, path := range paths {
		windows := strings.Contains(path, `\`) || len(path) >= 2 && path[1] == ':'
		spellings := []string{path}
		if windows {
			spellings = append(spellings, strings.ReplaceAll(path, `\`, "/"))
		}
		for _, spelling := range spellings {
			forms = append(forms, homeForm{text: []byte(spelling), fold: windows})
			if escaped, err := json.Marshal(spelling); err == nil {
				if inner := escaped[1 : len(escaped)-1]; string(inner) != spelling {
					forms = append(forms, homeForm{text: inner, fold: windows})
				}
			}
		}
	}
	return forms
}

// replaceHome writes ~ for the home directory wherever a JSON text names it
// as a path of its own: after the start of the text, a quote, white space
// (an escaped newline or tab included), a separator or one of = : ( [ { , ;
// | < >, and not running on into a longer name. /data/home/user/src keeps
// its name, and so does a longer user's home beside this one.
func replaceHome(data []byte, forms []homeForm) []byte {
	for _, form := range forms {
		data = replaceWhole(data, form)
	}
	return data
}

func replaceWhole(data []byte, form homeForm) []byte {
	hay, needle := data, form.text
	if form.fold {
		hay, needle = bytes.ToLower(data), bytes.ToLower(form.text)
	}
	var out []byte
	last := 0
	for from := 0; ; {
		i := bytes.Index(hay[from:], needle)
		if i < 0 {
			break
		}
		at, end := from+i, from+i+len(needle)
		from = at + 1
		if !startsPath(data, at) || end < len(data) && continuesName(data[end]) {
			continue
		}
		out = append(out, data[last:at]...)
		out = append(out, '~')
		last, from = end, end
	}
	if out == nil {
		return data
	}
	return append(out, data[last:]...)
}

func startsPath(data []byte, at int) bool {
	if at == 0 || strings.IndexByte("\"'` \t\r\n=:([{,;|<>/", data[at-1]) >= 0 {
		return true
	}
	// An escaped newline, tab or carriage return in the JSON text.
	return at >= 2 && data[at-2] == '\\' && strings.IndexByte("ntr", data[at-1]) >= 0
}

func continuesName(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_' || b == '.'
}
