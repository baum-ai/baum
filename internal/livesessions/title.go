package livesessions

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/baum-ai/baum/sessions/collector/model"
	"github.com/baum-ai/baum/sessions/cursortranscript"
)

// Bounds on what is read of a session to describe it. A session is looked at
// no more than once per TitleRecheckEvery (CursorTitleRecheckEvery for
// Cursor, whose database is one large file it writes all the time), and its
// files are read again only when they changed. Claude Code and Codex append
// to their transcripts, so each read starts where the last one stopped, at
// most scanPass bytes at a time; until a long transcript is caught up, its
// last titleTail bytes, where Claude Code rewrites the title as the session
// goes, name it.
const (
	TitleRecheckEvery       = 5 * time.Second
	CursorTitleRecheckEvery = 30 * time.Second
	maxTitle                = 120
	maxExcerpt              = 240
	maxSummary              = 32 << 10
	titleTail               = 256 << 10
	scanPass                = 16 << 20
	// maxLine is the longest transcript line looked into: a longer one is a
	// pasted file or a tool's output. A compaction summary can be long.
	maxLine = 256 << 10
)

// Listed is a session as the list serves it: the tracker's Session, the title
// its agent gave it and, after a compaction, what the summary says it is
// about.
type Listed struct {
	Session
	// Title is the session's name as its agent shows it: Claude Code's title
	// (the one the user set, else the one it generated), Codex's thread
	// name, Cursor's conversation name; else the session's first prompt.
	// One line, masked as transcripts are. Empty only when the agent's
	// files cannot be read.
	Title string `json:"title,omitempty"`
	// Summary is the opening of the session's latest compaction summary (its
	// first section), one line, masked; absent until the agent compacted the
	// session. The transcript route serves the whole summary.
	Summary string `json:"summary,omitempty"`
	// Activity is what the transcript says the session is doing: its latest
	// tool calls, how many files it wrote and its latest message (see
	// Activity). Absent for Cursor sessions and until a transcript has a tool
	// call or assistant text.
	Activity *Activity `json:"activity,omitempty"`
}

// described is what was last read of a session.
type described struct {
	// mu is held while the session's files are read.
	mu      sync.Mutex
	checked time.Time
	version string
	// title, excerpt and summary are as served: masked and cut.
	title, excerpt, summary string
	activity                *Activity
	// found is the file a lookup by id found, so the store is searched once.
	found string
	scan  scanState
}

// scanState is how far a transcript has been read, and what was found.
type scanState struct {
	path   string
	offset int64
	// custom and generated are Claude Code's titles; prompt is the first one
	// typed; summary is the latest compaction summary, raw.
	custom, generated, prompt, summary string
	// act is what the transcript's assistant lines did, kept across passes.
	act activityScan
}

// safeID is a session id that can name a file: Claude Code and Codex ids are
// UUIDs.
var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// listed is the tracker's sessions with their titles and summaries.
func (t *Transcripts) listed() []Listed {
	sessions := t.Tracker.Sessions()
	out := make([]Listed, 0, len(sessions))
	keep := make(map[key]bool, len(sessions))
	for _, s := range sessions {
		_, path, _ := t.Tracker.Lookup(s.Agent, s.ID)
		k := key{s.Agent, s.ID}
		keep[k] = true
		title, excerpt, _, activity := t.describe(k, path)
		out = append(out, Listed{Session: s, Title: title, Summary: excerpt, Activity: activity})
	}
	t.mu.Lock()
	for k := range t.described {
		if !keep[k] {
			delete(t.described, k)
		}
	}
	t.mu.Unlock()
	return out
}

// describe is the session's title, the opening of its compaction summary, the
// whole summary and its activity, read again when its files changed and it was last looked
// at long enough ago. A failed read keeps what was read before and is tried
// again by the next request, since a transcript can be named before it is
// written.
func (t *Transcripts) describe(k key, path string) (title, excerpt, summary string, activity *Activity) {
	t.mu.Lock()
	if t.described == nil {
		t.described = map[key]*described{}
	}
	e := t.described[k]
	if e == nil {
		e = &described{}
		t.described[k] = e
	}
	t.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	every := TitleRecheckEvery
	if k.agent == AgentCursor {
		every = CursorTitleRecheckEvery
	}
	now := t.now()
	if !e.checked.IsZero() && now.Sub(e.checked) < every {
		return e.title, e.excerpt, e.summary, e.activity
	}
	src, err := t.describeFiles(k, cmp.Or(path, e.found))
	if err != nil {
		return e.title, e.excerpt, e.summary, e.activity
	}
	version, err := Location{files: src.files(), store: src.store}.Version()
	if err != nil {
		return e.title, e.excerpt, e.summary, e.activity
	}
	if version == e.version {
		e.checked = now
		return e.title, e.excerpt, e.summary, e.activity
	}
	if err := src.read(k.id, &e.scan); err != nil {
		t.logger().Debug("live session title not read", "agent", k.agent, "error", err)
		return e.title, e.excerpt, e.summary, e.activity
	}
	caught := e.scan.offset >= src.size
	if !caught && k.agent == AgentClaudeCode {
		// Not caught up: the tail names the session meanwhile.
		var tail scanState
		_ = readClaude(src.transcript, max(0, src.size-titleTail), src.size, &tail)
		title = cmp.Or(tail.custom, tail.generated)
	}
	title = cmp.Or(title, e.scan.custom, e.scan.generated, src.name, e.scan.prompt)
	body := summaryBody(e.scan.summary)
	e.title = t.Tracker.oneLine(title, maxTitle)
	e.excerpt = t.Tracker.oneLine(summaryOpening(body), maxExcerpt)
	e.summary = t.block(body, maxSummary)
	e.activity = t.activity(&e.scan.act, t.Tracker.workdir(k.agent, k.id))
	e.found = src.found
	if caught {
		e.version = version
	}
	e.checked = now
	return e.title, e.excerpt, e.summary, e.activity
}

// block is text an agent wrote, kept as lines: format characters dropped,
// other control characters but newlines and tabs made spaces, masked, then
// cut to limit bytes.
func (t *Transcripts) block(text string, limit int) string {
	text = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case unicode.Is(unicode.Cf, r):
			return -1
		case unicode.IsControl(r):
			return ' '
		}
		return r
	}, strings.ToValidUTF8(text, "�"))
	text = strings.TrimSpace(text)
	if text != "" && t.Tracker.Mask != nil {
		text = t.Tracker.Mask(text)
	}
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

// summaryBody is a compaction summary without what Claude Code wraps it in:
// from the line after "Summary:" up to its closing instructions.
func summaryBody(text string) string {
	if _, after, ok := strings.Cut(text, "\nSummary:\n"); ok {
		text = after
	}
	if before, _, ok := strings.Cut(text, "\nIf you need specific details from before compaction"); ok {
		text = before
	}
	return strings.TrimSpace(text)
}

// numbered is a top-level section heading of a summary, such as
// "2. Key Technical Concepts:" or "1. **Primary Request and Intent** - …".
var numbered = regexp.MustCompile(`^\d+\.\s`)

// Markdown left out of a one-line excerpt: emphasis, code marks and heading
// marks; a link keeps its text.
var (
	emphasis = regexp.MustCompile("\\*\\*|__|`")
	heading  = regexp.MustCompile(`^#{1,6}\s+`)
	link     = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
)

// summaryOpening is what a summary opens with, as plain text: its first
// numbered section (the request Claude Code's summaries open with) without
// the section's name; without numbered sections, the whole summary, less
// headings of a word or three such as "Goal".
func summaryOpening(body string) string {
	lines := strings.Split(body, "\n")
	first := slices.IndexFunc(lines, func(line string) bool { return numbered.MatchString(line) })
	var out []string
	if first >= 0 {
		// The section's name ends at its colon or dash; anything after it on
		// the same line is the section's first words.
		label := plain(numbered.ReplaceAllString(lines[first], ""))
		end, width := -1, 0
		for _, sep := range []string{":", " - ", " — "} {
			if i := strings.Index(label, sep); i >= 0 && (end < 0 || i < end) {
				end, width = i, len(sep)
			}
		}
		switch {
		case end >= 0 && end <= 60:
			label = label[end+width:]
		case end < 0 && len(label) <= 60:
			// The line is the section's name alone.
			label = ""
		}
		out = append(out, label)
		for _, line := range lines[first+1:] {
			if numbered.MatchString(line) {
				break
			}
			out = append(out, plain(line))
		}
	} else {
		for _, line := range lines {
			if heading.MatchString(line) && len(strings.Fields(line)) <= 4 {
				continue
			}
			out = append(out, plain(line))
		}
	}
	return strings.Join(out, " ")
}

// plain is a line of markdown as plain text.
func plain(line string) string {
	line = heading.ReplaceAllString(strings.TrimSpace(line), "")
	line = link.ReplaceAllString(line, "$1")
	line = emphasis.ReplaceAllString(line, "")
	return strings.TrimLeft(line, "-*•> ")
}

// describeSource is where a session is described from.
type describeSource struct {
	agent string
	// transcript is the session's transcript file and size its size; index
	// is Codex's thread names; store is Cursor's database.
	transcript, index, store string
	size                     int64
	// found is a transcript found by the session's id; name is a title
	// read outside the transcript (Codex's thread name, Cursor's name).
	found, name string
}

// files are the source's files, whose sizes and times say when it changed;
// Cursor's database is the location's store.
func (s describeSource) files() model.Files {
	var out model.Files
	for _, path := range []string{s.transcript, s.index} {
		if path != "" {
			out = append(out, model.SessionFile{Path: path})
		}
	}
	return out
}

// describeFiles finds the files the session is described from, inside the
// agent's own store as its transcript is (see Transcripts.locate). A session
// the hooks named no transcript for, such as an ended one read back after a
// restart, is looked up by its id.
func (t *Transcripts) describeFiles(k key, path string) (describeSource, error) {
	home := t.home()
	src := describeSource{agent: k.agent}
	switch k.agent {
	case AgentClaudeCode:
		root := filepath.Join(home, ".claude", "projects")
		if path == "" {
			if !safeID.MatchString(k.id) {
				return src, errors.New("no transcript")
			}
			matches, _ := filepath.Glob(filepath.Join(root, "*", k.id+".jsonl"))
			if len(matches) == 0 {
				return src, errors.New("no transcript")
			}
			path, src.found = matches[0], matches[0]
		}
		main, err := inStore(root, path, func(name string) bool { return strings.HasSuffix(name, ".jsonl") })
		if err != nil {
			return src, err
		}
		src.transcript = main
	case AgentCodex:
		codexHome := filepath.Join(home, ".codex")
		if path == "" {
			found, err := findRollout(codexHome, k.id)
			if err != nil {
				return src, err
			}
			path, src.found = found, found
		}
		main, err := inStore(filepath.Join(codexHome, "sessions"), path, func(name string) bool {
			return strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl")
		})
		if err != nil {
			return src, err
		}
		src.transcript = main
		if index, err := inStore(codexHome, filepath.Join(codexHome, "session_index.jsonl"), func(name string) bool {
			return name == "session_index.jsonl"
		}); err == nil {
			src.index = index
		}
	case AgentCursor:
		store := cmp.Or(t.CursorStore, cursortranscript.DefaultStorePath())
		resolved, err := filepath.EvalSymlinks(store)
		if err != nil {
			return src, err
		}
		src.store = resolved
	default:
		return src, fmt.Errorf("sessions of %s are not described", k.agent)
	}
	if src.transcript != "" {
		info, err := os.Stat(src.transcript)
		if err != nil {
			return src, err
		}
		src.size = info.Size()
	}
	return src, nil
}

// read reads what is new of the session into scan: the transcript from where
// the last read stopped (from the start when it is another file or shrank),
// and the names kept outside it.
func (s *describeSource) read(id string, scan *scanState) error {
	if s.transcript != "" && (scan.path != s.transcript || s.size < scan.offset) {
		*scan = scanState{path: s.transcript}
	}
	switch s.agent {
	case AgentClaudeCode:
		return readClaude(s.transcript, scan.offset, min(s.size, scan.offset+scanPass), scan)
	case AgentCodex:
		if s.index != "" {
			if name, err := codexThreadName(s.index, id); err == nil {
				s.name = name
			}
		}
		return readCodex(s.transcript, scan.offset, min(s.size, scan.offset+scanPass), scan)
	case AgentCursor:
		store, err := cursortranscript.Open(s.store)
		if err != nil {
			return err
		}
		defer func() { _ = store.Close() }()
		tree, err := store.Tree(id)
		if err != nil || tree.Composer == nil || tree.Composer.Data == nil {
			return errors.New("no conversation")
		}
		s.name = tree.Composer.Data.Name
		return nil
	}
	return fmt.Errorf("sessions of %s are not described", s.agent)
}

// readClaude reads a Claude Code transcript's whole lines from from to to:
// the titles it sets (custom-title, the user's; ai-title, its own), the first
// prompt typed, the latest compaction summary and what its assistant lines
// did (tool calls, files written, latest text). scan.offset moves past the
// last whole line read.
func readClaude(path string, from, to int64, scan *scanState) error {
	return eachLine(path, from, to, scan, func(line []byte, whole bool) {
		if !whole {
			return
		}
		switch {
		case mayHaveActivity(line):
			scan.act.claudeActivity(line)
		case bytes.Contains(line, []byte(`"type":"custom-title"`)):
			var v struct {
				CustomTitle string `json:"customTitle"`
			}
			if json.Unmarshal(line, &v) == nil && strings.TrimSpace(v.CustomTitle) != "" {
				scan.custom = v.CustomTitle
			}
		case bytes.Contains(line, []byte(`"type":"ai-title"`)):
			var v struct {
				AITitle string `json:"aiTitle"`
			}
			if json.Unmarshal(line, &v) == nil && strings.TrimSpace(v.AITitle) != "" {
				scan.generated = v.AITitle
			}
		case bytes.Contains(line, []byte(`"isCompactSummary":true`)):
			if text, ok := claudeText(line, true); ok {
				scan.summary = text
			}
		case scan.prompt == "" && bytes.Contains(line, []byte(`"type":"user"`)):
			if text, ok := claudeText(line, false); ok && !strings.HasPrefix(text, "<") {
				// Commands and their output come wrapped in tags, such as
				// <command-name>.
				scan.prompt = text
			}
		}
	})
}

// claudeText is the text of a Claude Code user line: a compaction summary
// with summary, else one the user typed, not a meta line or a tool result.
func claudeText(line []byte, summary bool) (string, bool) {
	var v struct {
		Type             string `json:"type"`
		IsMeta           bool   `json:"isMeta"`
		IsCompactSummary bool   `json:"isCompactSummary"`
		Message          struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &v) != nil || v.Type != "user" || v.IsMeta || v.IsCompactSummary != summary {
		return "", false
	}
	var text string
	if json.Unmarshal(v.Message.Content, &text) != nil {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(v.Message.Content, &blocks) != nil {
			return "", false
		}
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				parts = append(parts, b.Text)
			}
		}
		text = strings.Join(parts, "\n")
	}
	text = strings.TrimSpace(text)
	return text, text != ""
}

// readCodex reads a Codex rollout's whole lines from from to to: the first
// message the user typed, what its response items did and the latest compaction that kept its summary as
// text (a compaction done by the server keeps it encrypted).
func readCodex(path string, from, to int64, scan *scanState) error {
	return eachLine(path, from, to, scan, func(line []byte, whole bool) {
		switch {
		case bytes.Contains(line, []byte(`"type":"compacted"`)):
			// The history it replaced follows the message, often past maxLine.
			if message := codexCompaction(line); strings.TrimSpace(message) != "" {
				scan.summary = message
			}
		case whole && bytes.Contains(line, []byte(`"type":"response_item"`)):
			scan.act.codexActivity(line)
		case whole && scan.prompt == "" && bytes.Contains(line, []byte(`"user_message"`)):
			var v struct {
				Type    string `json:"type"`
				Payload struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"payload"`
			}
			if json.Unmarshal(line, &v) == nil && v.Type == "event_msg" && v.Payload.Type == "user_message" {
				scan.prompt = strings.TrimSpace(v.Payload.Message)
			}
		}
	})
}

// codexCompaction is the message of a Codex compacted line, read from the
// start of the line, which may be cut: payload.message, when it comes before
// the cut.
func codexCompaction(line []byte) string {
	d := json.NewDecoder(bytes.NewReader(line))
	depth := 0
	inPayload := false
	for {
		token, err := d.Token()
		if err != nil {
			return ""
		}
		switch v := token.(type) {
		case json.Delim:
			if v == '{' || v == '[' {
				depth++
			} else {
				depth--
				if inPayload && depth < 2 {
					return ""
				}
			}
		case string:
			switch {
			case depth == 1 && v == "payload":
				inPayload = true
			case depth == 2 && inPayload && v == "message":
				var message string
				if d.Decode(&message) != nil {
					return ""
				}
				return message
			case depth == 2 && inPayload:
				// Another field's value: skip it whole.
				var skip json.RawMessage
				if d.Decode(&skip) != nil {
					return ""
				}
			}
		}
	}
}

// codexThreadName is the last name Codex's index gives the thread id.
func codexThreadName(path, id string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	needle := []byte(`"` + id + `"`)
	var name string
	var scan scanState
	err = eachLine(path, 0, min(info.Size(), scanPass), &scan, func(line []byte, whole bool) {
		if !whole || !bytes.Contains(line, needle) {
			return
		}
		var v struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
		}
		if json.Unmarshal(line, &v) == nil && v.ID == id && strings.TrimSpace(v.ThreadName) != "" {
			name = v.ThreadName
		}
	})
	return name, err
}

// eachLine calls fn with each whole line of the file between from and to,
// without its newline, and moves scan.offset past the last whole line. A line
// longer than maxLine is passed as its first maxLine bytes, with whole false.
// A line cut by to, or still being written, is read again next time.
func eachLine(path string, from, to int64, scan *scanState, fn func(line []byte, whole bool)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReaderSize(io.NewSectionReader(f, from, to-from), maxLine)
	offset := from
	for {
		line, err := r.ReadSlice('\n')
		var first []byte
		n := int64(len(line))
		if errors.Is(err, bufio.ErrBufferFull) {
			first = append([]byte(nil), line...)
		}
		for errors.Is(err, bufio.ErrBufferFull) {
			line, err = r.ReadSlice('\n')
			n += int64(len(line))
		}
		if err != nil {
			// The rest has no newline yet.
			break
		}
		offset += n
		if first != nil {
			fn(first, false)
		} else {
			fn(bytes.TrimRight(line, "\r\n"), true)
		}
	}
	scan.offset = offset
	return nil
}
