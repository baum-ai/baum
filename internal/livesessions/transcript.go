package livesessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/transcript"
	"github.com/baum-ai/baum/sessions"
	"github.com/baum-ai/baum/sessions/codextranscript"
	"github.com/baum-ai/baum/sessions/collector"
	"github.com/baum-ai/baum/sessions/collector/claudecode"
	"github.com/baum-ai/baum/sessions/collector/codex"
	"github.com/baum-ai/baum/sessions/collector/model"
	"github.com/baum-ai/baum/sessions/cursortranscript"
	"github.com/baum-ai/baum/sessions/session"
	"github.com/baum-ai/baum/sessions/vendors"
)

// Transcript bounds. A session's file is looked at (a stat of each of its
// files) at most once per RecheckEvery, however many windows ask, and read
// again only when one of them changed. Cursor keeps every conversation in one
// database that it writes all the time, and finding one conversation in it
// reads the whole index, so it is looked at less often. At most MaxCached
// transcripts are held, the least recently asked for making room.
const (
	RecheckEvery       = time.Second
	CursorRecheckEvery = 5 * time.Second
	MaxCached          = 8
)

// Agents the hooks name, as the tracker keys them.
const (
	AgentClaudeCode = "claude-code"
	AgentCodex      = "codex"
	AgentCursor     = "cursor"
)

// ErrNotTracked is a session the tracker does not list: not one of this
// machine's agent sessions, or one that ended more than EndedFor ago.
var ErrNotTracked = errors.New("no such live session")

// Unavailable is a tracked session whose transcript is not served, with the
// reason, a sentence the application shows.
type Unavailable struct{ Reason string }

func (u *Unavailable) Error() string { return u.Reason }

func unavailable(reason string) error { return &Unavailable{Reason: reason} }

// Transcripts serves the transcript of a session the Tracker lists, rendered
// as the review page renders a linked session's, while it runs.
//
// The file read is never named by a request: it is the transcript path the
// session's hooks reported, and it is read only when, its symbolic links
// resolved, it is a regular file inside that agent's own session store under
// the home directory (~/.claude/projects for Claude Code, ~/.codex/sessions
// for Codex). A Codex session whose hooks named no path is looked up in that
// store by its id; a Cursor session is read from Cursor's own database by its
// conversation id, since Cursor keeps no transcript file baum reads.
type Transcripts struct {
	Tracker *Tracker
	// Render writes a session's transcript; transcript.Renderer.Render is
	// the one every local transcript is written with.
	Render func(ctx context.Context, graph *session.Graph, meta transcript.Meta) ([]byte, error)
	// Home holds the agents' session stores; empty is the user's home.
	Home string
	// CursorStore is Cursor's database; empty is its default global store.
	CursorStore string
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	Log *slog.Logger

	mu    sync.Mutex
	cache map[key]*cached
	// titles are the listed sessions' titles (title.go).
	titles map[key]*titled
}

// cached is one session's transcript as last built.
type cached struct {
	// turn is held by the one request that looks at the files or builds;
	// the others wait for it and share its answer.
	turn    chan struct{}
	used    time.Time
	checked time.Time
	version string
	body    []byte
	err     error
	// found is the rollout a lookup by id found, so the store is scanned
	// once per session.
	found string
}

// Answer is a session's transcript as served.
type Answer struct {
	Session Session
	// Version changes whenever the transcript's files do.
	Version string
	// Body is the rendered transcript.
	Body []byte
}

func (t *Transcripts) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *Transcripts) logger() *slog.Logger {
	if t.Log != nil {
		return t.Log
	}
	return slog.Default()
}

func (t *Transcripts) home() string {
	if t.Home != "" {
		return t.Home
	}
	return transcript.UserHome()
}

// Get answers with the transcript of the tracked session agent/id. An error
// is ErrNotTracked, an *Unavailable, or the context's.
func (t *Transcripts) Get(ctx context.Context, agent, id string) (Answer, error) {
	session, path, ok := t.Tracker.Lookup(agent, id)
	if !ok {
		t.forget(agent, id)
		return Answer{}, ErrNotTracked
	}
	c := t.entry(key{agent, id})
	select {
	case c.turn <- struct{}{}:
	case <-ctx.Done():
		return Answer{}, ctx.Err()
	}
	defer func() { <-c.turn }()
	every := RecheckEvery
	if agent == AgentCursor {
		every = CursorRecheckEvery
	}
	now := t.now()
	if (c.body != nil || c.err != nil) && now.Sub(c.checked) < every {
		return Answer{Session: session, Version: c.version, Body: c.body}, c.err
	}
	c.checked = now
	src, err := t.locate(agent, id, path, c)
	if err != nil {
		c.version, c.body, c.err = "", nil, err
		return Answer{Session: session}, err
	}
	version, err := src.version()
	if err != nil {
		c.version, c.body, c.err = "", nil, unavailable("The transcript file is not there any more.")
		return Answer{Session: session}, c.err
	}
	if version == c.version && c.body != nil {
		return Answer{Session: session, Version: c.version, Body: c.body}, nil
	}
	body, err := t.build(ctx, src, transcript.Meta{ID: id, Vendor: string(src.slug), Branch: session.Branch})
	switch {
	case ctx.Err() != nil:
		// Looked at again by the next request.
		c.checked = time.Time{}
		return Answer{}, ctx.Err()
	case err != nil && c.body != nil:
		// A file caught mid-write keeps the last good transcript; the next
		// change is read again.
		t.logger().Warn("live session transcript not rebuilt", "agent", agent, "error", err)
		return Answer{Session: session, Version: c.version, Body: c.body}, nil
	case err != nil:
		var missing *Unavailable
		if !errors.As(err, &missing) {
			t.logger().Warn("live session transcript not read", "agent", agent, "error", err)
			err = unavailable("The transcript could not be read yet.")
		}
		c.err = err
		return Answer{Session: session}, c.err
	}
	c.version, c.body, c.err = version, body, nil
	return Answer{Session: session, Version: version, Body: body}, nil
}

// entry is the session's cache entry, made when there is none. The sessions
// the tracker no longer lists are dropped first, and a new entry beyond
// MaxCached drops the least recently asked for.
func (t *Transcripts) entry(k key) *cached {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cache == nil {
		t.cache = map[key]*cached{}
	}
	for other := range t.cache {
		if _, _, ok := t.Tracker.Lookup(other.agent, other.id); !ok {
			delete(t.cache, other)
		}
	}
	now := t.now()
	if c := t.cache[k]; c != nil {
		c.used = now
		return c
	}
	for len(t.cache) >= MaxCached {
		var oldest key
		var at time.Time
		for other, c := range t.cache {
			if at.IsZero() || c.used.Before(at) {
				oldest, at = other, c.used
			}
		}
		delete(t.cache, oldest)
	}
	c := &cached{turn: make(chan struct{}, 1), used: now}
	t.cache[k] = c
	return c
}

func (t *Transcripts) forget(agent, id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.cache, key{agent, id})
}

// cachedLen is how many transcripts are held, for tests.
func (t *Transcripts) cachedLen() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.cache)
}

// location is where a session's transcript is read from: files of an agent's
// store, or one conversation of Cursor's database.
type location struct {
	slug  vendors.Slug
	files model.Files
	// store and id are Cursor's database and conversation.
	store, id string
}

// version is a fingerprint of the size and modification time of every file
// the transcript is read from.
func (s location) version() (string, error) {
	paths := make([]string, 0, len(s.files)+2)
	for _, f := range s.files {
		paths = append(paths, f.Path)
	}
	if s.store != "" {
		paths = append(paths, s.store, s.store+"-wal")
	}
	hash := sha256.New()
	for i, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			// Cursor's write-ahead log comes and goes.
			if i > 0 && errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", err
		}
		_, _ = fmt.Fprintf(hash, "%s\x00%d\x00%d\n", path, info.Size(), info.ModTime().UnixNano())
	}
	return hex.EncodeToString(hash.Sum(nil)[:12]), nil
}

// locate finds the files of the session's transcript and checks that they
// are the agent's own.
func (t *Transcripts) locate(agent, id, path string, c *cached) (location, error) {
	home := t.home()
	switch agent {
	case AgentClaudeCode:
		root := filepath.Join(home, ".claude", "projects")
		if path == "" {
			return location{}, unavailable("Claude Code did not say where this session's transcript is.")
		}
		main, err := inStore(root, path, func(name string) bool { return strings.HasSuffix(name, ".jsonl") })
		if err != nil {
			return location{}, err
		}
		// Its subagents' transcripts, which must be inside the store too.
		files := model.Files{}
		for _, f := range claudecode.SessionFiles(main) {
			if f.Kind != model.KindTranscript {
				resolved, err := inStore(root, f.Path, func(string) bool { return true })
				if err != nil {
					continue
				}
				f.Path = resolved
			}
			files = append(files, f)
		}
		return location{slug: vendors.ClaudeCode, files: files}, nil
	case AgentCodex:
		root := filepath.Join(home, ".codex", "sessions")
		rollout := func(name string) bool {
			return strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl")
		}
		if path == "" {
			path = c.found
		}
		if path == "" {
			found, err := findRollout(filepath.Join(home, ".codex"), id)
			if err != nil {
				return location{}, err
			}
			path, c.found = found, found
		}
		main, err := inStore(root, path, rollout)
		if err != nil {
			return location{}, err
		}
		// The root rollout alone: finding its children scans every rollout
		// of the store, too much to do each time the session writes. The
		// mapper reads a root without them, as discovery does when it
		// cannot place them.
		return location{slug: vendors.Codex, files: codex.RootFile(main)}, nil
	case AgentCursor:
		store := t.CursorStore
		if store == "" {
			store = cursortranscript.DefaultStorePath()
		}
		resolved, err := filepath.EvalSymlinks(store)
		if err != nil {
			return location{}, unavailable("Cursor's conversation database was not found on this computer.")
		}
		if info, err := os.Stat(resolved); err != nil || !info.Mode().IsRegular() {
			return location{}, unavailable("Cursor's conversation database was not found on this computer.")
		}
		return location{slug: vendors.Cursor, store: resolved, id: id}, nil
	}
	return location{}, unavailable(fmt.Sprintf("baum cannot read the transcripts of %s sessions.", agent))
}

// inStore resolves path and reports it when it is a regular file inside the
// store root, whose name named accepts; otherwise the transcript is
// unavailable.
func inStore(root, path string, named func(string) bool) (string, error) {
	outside := unavailable("The transcript is outside the agent's session folder, so baum does not read it.")
	if !filepath.IsAbs(path) {
		return "", outside
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", unavailable("The agent's session folder was not found on this computer.")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", unavailable("The transcript file is not there any more.")
	}
	if err != nil || resolved == realRoot || !model.PathWithin(realRoot, resolved) || !named(filepath.Base(resolved)) {
		return "", outside
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", outside
	}
	return resolved, nil
}

// findRollout looks a Codex session up by its id in the store's first-line
// index, for a session whose hooks named no path.
func findRollout(codexHome, id string) (string, error) {
	index, _, err := codex.ScanStore(codexHome)
	if err != nil {
		return "", unavailable("Codex's session folder could not be read.")
	}
	for _, entry := range index {
		if entry.Class == codextranscript.ThreadRoot && entry.Meta != nil && entry.Meta.ID == id {
			return entry.Path, nil
		}
	}
	return "", unavailable("Codex has no transcript of this session on this computer.")
}

// build reads the session's graph with the mapper discovery uses and renders
// it.
func (t *Transcripts) build(ctx context.Context, src location, meta transcript.Meta) ([]byte, error) {
	found := collector.Session{Source: src.slug, SessionID: meta.ID, Files: src.files}
	if src.slug == vendors.Cursor {
		store, err := cursortranscript.Open(src.store)
		if err != nil {
			return nil, err
		}
		tree, err := store.Tree(src.id)
		_ = store.Close()
		if err != nil {
			return nil, unavailable("Cursor has no conversation of this session yet.")
		}
		body, err := cursortranscript.EncodeTreePayload(tree)
		if err != nil {
			return nil, err
		}
		found.Files = []model.SessionFile{{Data: body, Name: "cursor-tree.json", Kind: model.KindTranscript}}
	}
	graph, _, err := sessions.Map(found)
	if err != nil {
		return nil, err
	}
	return t.Render(ctx, graph, meta)
}
