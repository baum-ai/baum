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
	"strings"
	"time"

	"github.com/baum-ai/baum/sessions/collector/model"
	"github.com/baum-ai/baum/sessions/cursortranscript"
)

// Title bounds. A session's title is looked at no more than once per
// TitleRecheckEvery (CursorTitleRecheckEvery for Cursor, whose database is
// one large file it writes all the time), and its files are read again only
// when they changed. A Claude Code transcript is read from its last
// titleTail bytes, where the title it rewrites as the session goes is, and in
// full (up to maxTitleScan) only when the tail has none.
const (
	TitleRecheckEvery       = 5 * time.Second
	CursorTitleRecheckEvery = 30 * time.Second
	maxTitle                = 120
	titleTail               = 256 << 10
	maxTitleScan            = 64 << 20
	// maxPromptLine is the longest transcript line read for a first prompt:
	// a longer one is a pasted file or a tool's output, not a title.
	maxPromptLine = 64 << 10
)

// Listed is a session as the list serves it: the tracker's Session and the
// title its agent gave it.
type Listed struct {
	Session
	// Title is the session's name as its agent shows it: Claude Code's title
	// (the one the user set, else the one it generated), Codex's thread
	// name, Cursor's conversation name; else the session's first prompt.
	// One line, masked as transcripts are. Empty only when the agent's
	// files cannot be read.
	Title string `json:"title,omitempty"`
}

// titled is a session's title as last read.
type titled struct {
	checked time.Time
	version string
	title   string
	// found is the file a lookup by id found, so the store is searched once.
	found string
}

// safeID is a session id that can name a file: Claude Code and Codex ids are
// UUIDs.
var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// listed is the tracker's sessions with their titles.
func (t *Transcripts) listed() []Listed {
	sessions := t.Tracker.Sessions()
	out := make([]Listed, 0, len(sessions))
	keep := make(map[key]bool, len(sessions))
	for _, s := range sessions {
		_, path, _ := t.Tracker.Lookup(s.Agent, s.ID)
		k := key{s.Agent, s.ID}
		keep[k] = true
		out = append(out, Listed{Session: s, Title: t.title(k, path)})
	}
	t.mu.Lock()
	for k := range t.titles {
		if !keep[k] {
			delete(t.titles, k)
		}
	}
	t.mu.Unlock()
	return out
}

// title is the session's title, read again when its files changed and it was
// last looked at long enough ago.
func (t *Transcripts) title(k key, path string) string {
	t.mu.Lock()
	if t.titles == nil {
		t.titles = map[key]*titled{}
	}
	e := t.titles[k]
	if e == nil {
		e = &titled{}
		t.titles[k] = e
	}
	every := TitleRecheckEvery
	if k.agent == AgentCursor {
		every = CursorTitleRecheckEvery
	}
	now := t.now()
	if !e.checked.IsZero() && now.Sub(e.checked) < every {
		title := e.title
		t.mu.Unlock()
		return title
	}
	e.checked = now
	found := e.found
	t.mu.Unlock()

	files, err := t.titleFiles(k, cmp.Or(path, found))
	if err != nil {
		return t.cachedTitle(k)
	}
	version, err := location{files: files.files(), store: files.store}.version()
	if err != nil {
		return t.cachedTitle(k)
	}
	t.mu.Lock()
	if e.version == version && e.title != "" {
		title := e.title
		t.mu.Unlock()
		return title
	}
	t.mu.Unlock()
	title, err := files.read(k.id)
	if err != nil {
		t.logger().Debug("live session title not read", "agent", k.agent, "error", err)
		return t.cachedTitle(k)
	}
	title = t.Tracker.oneLine(title, maxTitle)
	t.mu.Lock()
	e.version, e.title, e.found = version, title, files.found
	t.mu.Unlock()
	return title
}

// cachedTitle is the title last read, after a failed read: the next request
// tries again, since a transcript can be named before it is written.
func (t *Transcripts) cachedTitle(k key) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e := t.titles[k]; e != nil {
		e.checked = time.Time{}
		return e.title
	}
	return ""
}

// titleSource is where a session's title is read from.
type titleSource struct {
	agent string
	// transcript is the session's transcript file; index is Codex's thread
	// names; store is Cursor's database.
	transcript, index, store string
	// found is a transcript found by the session's id.
	found string
}

// files are the source's files, whose sizes and times say when it changed;
// Cursor's database is the location's store.
func (s titleSource) files() model.Files {
	var out model.Files
	for _, path := range []string{s.transcript, s.index} {
		if path != "" {
			out = append(out, model.SessionFile{Path: path})
		}
	}
	return out
}

// titleFiles finds the files the session's title is read from, inside the
// agent's own store as its transcript is (see Transcripts.locate). A session
// the hooks named no transcript for, such as an ended one read back after a
// restart, is looked up by its id.
func (t *Transcripts) titleFiles(k key, path string) (titleSource, error) {
	home := t.home()
	src := titleSource{agent: k.agent}
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
		return src, fmt.Errorf("no titles for %s", k.agent)
	}
	return src, nil
}

// read is the session's title from its files.
func (s titleSource) read(id string) (string, error) {
	switch s.agent {
	case AgentClaudeCode:
		return claudeTitle(s.transcript)
	case AgentCodex:
		if s.index != "" {
			if name, err := codexThreadName(s.index, id); err == nil && name != "" {
				return name, nil
			}
		}
		return codexFirstPrompt(s.transcript)
	case AgentCursor:
		store, err := cursortranscript.Open(s.store)
		if err != nil {
			return "", err
		}
		defer func() { _ = store.Close() }()
		tree, err := store.Tree(id)
		if err != nil || tree.Composer == nil || tree.Composer.Data == nil {
			return "", errors.New("no conversation")
		}
		return tree.Composer.Data.Name, nil
	}
	return "", fmt.Errorf("no titles for %s", s.agent)
}

// claudeTitle is the title of a Claude Code transcript: the last title the
// user set (custom-title), else the last one Claude Code generated
// (ai-title), else the first prompt.
func claudeTitle(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	size := info.Size()
	if size > titleTail {
		if title, _ := scanClaude(io.NewSectionReader(f, size-titleTail, titleTail), false); title != "" {
			return title, nil
		}
	}
	title, prompt := scanClaude(io.NewSectionReader(f, 0, min(size, maxTitleScan)), true)
	return cmp.Or(title, prompt), nil
}

// scanClaude reads transcript lines for the latest titles and, with
// wantPrompt, the first prompt the user typed.
func scanClaude(r io.Reader, wantPrompt bool) (title, prompt string) {
	var custom, generated string
	eachLine(r, func(line []byte, whole bool) {
		switch {
		case bytes.Contains(line, []byte(`"type":"custom-title"`)):
			var v struct {
				CustomTitle string `json:"customTitle"`
			}
			if whole && json.Unmarshal(line, &v) == nil && strings.TrimSpace(v.CustomTitle) != "" {
				custom = v.CustomTitle
			}
		case bytes.Contains(line, []byte(`"type":"ai-title"`)):
			var v struct {
				AITitle string `json:"aiTitle"`
			}
			if whole && json.Unmarshal(line, &v) == nil && strings.TrimSpace(v.AITitle) != "" {
				generated = v.AITitle
			}
		case wantPrompt && prompt == "" && whole && len(line) <= maxPromptLine &&
			bytes.Contains(line, []byte(`"type":"user"`)):
			prompt = claudePrompt(line)
		}
	})
	return cmp.Or(custom, generated), prompt
}

// claudePrompt is the text a user line typed, or "" for a line Claude Code
// wrote: a tool result, a command's output, a meta or compaction message.
func claudePrompt(line []byte) string {
	var v struct {
		Type             string `json:"type"`
		IsMeta           bool   `json:"isMeta"`
		IsCompactSummary bool   `json:"isCompactSummary"`
		Message          struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &v) != nil || v.Type != "user" || v.IsMeta || v.IsCompactSummary {
		return ""
	}
	var text string
	if json.Unmarshal(v.Message.Content, &text) != nil {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(v.Message.Content, &blocks) != nil {
			return ""
		}
		for _, b := range blocks {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				text = b.Text
				break
			}
		}
	}
	text = strings.TrimSpace(text)
	// Commands and their output come wrapped in tags, such as <command-name>.
	if strings.HasPrefix(text, "<") {
		return ""
	}
	return text
}

// codexThreadName is the last name Codex's index gives the thread id.
func codexThreadName(path, id string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	needle := []byte(`"` + id + `"`)
	var name string
	eachLine(io.LimitReader(f, maxTitleScan), func(line []byte, whole bool) {
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
	return name, nil
}

// codexFirstPrompt is the first message the user typed in a Codex rollout.
func codexFirstPrompt(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	var prompt string
	eachLine(io.LimitReader(f, maxTitleScan), func(line []byte, whole bool) {
		if prompt != "" || !whole || len(line) > maxPromptLine || !bytes.Contains(line, []byte(`"user_message"`)) {
			return
		}
		var v struct {
			Type    string `json:"type"`
			Payload struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &v) == nil && v.Type == "event_msg" && v.Payload.Type == "user_message" {
			prompt = strings.TrimSpace(v.Payload.Message)
		}
	})
	return prompt, nil
}

// eachLine calls fn with each line of r, without its newline. A line longer
// than the reader's buffer is passed once, cut, with whole false.
func eachLine(r io.Reader, fn func(line []byte, whole bool)) {
	br := bufio.NewReaderSize(r, maxPromptLine+1)
	for {
		line, err := br.ReadSlice('\n')
		whole := !errors.Is(err, bufio.ErrBufferFull)
		fn(bytes.TrimRight(line, "\r\n"), whole)
		for errors.Is(err, bufio.ErrBufferFull) {
			_, err = br.ReadSlice('\n')
		}
		if err != nil {
			return
		}
	}
}
