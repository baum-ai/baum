package orchestrate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/reviewv2"
)

// The limits of the texts the levels write are the document layer's
// (reviewv2.Max*), which its checks hold every level to. A text over its
// character limit by at most a quarter is cut, at a sentence's end where
// one is near; one further over, or over its words or sentences, is sent
// back for a repair.
const (
	MaxTitleWords    = reviewv2.MaxTitleWords
	MaxSectionTitle  = reviewv2.MaxTitle
	MaxGist          = reviewv2.MaxGist
	MaxScope         = reviewv2.MaxScope
	MaxLevelReason   = reviewv2.MaxLevelReason
	MaxSummary       = reviewv2.MaxSummary
	MaxStepTitle     = reviewv2.MaxStepTitle
	MaxStepText      = reviewv2.MaxStepText
	MaxRiskText      = reviewv2.MaxRiskText
	MaxShows         = reviewv2.MaxShows
	MaxContextText   = reviewv2.MaxContextPart
	MaxContextFiles  = reviewv2.MaxContextFiles
	MaxLede          = reviewv2.MaxSummary
	MaxVerdictText   = reviewv2.MaxVerdictText
	MaxPointText     = reviewv2.MaxPointText
	MaxRiskWhy       = reviewv2.MaxRiskWhy
	MaxIntentText    = reviewv2.MaxIntentText
	MaxTiles         = reviewv2.MaxTiles
	MaxCites         = reviewv2.MaxCites
	MaxTileTitle     = reviewv2.MaxTileTitle
	MaxCaption       = reviewv2.MaxCaption
	MaxSectionTiles  = 2
	MaxReasonText    = 200
	maxFindingsShown = 24
)

var (
	barePR       = regexp.MustCompile(`(?i)(?:^|[^\w&])#\d+\b|\b(?:PR|pull request)\s+#?\d+\b`)
	lineCount    = regexp.MustCompile(`(?i)\b\d+\s+(?:new\s+|changed\s+)?(?:lines?|files?)\b`)
	mdLink       = regexp.MustCompile(`\[([^\]\n]+)\]\([^)\s]+\)`)
	sentenceStop = regexp.MustCompile(`[.!?]["')\]]*\s`)
)

// checker collects the findings of one reply, each with the path of the
// value it is about, in the host's words.
type checker struct {
	findings []string
	// lenient is the check of a call's last pass: what is wrong with a
	// text's style or length is fixed (cut) or let through rather than
	// sent back, so a section or a tile is not lost over its wording. What
	// is wrong with its structure still rejects it.
	lenient bool
}

func (c *checker) add(path, format string, args ...any) {
	c.findings = append(c.findings, path+": "+fmt.Sprintf(format, args...))
}

// style adds a finding about a text's wording or length, unless the check
// is lenient.
func (c *checker) style(path, format string, args ...any) {
	if !c.lenient {
		c.add(path, format, args...)
	}
}

// merge adds the document layer's findings of a part (reviewv2.Check*).
// The last pass lets through those about a text's wording or length.
func (c *checker) merge(found []string) {
	for _, f := range found {
		if c.lenient && styleFinding(f) {
			continue
		}
		if !slices.Contains(c.findings, f) {
			c.findings = append(c.findings, f)
		}
	}
}

// styleFinding reports whether a document layer finding is about a text's
// wording or length, not its structure.
func styleFinding(f string) bool {
	for _, s := range []string{"characters; write at most", "sentences; write at most", "words; a title", `the word "evidence"`, "names a hunk by its ID", "joins two things", "spans lines", "over their limits"} {
		if strings.Contains(f, s) {
			return true
		}
	}
	return false
}

// String is the findings as a repair reads them.
func (c *checker) String() string {
	if len(c.findings) > maxFindingsShown {
		return strings.Join(c.findings[:maxFindingsShown], "\n") + fmt.Sprintf("\n… and %d more", len(c.findings)-maxFindingsShown)
	}
	return strings.Join(c.findings, "\n")
}

// clean makes text one plain line: whitespace folded, control characters
// dropped, markdown links to their text and emphasis markers removed.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t' || r == '\r':
			return ' '
		case unicode.IsControl(r), r == 0x200b, r == 0x200c, r == 0x200d, r == 0x2060, r == 0xfeff:
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	s = mdLink.ReplaceAllString(s, "$1")
	s = strings.NewReplacer("**", "", "__", "").Replace(s)
	return strings.TrimSpace(s)
}

// cut shortens text to at most limit characters at a word boundary, with
// an ellipsis.
func cut(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	out := string([]rune(s)[:limit-1])
	if i := strings.LastIndex(out, " "); i > limit/2 {
		out = out[:i]
	}
	return strings.TrimRight(out, " ,;:.-") + "…"
}

// fit cuts a text to its limit: at the end of its last sentence that fits,
// when that keeps most of it, else at a word.
func fit(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	prefix := string([]rune(s)[:limit])
	ends := sentenceStop.FindAllStringIndex(prefix+" ", -1)
	if len(ends) > 0 {
		at := ends[len(ends)-1][1]
		if kept := strings.TrimSpace(prefix[:min(at, len(prefix))]); utf8.RuneCountInString(kept) >= limit*3/5 {
			return kept
		}
	}
	return cut(s, limit)
}

// copyRules reports the copy rules a text breaks that the host can see and
// the document layer's checks do not (they report the word "evidence",
// hunk IDs, lengths, sentences and titles).
func (c *checker) copyRules(path, s string) {
	switch {
	case barePR.MatchString(s):
		c.style(path, "names a pull request or issue by number; say what the other pull request did")
	case lineCount.MatchString(s):
		c.style(path, "counts lines or files; say what changed instead")
	}
}

// text checks one text: cleaned, the copy rules, required, its sentences
// (none when max is 0) and its length.
func (c *checker) text(path, s string, limit, maxSentences int, required bool) string {
	s = clean(s)
	if s == "" {
		if required {
			c.add(path, "is empty; write it")
		}
		return ""
	}
	c.copyRules(path, s)
	// A text a quarter over its limit at most is cut to it, at a sentence's
	// end where one is near; further over, it is left for the document
	// layer's checks to send back, or cut on the last pass. Its sentences
	// are theirs to count (maxSentences says what they hold it to).
	_ = maxSentences
	if n := utf8.RuneCountInString(s); n > limit+limit/4 && !c.lenient {
		return s
	}
	return fit(s, limit)
}

// title checks a title: a plain subject of a few words, never two things
// joined by a comma.
func (c *checker) title(path, s string, limit, maxWords int) string {
	s = c.text(path, s, limit, 0, true)
	if s == "" {
		return s
	}
	_ = maxWords // the document layer's checks count a title's words
	return strings.TrimRight(s, ".")
}

// strings checks every string in a tile's data for the copy rules.
func (c *checker) dataStrings(path string, data json.RawMessage) {
	var value any
	if json.Unmarshal(data, &value) != nil {
		return
	}
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case string:
			if !strings.HasPrefix(v, "h_") {
				c.copyRules(path, v)
			}
		case []any:
			for _, item := range v {
				walk(item)
			}
		case map[string]any:
			for key, item := range v {
				if key != "source" && key != "id" && key != "type" {
					walk(item)
				}
			}
		}
	}
	walk(value)
}

// facts are what the host checks cites and ids against: the pull request's
// hunks, sessions, checks, threads, comments and attachments, and the
// checkout.
type facts struct {
	b         *bundle.Bundle
	roots     []string
	hunks     map[string]bundle.Hunk
	order     []string
	checks    map[string]bool
	threads   map[string]bool
	comments  map[string]bool
	images    map[string]bool
	prompts   map[string]map[int]bool
	events    map[string]map[string]bool
	sessions  map[string]bool
	hasBody   bool
	pathCache map[string]bool
	// refs are what the document layer's checks resolve cites against.
	refs *reviewv2.Refs
}

func newFacts(b *bundle.Bundle, roots []string) *facts {
	f := &facts{b: b, roots: roots, hunks: map[string]bundle.Hunk{}, checks: map[string]bool{}, threads: map[string]bool{}, comments: map[string]bool{},
		images: map[string]bool{}, prompts: map[string]map[int]bool{}, events: map[string]map[string]bool{}, sessions: map[string]bool{},
		hasBody: strings.TrimSpace(b.PullRequest.Body) != "", pathCache: map[string]bool{}, refs: reviewv2.RefsOf(b)}
	for _, h := range b.Diff {
		f.hunks[h.ID] = h
		f.order = append(f.order, h.ID)
	}
	for _, c := range b.Checks {
		f.checks[c.ID] = true
	}
	for _, t := range b.Threads {
		f.threads[t.ID] = true
	}
	for _, c := range b.Conversation {
		f.comments[c.ID] = true
	}
	for _, a := range b.Attachments {
		f.images[a.ID] = true
	}
	for _, s := range b.Sessions {
		if s.Restricted {
			continue
		}
		f.sessions[s.RootSessionID] = true
		f.prompts[s.RootSessionID] = map[int]bool{}
		for _, p := range s.Prompts {
			f.prompts[s.RootSessionID][p.Index] = true
		}
		f.events[s.RootSessionID] = map[string]bool{}
		for _, e := range s.CommandEvents {
			f.events[s.RootSessionID][e.ID] = true
		}
	}
	return f
}

// hasPath reports whether a repository path is a file of the head or the
// base checkout.
func (f *facts) hasPath(path string) bool {
	path = strings.TrimPrefix(strings.TrimPrefix(path, "head/"), "merge-base/")
	if path == "" || !filepath.IsLocal(path) {
		return false
	}
	if ok, seen := f.pathCache[path]; seen {
		return ok
	}
	ok := false
	if _, isHunk := f.pathOfHunk(path); isHunk {
		ok = true
	}
	for _, root := range f.roots {
		if info, err := os.Stat(filepath.Join(root, path)); err == nil && !info.IsDir() {
			ok = true
		}
	}
	f.pathCache[path] = ok
	return ok
}

func (f *facts) pathOfHunk(path string) (string, bool) {
	for _, h := range f.b.Diff {
		if h.Path == path || h.OldPath == path {
			return h.ID, true
		}
	}
	return "", false
}

// anchor is a cite as the model writes it.
type anchor struct {
	Type    string `json:"type"`
	ID      string `json:"id,omitempty"`
	Path    string `json:"path,omitempty"`
	Lines   []int  `json:"lines,omitempty"`
	Session string `json:"session,omitempty"`
	Index   *int   `json:"index,omitempty"`
	Event   string `json:"event,omitempty"`
}

// cites keeps the cites that resolve, once each, at most MaxCites, in the
// stored anchor shape: a hunk's lines only when they fall inside it. It
// returns how many it dropped.
func (f *facts) cites(in []json.RawMessage) ([]json.RawMessage, int) {
	out := []json.RawMessage{}
	seen := map[string]bool{}
	dropped := 0
	for _, raw := range in {
		var a anchor
		if json.Unmarshal(raw, &a) != nil {
			dropped++
			continue
		}
		var keep any
		switch a.Type {
		case "hunk":
			h, ok := f.hunks[a.ID]
			if !ok {
				break
			}
			hunk := map[string]any{"type": "hunk", "id": a.ID}
			if len(a.Lines) == 2 && h.LineCount > 0 && a.Lines[0] >= h.StartLine && a.Lines[1] >= a.Lines[0] && a.Lines[1] <= h.StartLine+h.LineCount-1 {
				hunk["lines"] = a.Lines
			}
			keep = hunk
		case "file":
			if f.hasPath(a.Path) {
				keep = map[string]any{"type": "file", "path": strings.TrimPrefix(strings.TrimPrefix(a.Path, "head/"), "merge-base/")}
			}
		case "prompt":
			if a.Index != nil && f.prompts[a.Session][*a.Index] {
				keep = map[string]any{"type": "prompt", "session": a.Session, "index": *a.Index}
			}
		case "session_event":
			if f.events[a.Session][a.Event] {
				keep = map[string]any{"type": "session_event", "session": a.Session, "event": a.Event}
			}
		case "check":
			if f.checks[a.ID] {
				keep = map[string]any{"type": "check", "id": a.ID}
			}
		case "review_thread":
			if f.threads[a.ID] {
				keep = map[string]any{"type": "review_thread", "id": a.ID}
			}
		case "pr_comment":
			if f.comments[a.ID] {
				keep = map[string]any{"type": "pr_comment", "id": a.ID}
			}
		case "attachment":
			if f.images[a.ID] {
				keep = map[string]any{"type": "attachment", "id": a.ID}
			}
		case "pr_body":
			if f.hasBody {
				keep = map[string]any{"type": "pr_body"}
			}
		}
		if keep == nil {
			dropped++
			continue
		}
		encoded, _ := json.Marshal(keep)
		if seen[string(encoded)] || len(out) == MaxCites {
			continue
		}
		seen[string(encoded)] = true
		out = append(out, encoded)
	}
	return out, dropped
}

// hunkCites are the hunk IDs among cites.
func hunkCites(cites []json.RawMessage) []string {
	var out []string
	for _, raw := range cites {
		var a anchor
		if json.Unmarshal(raw, &a) == nil && a.Type == "hunk" {
			out = append(out, a.ID)
		}
	}
	return out
}

// hunkAnchors are hunk cites of ids.
func hunkAnchors(ids []string) []json.RawMessage {
	out := []json.RawMessage{}
	for _, id := range ids {
		encoded, _ := json.Marshal(map[string]string{"type": "hunk", "id": id})
		out = append(out, encoded)
	}
	return out
}

// refsFor are the refs with the registry's types, which a planned tile may
// have.
func (f *facts) refsFor(r *Registry) *reviewv2.Refs {
	refs := *f.refs
	refs.TileTypes = map[string]bool{}
	for _, name := range r.names() {
		refs.TileTypes[name] = true
	}
	return &refs
}
