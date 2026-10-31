package livesessions

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
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
	// Enders stop a session's agent, by agent (end.go); an agent without
	// one cannot be ended from the app.
	Enders map[string]Ender
	// Senders put a message from the app into a session, by agent
	// (message.go); an agent without one cannot be messaged.
	Senders map[string]Sender
	// Focus brings a running session forward; nil serves no focus route and
	// lists no session as one it can bring forward.
	Focus Focuser

	mu    sync.Mutex
	cache map[key]*cached
	// described is what the listed sessions' files say of them (title.go).
	described map[key]*described
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
	// images, source and vendor are what Answer says of them.
	images map[string]promptImages
	source string
	vendor vendors.Slug
	// found is the rollout a lookup by id found, so the store is scanned
	// once per session.
	found string
	// untracked is an entry Read made for a session the tracker may not
	// list: only the least recently used rule drops it.
	untracked bool
}

// Answer is a session's transcript as served.
type Answer struct {
	Session Session
	// Version changes whenever the transcript's files do.
	Version string
	// Body is the rendered transcript.
	Body []byte
	// Images are the media types of the images pasted with each prompt, by
	// its turn, in order; nil when none were.
	Images map[string][]string

	// images says where each prompt's images are in source, the transcript
	// file of vendor's they are read from (Image).
	images map[string]promptImages
	source string
	vendor vendors.Slug
}

// answer is the cached transcript as served.
func (c *cached) answer(session Session) Answer {
	return Answer{
		Session: session, Version: c.version, Body: c.body,
		Images: imageTypesByTurn(c.images), images: c.images, source: c.source, vendor: c.vendor,
	}
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
	return t.serve(ctx, session, path, false)
}

// Read answers with the transcript of the session agent/id whether the
// tracker lists it or not, such as one that ended long ago: from path, the
// transcript its hooks named when it was captured (empty when none did), on
// branch. While the tracker lists it, its session and path are the
// tracker's. The file read is held to the same rules as Get's. An error is
// an *Unavailable or the context's.
func (t *Transcripts) Read(ctx context.Context, agent, id, path, branch string) (Answer, error) {
	if t.Tracker != nil {
		if session, tracked, ok := t.Tracker.Lookup(agent, id); ok {
			return t.serve(ctx, session, cmp.Or(tracked, path), false)
		}
	}
	return t.serve(ctx, Session{Agent: agent, ID: id, State: StateEnded, Branch: branch}, path, true)
}

// serve answers with the session's transcript from path, building it again
// only when its files changed.
func (t *Transcripts) serve(ctx context.Context, session Session, path string, untracked bool) (Answer, error) {
	agent, id := session.Agent, session.ID
	c := t.entry(key{agent, id}, untracked)
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
		return c.answer(session), c.err
	}
	c.checked = now
	src, err := t.locate(agent, id, path, c)
	if err != nil {
		c.version, c.body, c.err = "", nil, err
		return Answer{Session: session}, err
	}
	version, err := src.Version()
	if err != nil {
		c.version, c.body, c.err = "", nil, unavailable("The transcript file is not there any more.")
		return Answer{Session: session}, c.err
	}
	if version == c.version && c.body != nil {
		return c.answer(session), nil
	}
	body, images, err := t.build(ctx, src, transcript.Meta{ID: id, Vendor: string(src.Vendor), Branch: session.Branch})
	switch {
	case ctx.Err() != nil:
		// Looked at again by the next request.
		c.checked = time.Time{}
		return Answer{}, ctx.Err()
	case err != nil && c.body != nil:
		// A file caught mid-write keeps the last good transcript; the next
		// change is read again.
		t.logger().Warn("live session transcript not rebuilt", "agent", agent, "error", err)
		return c.answer(session), nil
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
	c.images, c.source, c.vendor = images, src.main, src.Vendor
	return c.answer(session), nil
}

// entry is the session's cache entry, made when there is none. The sessions
// the tracker no longer lists are dropped first, but for those Read asked
// for, and a new entry beyond MaxCached drops the least recently asked for.
func (t *Transcripts) entry(k key, untracked bool) *cached {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cache == nil {
		t.cache = map[key]*cached{}
	}
	for other, c := range t.cache {
		if c.untracked || other == k || t.Tracker == nil {
			continue
		}
		if _, _, ok := t.Tracker.Lookup(other.agent, other.id); !ok {
			delete(t.cache, other)
		}
	}
	now := t.now()
	if c := t.cache[k]; c != nil {
		c.used = now
		c.untracked = untracked
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
	c := &cached{turn: make(chan struct{}, 1), used: now, untracked: untracked}
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

// Location is where a session's transcript is read from: files of an agent's
// store, or one conversation of Cursor's database (Locate).
type Location struct {
	Vendor vendors.Slug
	// ID is the session's id: Cursor's conversation.
	ID    string
	files model.Files
	// main is the session's own transcript file among files.
	main string
	// store is Cursor's database.
	store string
}

// Path is the session's main transcript file, empty for a Cursor
// conversation.
func (s Location) Path() string { return s.main }

// Size is the total size of the session's own files; zero for a Cursor
// conversation, which shares one database with every other.
func (s Location) Size() (int64, error) {
	var total int64
	for _, f := range s.files {
		info, err := os.Stat(f.Path)
		if err != nil {
			return 0, err
		}
		total += info.Size()
	}
	return total, nil
}

// Version is a fingerprint of the size and modification time of every file
// the transcript is read from.
func (s Location) Version() (string, error) {
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
// are the agent's own. A Codex rollout found by id is remembered in c.
func (t *Transcripts) locate(agent, id, path string, c *cached) (Location, error) {
	if agent == AgentCodex && path == "" {
		path = c.found
	}
	cursorStore := t.CursorStore
	if cursorStore == "" {
		cursorStore = cursortranscript.DefaultStorePath()
	}
	loc, err := Locate(t.home(), cursorStore, agent, id, path)
	if err == nil && agent == AgentCodex && path == "" {
		c.found = loc.main
	}
	return loc, err
}

// Locate finds the files of the transcript of the agent's session id, whose
// hooks named path (empty when none did), and checks that they are the
// agent's own: inside its session store under home, its symbolic links
// resolved. A Codex session without a path is looked up in its store by id;
// a Cursor session is read from cursorStore. Errors are *Unavailable.
func Locate(home, cursorStore, agent, id, path string) (Location, error) {
	switch agent {
	case AgentClaudeCode:
		root := filepath.Join(home, ".claude", "projects")
		if path == "" {
			// A session read back from StateFile keeps no path; its
			// transcript is found by its id.
			found, ok := claudeTranscriptByID(root, id)
			if !ok {
				return Location{}, unavailable("Claude Code did not say where this session's transcript is.")
			}
			path = found
		}
		main, err := inStore(root, path, func(name string) bool { return strings.HasSuffix(name, ".jsonl") })
		if err != nil {
			return Location{}, err
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
		return Location{Vendor: vendors.ClaudeCode, ID: id, files: files, main: main}, nil
	case AgentCodex:
		root := filepath.Join(home, ".codex", "sessions")
		rollout := func(name string) bool {
			return strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl")
		}
		if path == "" {
			found, err := findRollout(filepath.Join(home, ".codex"), id)
			if err != nil {
				return Location{}, err
			}
			path = found
		}
		main, err := inStore(root, path, rollout)
		if err != nil {
			return Location{}, err
		}
		// The root rollout alone: finding its children scans every rollout
		// of the store, too much to do each time the session writes. The
		// mapper reads a root without them, as discovery does when it
		// cannot place them.
		return Location{Vendor: vendors.Codex, ID: id, files: codex.RootFile(main), main: main}, nil
	case AgentCursor:
		resolved, err := filepath.EvalSymlinks(cursorStore)
		if err != nil {
			return Location{}, unavailable("Cursor's conversation database was not found on this computer.")
		}
		if info, err := os.Stat(resolved); err != nil || !info.Mode().IsRegular() {
			return Location{}, unavailable("Cursor's conversation database was not found on this computer.")
		}
		return Location{Vendor: vendors.Cursor, ID: id, store: resolved}, nil
	}
	return Location{}, unavailable(fmt.Sprintf("baum cannot read the transcripts of %s sessions.", agent))
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

// findRollout looks a Codex session up by its id, for a session whose hooks
// named no path (one restored from StateFile): Codex names each rollout
// after its thread, so it is the one rollout under sessions whose name ends in
// the id and whose first line says it is that thread's root. Only names are
// walked, not the store's other rollouts, which Codex compresses as they age
// (.jsonl.zst, which baum does not read).
func findRollout(codexHome, id string) (string, error) {
	if !safeID.MatchString(id) {
		return "", unavailable("Codex has no transcript of this session on this computer.")
	}
	var found string
	_ = filepath.WalkDir(filepath.Join(codexHome, "sessions"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, "-"+id+".jsonl") || !rootOf(path, id) {
			return nil
		}
		found = path
		return fs.SkipAll
	})
	if found == "" {
		return "", unavailable("Codex has no transcript of this session on this computer.")
	}
	return found, nil
}

// rootOf reports whether the rollout at path starts thread id's root.
func rootOf(path, id string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	line, err := codextranscript.DecodeFirst(f)
	if err != nil || line.SessionMeta == nil || line.SessionMeta.ID != id {
		return false
	}
	class, err := codextranscript.ClassifyThread([]codextranscript.Line{line})
	return err == nil && class == codextranscript.ThreadRoot
}

// build reads the session's graph with the mapper discovery uses and renders
// it, and finds the images pasted with its prompts. Images that cannot be
// read leave the transcript without them.
func (t *Transcripts) build(ctx context.Context, src Location, meta transcript.Meta) ([]byte, map[string]promptImages, error) {
	graph, err := src.Graph()
	if err != nil {
		return nil, nil, err
	}
	body, err := t.Render(ctx, graph, meta)
	if err != nil {
		return nil, nil, err
	}
	images, err := findImages(src, graph)
	if err != nil {
		t.logger().Warn("live session images not read", "agent", meta.Vendor, "error", err)
	}
	return body, images, nil
}

// Graph reads the session's graph with the mapper discovery uses.
func (s Location) Graph() (*session.Graph, error) {
	found := collector.Session{Source: s.Vendor, SessionID: s.ID, Files: s.files}
	if s.Vendor == vendors.Cursor {
		store, err := cursortranscript.Open(s.store)
		if err != nil {
			return nil, err
		}
		tree, err := store.Tree(s.ID)
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
	return graph, err
}
