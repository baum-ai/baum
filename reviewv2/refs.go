package reviewv2

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"

	"github.com/baum-ai/baum/bundle"
)

// Refs is what a document's citations may name: the pull request's hunks
// (with their paths and new-side ranges), the files they change, the linked
// sessions' prompts and events, the attachments, checks and review threads.
// RefsOf reads them from the bundle a run read, RefsOfIndex from a
// document's index. TileTypes, when set, are the tile types a tile may
// have; nil allows any.
type Refs struct {
	Hunks       map[string]HunkRef
	Files       map[string]bool
	Sessions    map[string]SessionRef
	Attachments map[string]bool
	Checks      map[string]bool
	Threads     map[string]bool
	TileTypes   map[string]bool
	// order is the hunks in diff order.
	order []string
}

// HunkRef is a hunk a document may cite.
type HunkRef struct {
	Path string
	// Start and Count are the new side's first line and line count; 0 when
	// unknown (a removal or a textless hunk).
	Start, Count int
}

// SessionRef is a linked session a document may cite: its prompts by index
// and its events by ID. A restricted session has neither.
type SessionRef struct {
	Restricted bool
	Prompts    map[int]bool
	Events     map[string]bool
}

func newRefs() *Refs {
	return &Refs{Hunks: map[string]HunkRef{}, Files: map[string]bool{}, Sessions: map[string]SessionRef{}, Attachments: map[string]bool{}, Checks: map[string]bool{}, Threads: map[string]bool{}}
}

// RefsOf returns what a document written from the bundle may cite.
func RefsOf(b *bundle.Bundle) *Refs {
	r := newRefs()
	if b == nil {
		return r
	}
	for _, h := range b.Diff {
		r.addHunk(h.ID, HunkRef{Path: h.Path, Start: h.StartLine, Count: h.LineCount})
	}
	for _, s := range b.Sessions {
		session := SessionRef{Restricted: s.Restricted, Prompts: map[int]bool{}, Events: map[string]bool{}}
		if !s.Restricted {
			for _, p := range s.Prompts {
				session.Prompts[p.Index] = true
			}
			for _, e := range s.CommandEvents {
				session.Events[e.ID] = true
			}
			for _, e := range s.ValidationEvents {
				session.Events[e.ID] = true
			}
		}
		r.Sessions[s.RootSessionID] = session
	}
	for _, a := range b.Attachments {
		r.Attachments[a.ID] = true
	}
	for _, c := range b.Checks {
		r.Checks[c.ID] = true
	}
	for _, t := range b.Threads {
		r.Threads[t.ID] = true
	}
	return r
}

// RefsOfIndex returns what a document whose index is index may cite: its
// index's hunks, sessions, attachments, checks and threads.
func RefsOfIndex(index json.RawMessage) *Refs {
	r := newRefs()
	var ix struct {
		Hunks []struct {
			ID       string `json:"id"`
			Path     string `json:"path"`
			NewRange []int  `json:"new_range"`
		} `json:"hunks"`
		Sessions []struct {
			ID         string `json:"id"`
			Restricted bool   `json:"restricted"`
			Prompts    []struct {
				Index int `json:"index"`
			} `json:"prompts"`
			Events []struct {
				ID string `json:"id"`
			} `json:"events"`
		} `json:"sessions"`
		Attachments []struct {
			ID string `json:"id"`
		} `json:"attachments"`
		Checks []struct {
			ID string `json:"id"`
		} `json:"checks"`
		Threads []struct {
			ID string `json:"id"`
		} `json:"threads"`
	}
	_ = json.Unmarshal(index, &ix)
	for _, h := range ix.Hunks {
		ref := HunkRef{Path: h.Path}
		if len(h.NewRange) == 2 {
			ref.Start, ref.Count = h.NewRange[0], h.NewRange[1]
		}
		r.addHunk(h.ID, ref)
	}
	for _, s := range ix.Sessions {
		session := SessionRef{Restricted: s.Restricted, Prompts: map[int]bool{}, Events: map[string]bool{}}
		for _, p := range s.Prompts {
			session.Prompts[p.Index] = true
		}
		for _, e := range s.Events {
			session.Events[e.ID] = true
		}
		r.Sessions[s.ID] = session
	}
	for _, a := range ix.Attachments {
		r.Attachments[a.ID] = true
	}
	for _, c := range ix.Checks {
		r.Checks[c.ID] = true
	}
	for _, t := range ix.Threads {
		r.Threads[t.ID] = true
	}
	return r
}

func (r *Refs) addHunk(id string, ref HunkRef) {
	if _, ok := r.Hunks[id]; !ok {
		r.order = append(r.order, id)
	}
	r.Hunks[id] = ref
	if ref.Path != "" {
		r.Files[ref.Path] = true
	}
}

// HunkIDs are the hunks in diff order.
func (r *Refs) HunkIDs() []string { return slices.Clone(r.order) }

// HasHunk reports whether the pull request has the hunk.
func (r *Refs) HasHunk(id string) bool {
	_, ok := r.Hunks[id]
	return ok
}

// anchor is the fields of an anchor the checks read.
type anchor struct {
	Type    string          `json:"type"`
	ID      json.RawMessage `json:"id"`
	Lines   []int           `json:"lines"`
	Path    string          `json:"path"`
	Session string          `json:"session"`
	Index   int             `json:"index"`
	Event   string          `json:"event"`
	URL     string          `json:"url"`
}

func (a anchor) id() string {
	var s string
	if json.Unmarshal(a.ID, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(a.ID, &n) == nil {
		return n.String()
	}
	return ""
}

// anchorTypes are the citation types of baum.review/1's anchor.
var anchorTypes = []string{"hunk", "file", "commit", "pr", "pr_body", "pr_comment", "review_thread", "prompt", "session_event", "check", "bot_report", "issue", "spec", "external_url", "attachment"}

// AnchorProblem says why a citation does not resolve to what the pull
// request has, or "" when it does. A hunk must be one of the diff's (and its
// lines, when given, on the hunk's new side), a file one the diff changes,
// a prompt or session event one of a linked session's, an attachment, check
// or review thread one the bundle has. Other citation types are accepted
// when well formed.
func (r *Refs) AnchorProblem(raw json.RawMessage) string {
	var a anchor
	if err := json.Unmarshal(raw, &a); err != nil {
		return "a citation is not an object"
	}
	switch a.Type {
	case "hunk":
		id := a.id()
		h, ok := r.Hunks[id]
		if !ok {
			return fmt.Sprintf("it cites hunk %q, which this pull request does not have", id)
		}
		if a.Lines != nil {
			if len(a.Lines) != 2 || a.Lines[0] < 1 || a.Lines[1] < a.Lines[0] {
				return fmt.Sprintf("its citation of hunk %s gives lines that are not a first and last line", id)
			}
			if h.Count > 0 && (a.Lines[0] < h.Start || a.Lines[1] > h.Start+h.Count-1) {
				return fmt.Sprintf("its citation of hunk %s names lines %d to %d, outside the hunk's lines %d to %d", id, a.Lines[0], a.Lines[1], h.Start, h.Start+h.Count-1)
			}
		}
	case "file":
		if !r.Files[a.Path] {
			return fmt.Sprintf("it cites file %q, which this pull request does not change", a.Path)
		}
	case "prompt":
		s, ok := r.Sessions[a.Session]
		switch {
		case !ok:
			return fmt.Sprintf("it cites a prompt of session %q, which is not linked", a.Session)
		case s.Restricted:
			return fmt.Sprintf("it cites a prompt of session %q, which is restricted", a.Session)
		case !s.Prompts[a.Index]:
			return fmt.Sprintf("it cites prompt %d of session %q, which it does not have", a.Index, a.Session)
		}
	case "session_event":
		s, ok := r.Sessions[a.Session]
		if !ok || !s.Events[a.Event] {
			return fmt.Sprintf("it cites event %q of session %q, which the sessions do not have", a.Event, a.Session)
		}
	case "attachment":
		if id := a.id(); id != "" && !r.Attachments[id] {
			return fmt.Sprintf("it cites attachment %q, which the pull request does not have", id)
		}
		if a.id() == "" && a.URL == "" {
			return "it cites an attachment without its id"
		}
	case "check":
		if id := a.id(); !r.Checks[id] {
			return fmt.Sprintf("it cites check %q, which the pull request does not have", id)
		}
	case "review_thread":
		if id := a.id(); !r.Threads[id] {
			return fmt.Sprintf("it cites review thread %q, which the pull request does not have", id)
		}
	default:
		if !slices.Contains(anchorTypes, a.Type) {
			return "it cites something of type " + strconv.Quote(a.Type) + ", which is not a citation type"
		}
	}
	return ""
}

// HunkAnchor is a citation of a hunk, or of lines of it when lines is a
// first and last line.
func HunkAnchor(id string, lines []int) Anchor {
	value := map[string]any{"type": "hunk", "id": id}
	if len(lines) == 2 {
		value["lines"] = lines
	}
	encoded, _ := json.Marshal(value)
	return encoded
}

// citedHunks are the hunks anchors cite, once each, in order.
func citedHunks(anchors []Anchor) []string {
	var out []string
	for _, raw := range anchors {
		var a anchor
		if json.Unmarshal(raw, &a) == nil && a.Type == "hunk" {
			if id := a.id(); id != "" && !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}
	return out
}
