package viewserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/lastpersonlabs/baum/internal/locallink"
	"github.com/lastpersonlabs/baum/internal/transcript"
	"github.com/lastpersonlabs/baum/redact"
	"github.com/lastpersonlabs/baum/sessions/session"
)

// errNotLinked is a session id the sessions answer does not list.
var errNotLinked = errors.New("no such linked session")

// handleTranscript serves GET /api/sessions/{id}: one session of the
// /api/sessions answer, its graph redacted as "baum sessions --dump" redacts
// it, with the user's home directory written as ~, and shaped and bounded
// by package transcript. A session the answer does not list is not found,
// whatever this machine holds.
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
	case r.Context().Err() != nil:
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

// transcript builds the body for one session of entry's answer, once.
func (s *Server) transcript(ctx context.Context, entry *sessionsEntry, id string) ([]byte, error) {
	result := entry.result
	meta, graph, ok := linkedSession(result, id)
	if !ok {
		return nil, errNotLinked
	}
	entry.transcriptsMu.Lock()
	defer entry.transcriptsMu.Unlock()
	if body, ok := entry.transcripts[id]; ok {
		return body, nil
	}
	engine, err := s.redactor()
	if err != nil {
		return nil, err
	}
	redacted, err := locallink.RedactGraph(ctx, engine, graph, false)
	if err != nil {
		return nil, err
	}
	redacted = replaceHome(redacted, s.home)
	var shown session.Graph
	if err := json.Unmarshal(redacted, &shown); err != nil {
		return nil, fmt.Errorf("reading the redacted session graph: %w", err)
	}
	body, err := json.Marshal(transcript.Build(&shown, meta, transcript.DefaultLimits))
	if err != nil {
		return nil, fmt.Errorf("encoding the transcript: %w", err)
	}
	body = append(body, '\n')
	if entry.transcripts == nil {
		entry.transcripts = map[string][]byte{}
	}
	entry.transcripts[id] = body
	return body, nil
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

// replaceHome writes ~ for the home directory wherever a JSON text names it,
// as is or JSON-escaped (a Windows path's backslashes), unless the name runs
// on into a longer one, such as another user's home whose name starts with it.
func replaceHome(data []byte, home string) []byte {
	if len(home) < 2 {
		return data
	}
	forms := [][]byte{[]byte(home)}
	if escaped, err := json.Marshal(home); err == nil {
		if inner := escaped[1 : len(escaped)-1]; !bytes.Equal(inner, forms[0]) {
			forms = append(forms, inner)
		}
	}
	for _, form := range forms {
		data = replaceWhole(data, form, []byte("~"))
	}
	return data
}

func replaceWhole(data, old, replacement []byte) []byte {
	var out []byte
	rest := data
	for {
		at := bytes.Index(rest, old)
		if at < 0 {
			break
		}
		end := at + len(old)
		if end < len(rest) && continuesName(rest[end]) {
			out = append(out, rest[:end]...)
			rest = rest[end:]
			continue
		}
		out = append(out, rest[:at]...)
		out = append(out, replacement...)
		rest = rest[end:]
	}
	if out == nil {
		return data
	}
	return append(out, rest...)
}

func continuesName(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_' || b == '.'
}
