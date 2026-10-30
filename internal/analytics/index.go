package analytics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/appdb"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/sessions"
	"github.com/baum-ai/baum/sessions/collector"
	"github.com/baum-ai/baum/sessions/session"
)

const (
	// Retention bounds the index: a session last written longer ago is not
	// read, and its entry is dropped.
	Retention = 90 * 24 * time.Hour
	// RefreshInterval is how often the index looks for new and changed
	// sessions while the daemon runs; a session ending also starts a look.
	RefreshInterval = 5 * time.Minute
	// indexVersion changes whenever Entry's meaning does, which rebuilds it.
	indexVersion = 1
	// saveEvery is how many sessions are read between saves of a first
	// build, so a restart resumes rather than starts over.
	saveEvery = 25
)

// Clones lists the registered clones; *repos.Registry is one.
type Clones interface {
	List(ctx context.Context) ([]repos.Clone, error)
}

// Stores overrides where sessions are read from, for tests; the zero value
// reads each agent's default store in the home directory.
type Stores struct {
	ClaudeProjectsDir string
	CodexSessionsDir  string
	CursorStorePaths  []string
}

// Progress is how far a build has come: Done of Total sessions read or found
// unchanged. Complete is set once a whole pass has finished in this process.
type Progress struct {
	Done     int  `json:"done"`
	Total    int  `json:"total"`
	Complete bool `json:"complete"`
}

// Index keeps one Entry per session file recorded in a registered clone in the
// last Retention, in the application database. A refresh reads
// only the files whose size or modification time changed since it last read
// them, and drops the entries of files that are gone, too old, or no longer in
// a registered clone. Requests read what it has while it works.
type Index struct {
	Path   string
	Clones Clones
	Stores Stores
	Log    *slog.Logger
	Now    func() time.Time
	// mapSession maps a found session; tests count its calls.
	mapSession func(collector.Session) (*session.Graph, []string, error)

	mu      sync.RWMutex
	entries map[string]Entry
	// bands are the price list thresholds the entries were summed with.
	bands    []int64
	progress Progress
	loaded   bool
	wake     chan struct{}
	once     sync.Once
}

type indexFile struct {
	Version int              `json:"version"`
	Bands   []int64          `json:"bands"`
	Entries map[string]Entry `json:"entries"`
}

func (x *Index) now() time.Time {
	if x.Now != nil {
		return x.Now()
	}
	return time.Now()
}

func (x *Index) wakeChannel() chan struct{} {
	x.once.Do(func() { x.wake = make(chan struct{}, 1) })
	return x.wake
}

// Trigger asks for a refresh soon, as when an agent session ended.
func (x *Index) Trigger() {
	select {
	case x.wakeChannel() <- struct{}{}:
	default:
	}
}

// Run refreshes at once, then every RefreshInterval and on Trigger, until
// ctx ends.
func (x *Index) Run(ctx context.Context) {
	ticker := time.NewTicker(RefreshInterval)
	defer ticker.Stop()
	for {
		if err := x.Refresh(ctx); err != nil && ctx.Err() == nil && x.Log != nil {
			x.Log.Warn("analytics index not refreshed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-x.wakeChannel():
		}
	}
}

// Snapshot returns the entries the index holds and how far it has come.
func (x *Index) Snapshot() ([]Entry, Progress) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.load()
	out := make([]Entry, 0, len(x.entries))
	for _, e := range x.entries {
		out = append(out, e)
	}
	return out, x.progress
}

// load reads the cached document once; the caller holds mu. A document for another
// version or price list thresholds is ignored, so every session is read again.
func (x *Index) load() {
	if x.loaded {
		return
	}
	x.loaded = true
	x.entries = map[string]Entry{}
	x.bands = pricing.Active().PromptBands()
	raw, err := appdb.Read(x.Path, "cache", "analytics-index")
	if err != nil {
		return
	}
	var stored indexFile
	if json.Unmarshal(raw, &stored) != nil || stored.Version != indexVersion || !slices.Equal(stored.Bands, pricing.Active().PromptBands()) {
		return
	}
	for path, e := range stored.Entries {
		x.entries[path] = e
	}
}

func (x *Index) save() error {
	x.mu.RLock()
	raw, err := json.Marshal(indexFile{Version: indexVersion, Bands: x.bands, Entries: x.entries})
	x.mu.RUnlock()
	if err != nil {
		return err
	}
	return appdb.Write(x.Path, "cache", "analytics-index", raw)
}

// found is one session file discovery returned, with the clone it is in.
type found struct {
	session collector.Session
	clone   repos.Clone
	key     string
}

// Refresh brings the index up to date with the session stores. Only sessions
// whose working directory is inside a registered clone (any of its
// worktrees) are read; a clone that is not available keeps its entries
// until it is back or unregistered.
func (x *Index) Refresh(ctx context.Context) error {
	x.mu.Lock()
	x.load()
	// A newer price list with other thresholds groups usage differently.
	if bands := pricing.Active().PromptBands(); !slices.Equal(x.bands, bands) {
		x.entries, x.bands = map[string]Entry{}, bands
	}
	x.mu.Unlock()

	clones, err := x.Clones.List(ctx)
	if err != nil {
		return err
	}
	cutoff := x.now().Add(-Retention)
	registered := map[string]bool{}
	scanned := map[string]bool{}
	seen := map[string]bool{}
	var all []found
	for _, clone := range clones {
		registered[clone.ID] = true
		if !clone.Available {
			continue
		}
		var worktrees []string
		if listed, err := gitlocal.Worktrees(ctx, clone.Path); err == nil {
			for _, w := range listed {
				worktrees = append(worktrees, w.Path)
			}
		}
		list, _, err := collector.FindSessions(clone.Path, collector.FindOptions{
			ClaudeProjectsDir: x.Stores.ClaudeProjectsDir, CodexSessionsDir: x.Stores.CodexSessionsDir, CursorStorePaths: x.Stores.CursorStorePaths,
			Worktrees: worktrees, Since: cutoff, ExcludeNestedClones: true,
		})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if x.Log != nil {
				x.Log.Warn("analytics: sessions of a clone not read", "clone", clone.ID, "error", err)
			}
			continue
		}
		scanned[clone.ID] = true
		for _, s := range list {
			// Two registrations of one clone's worktrees find the same file;
			// the first keeps it.
			if s.ModifiedAt.Before(cutoff) || seen[s.Path] {
				continue
			}
			seen[s.Path] = true
			all = append(all, found{session: s, clone: clone, key: fingerprint(s)})
		}
	}

	x.mu.Lock()
	for path, e := range x.entries {
		drop := !registered[e.Clone] || e.ModifiedAt.Before(cutoff) || (scanned[e.Clone] && !seen[path])
		if drop {
			delete(x.entries, path)
		}
	}
	var todo []found
	done := 0
	for _, f := range all {
		if e, ok := x.entries[f.session.Path]; ok && e.Key == f.key && e.Clone == f.clone.ID {
			done++
			continue
		}
		todo = append(todo, f)
	}
	// Once a pass has finished, later ones are small and the page does not
	// say it is reading.
	x.progress = Progress{Done: done, Total: len(all), Complete: x.progress.Complete}
	x.mu.Unlock()

	for i, f := range todo {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry := x.read(ctx, f)
		x.mu.Lock()
		x.entries[f.session.Path] = entry
		x.progress.Done++
		x.mu.Unlock()
		if (i+1)%saveEvery == 0 {
			if err := x.save(); err != nil {
				return err
			}
		}
	}
	x.mu.Lock()
	x.progress = Progress{Done: len(all), Total: len(all), Complete: true}
	x.mu.Unlock()
	return x.save()
}

// read maps one session and reduces it to its entry. A session that does not
// map keeps an entry that counts it with unknown usage, so a broken file is
// not read again until it changes.
func (x *Index) read(_ context.Context, f found) Entry {
	read := x.mapSession
	if read == nil {
		read = mapSession
	}
	graph, _, err := read(f.session)
	s := sessions.Session{Source: f.session.Source, ID: f.session.SessionID, ModifiedAt: f.session.ModifiedAt, Graph: graph}
	if err != nil {
		s.Graph = nil
		if x.Log != nil {
			x.Log.Debug("analytics: session not mapped", "source", f.session.Source, "error", err)
		}
	}
	x.mu.RLock()
	bands := x.bands
	x.mu.RUnlock()
	entry := summarize(s, bands)
	entry.Key, entry.Clone, entry.Repository = f.key, f.clone.ID, strings.ToLower(f.clone.Repository)
	return entry
}

// mapSession is how a session is read: the same mapping every other reader
// of the stores uses.
var mapSession = sessions.Map

// fingerprint identifies the state of a session's files: each file's size
// and modification time, or a digest of content the source generated.
func fingerprint(s collector.Session) string {
	h := sha256.New()
	for _, file := range s.Files {
		if file.Data != nil {
			sum := sha256.Sum256(file.Data)
			_, _ = fmt.Fprintf(h, "%s:data:%x\n", file.Name, sum)
			continue
		}
		info, err := os.Stat(file.Path)
		if err != nil {
			_, _ = fmt.Fprintf(h, "%s:missing\n", file.Path)
			continue
		}
		_, _ = fmt.Fprintf(h, "%s:%d:%d\n", file.Path, info.Size(), info.ModTime().UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}
