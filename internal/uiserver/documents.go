package uiserver

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/viewserver"
	"github.com/baum-ai/baum/sessions"
)

const (
	// maxCachedDocuments and maxCachedBytes bound the viewers kept for
	// documents, by count and by what they hold (viewserver.CachedBytes); the
	// least recently used goes first. The one asked for last stays, whatever
	// its size.
	maxCachedDocuments = 16
	maxCachedBytes     = 256 << 20
	// documentIdle is how long a viewer nobody asks for is kept.
	documentIdle  = 10 * time.Minute
	maxLocalReads = 4
)

// cachedDocument is one document's viewer. It is built once, outside the
// cache's lock, and every request for the document while it builds waits for
// that build. An evicted viewer is closed once the last request holding it
// releases it.
type cachedDocument struct {
	key string
	// revisions are the store revision keys naming the same bytes.
	revisions []string
	ready     chan struct{}
	server    *viewserver.Server
	err       error
	used      uint64
	idleSince time.Time
	active    int
	evicted   bool
	shut      bool
}

type documentCache struct {
	mu sync.Mutex
	// entries are the viewers by clone and document content; revisions the
	// same viewers by clone and store revision (revisionKey), so a request
	// whose store revision is known finds its viewer without reading the
	// document.
	entries   map[string]*cachedDocument
	revisions map[string]*cachedDocument
	clock     uint64
	closed    bool
	// sweep drops idle viewers while any is kept.
	sweep           *time.Timer
	idle            time.Duration
	maxEntries      int
	maxBytes        int64
	now             func() time.Time
	requests, reads chan struct{}
	resolve         func(context.Context, locallink.Request) (locallink.Result, error)
	// sessions are the session stores linking reads; zero reads the defaults.
	sessions sessions.Options
	// patch is shared by every document, so versions of a pull request that
	// compare the same commits share a read that is running.
	patch func(context.Context, locallink.PatchRequest) (locallink.PatchResult, error)
	// patcher answers patch; close closes the clones it keeps open.
	patcher *locallink.Patcher
	// built counts viewer builds, and onClose sees each viewer closed, for
	// tests.
	built   int
	onClose func(*viewserver.Server)
}

func newDocumentCache() *documentCache {
	patcher := new(locallink.Patcher)
	return &documentCache{
		entries: map[string]*cachedDocument{}, revisions: map[string]*cachedDocument{},
		idle: documentIdle, maxEntries: maxCachedDocuments, maxBytes: maxCachedBytes, now: time.Now,
		requests: make(chan struct{}, maxLocalReads), reads: make(chan struct{}, maxLocalReads),
		resolve: locallink.ResolveSummary, patch: patcher.Patch, patcher: patcher,
	}
}
func acquire(ctx context.Context, slots chan struct{}) bool {
	select {
	case slots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// revisionKey names a stored version's document by its store revision, or is
// empty when the store cannot vouch for one (store.Version.Revision).
func revisionKey(clone repos.Clone, s *store.Store, key store.Key, version int, revision int64) string {
	if revision == 0 {
		return ""
	}
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%d\x00%d", clone.ID, clone.Repository, clone.CommonDir, clone.Path, s.Root(), strings.ToLower(key.Repository), key.Number, version, revision)
}

// cached returns the viewer of the document revision names, when one is kept
// or building; ok is false otherwise, and always for an empty revision.
func (c *documentCache) cached(ctx context.Context, revision string) (server *viewserver.Server, release func(), ok bool, err error) {
	if revision == "" {
		return nil, nil, false, nil
	}
	c.mu.Lock()
	entry := c.revisions[revision]
	if c.closed || entry == nil {
		c.mu.Unlock()
		return nil, nil, false, nil
	}
	c.hold(entry)
	c.mu.Unlock()
	server, release, err = c.wait(ctx, entry)
	return server, release, true, err
}

// get returns the viewer of document, building it unless it is kept or
// building, and records revision (a revisionKey, or empty) as naming it.
func (c *documentCache) get(ctx context.Context, clone repos.Clone, document []byte, revision string) (*viewserver.Server, func(), error) {
	key := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%x", clone.ID, clone.Repository, clone.CommonDir, clone.Path, sha256.Sum256(document))
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, nil, errors.New("document cache closed")
	}
	entry := c.entries[key]
	build := entry == nil
	if build {
		entry = &cachedDocument{key: key, ready: make(chan struct{})}
		c.entries[key] = entry
		c.built++
	}
	if revision != "" && c.revisions[revision] != entry {
		if previous := c.revisions[revision]; previous != nil {
			previous.revisions = removeString(previous.revisions, revision)
		}
		c.revisions[revision] = entry
		entry.revisions = append(entry.revisions, revision)
	}
	c.hold(entry)
	shut := c.evict(entry)
	c.mu.Unlock()
	c.closeAll(shut)
	if build {
		server, err := c.newServer(clone, document)
		c.mu.Lock()
		entry.server, entry.err = server, err
		close(entry.ready)
		if err != nil {
			c.remove(entry)
		}
		shut = c.evict(entry)
		c.mu.Unlock()
		c.closeAll(shut)
	}
	return c.wait(ctx, entry)
}

// hold marks entry used by one more request; the caller holds c.mu.
func (c *documentCache) hold(entry *cachedDocument) {
	c.clock++
	entry.used = c.clock
	entry.active++
}

// wait returns entry's viewer once it is built; the request holds entry until
// release, or until wait fails.
func (c *documentCache) wait(ctx context.Context, entry *cachedDocument) (*viewserver.Server, func(), error) {
	select {
	case <-entry.ready:
	case <-ctx.Done():
		c.release(entry)
		return nil, nil, ctx.Err()
	}
	if entry.err != nil {
		c.release(entry)
		return nil, nil, entry.err
	}
	var once sync.Once
	return entry.server, func() { once.Do(func() { c.release(entry) }) }, nil
}

func (c *documentCache) release(entry *cachedDocument) {
	c.mu.Lock()
	entry.active--
	if entry.active == 0 {
		entry.idleSince = c.now()
	}
	var shut []*viewserver.Server
	if entry.evicted {
		shut = c.shutdown(entry, shut)
	} else {
		// The request may have added answers to the viewer, so the bounds
		// are checked again, sparing the viewer asked for last.
		shut = c.evict(c.recent())
	}
	c.mu.Unlock()
	c.closeAll(shut)
}

// recent is the kept viewer asked for last; the caller holds c.mu.
func (c *documentCache) recent() *cachedDocument {
	var last *cachedDocument
	for _, entry := range c.entries {
		if last == nil || entry.used > last.used {
			last = entry
		}
	}
	return last
}

// evict drops idle viewers, then the least recently used ones over the count
// or byte bound, never keep, and returns the viewers to close now; the caller
// holds c.mu and closes them after unlocking.
func (c *documentCache) evict(keep *cachedDocument) []*viewserver.Server {
	var shut []*viewserver.Server
	now := c.now()
	for _, entry := range c.entries {
		if entry != keep && entry.active == 0 && now.Sub(entry.idleSince) >= c.idle {
			shut = c.drop(entry, shut)
		}
	}
	var total int64
	for _, entry := range c.entries {
		total += entry.size()
	}
	for len(c.entries) > c.maxEntries || (total > c.maxBytes && len(c.entries) > 1) {
		var oldest *cachedDocument
		for _, entry := range c.entries {
			if entry != keep && (oldest == nil || entry.used < oldest.used) {
				oldest = entry
			}
		}
		if oldest == nil {
			break
		}
		total -= oldest.size()
		shut = c.drop(oldest, shut)
	}
	c.schedule()
	return shut
}

// size is what a built viewer holds; 0 while it builds.
func (entry *cachedDocument) size() int64 {
	select {
	case <-entry.ready:
	default:
		return 0
	}
	if entry.server == nil {
		return 0
	}
	return entry.server.CachedBytes()
}

func (c *documentCache) drop(entry *cachedDocument, shut []*viewserver.Server) []*viewserver.Server {
	c.remove(entry)
	return c.shutdown(entry, shut)
}

// remove forgets entry, so no request finds it again.
func (c *documentCache) remove(entry *cachedDocument) {
	entry.evicted = true
	if c.entries[entry.key] == entry {
		delete(c.entries, entry.key)
	}
	for _, revision := range entry.revisions {
		if c.revisions[revision] == entry {
			delete(c.revisions, revision)
		}
	}
	entry.revisions = nil
}

// shutdown adds an evicted entry's viewer to shut once no request holds it.
func (c *documentCache) shutdown(entry *cachedDocument, shut []*viewserver.Server) []*viewserver.Server {
	if entry.active > 0 || entry.shut || entry.server == nil {
		return shut
	}
	entry.shut = true
	return append(shut, entry.server)
}

// schedule runs sweep when the first kept viewer may have been idle long
// enough; the caller holds c.mu.
func (c *documentCache) schedule() {
	if c.closed || c.sweep != nil {
		return
	}
	var next time.Time
	for _, entry := range c.entries {
		if entry.active == 0 && (next.IsZero() || entry.idleSince.Before(next)) {
			next = entry.idleSince
		}
	}
	if next.IsZero() {
		// Nothing is idle: a release schedules the sweep.
		return
	}
	c.sweep = time.AfterFunc(max(next.Add(c.idle).Sub(c.now()), 0), func() {
		c.mu.Lock()
		c.sweep = nil
		shut := c.evict(nil)
		c.mu.Unlock()
		c.closeAll(shut)
	})
}

func (c *documentCache) closeAll(servers []*viewserver.Server) {
	for _, server := range servers {
		server.Close()
		if c.onClose != nil {
			c.onClose(server)
		}
	}
}

func removeString(list []string, s string) []string {
	for i, item := range list {
		if item == s {
			return append(list[:i], list[i+1:]...)
		}
	}
	return list
}

func (c *documentCache) newServer(clone repos.Clone, document []byte) (*viewserver.Server, error) {
	return viewserver.New(viewserver.Options{Document: document, Sessions: func(ctx context.Context, v viewserver.Review) (locallink.Result, error) {
		if !acquire(ctx, c.reads) {
			return locallink.Result{}, ctx.Err()
		}
		defer func() { <-c.reads }()
		result, err := c.resolve(ctx, locallink.Request{Dir: clone.Path, Repository: v.Repository, Head: v.Head, Base: v.Base, MergeBase: v.MergeBase, Sessions: c.sessions})
		if load := result.LoadGraph; load != nil {
			result.LoadGraph = func(ctx context.Context, id string) (locallink.Result, error) {
				if !acquire(ctx, c.reads) {
					return locallink.Result{}, ctx.Err()
				}
				defer func() { <-c.reads }()
				return load(ctx, id)
			}
		}
		return result, err
	}, Patch: func(ctx context.Context, v viewserver.Review, path string) (locallink.PatchResult, error) {
		if !acquire(ctx, c.reads) {
			return locallink.PatchResult{}, ctx.Err()
		}
		defer func() { <-c.reads }()
		return c.patch(ctx, locallink.PatchRequest{Dir: clone.Path, Repository: v.Repository, Head: v.Head, Base: v.Base, MergeBase: v.MergeBase, Path: path})
	}, Commit: func(ctx context.Context, v viewserver.Review, oid string) (locallink.CommitDetail, error) {
		if !acquire(ctx, c.reads) {
			return locallink.CommitDetail{}, ctx.Err()
		}
		defer func() { <-c.reads }()
		return locallink.ReadCommit(ctx, locallink.CommitRequest{Dir: clone.Path, Repository: v.Repository, OID: oid})
	}, CommitPatch: func(ctx context.Context, v viewserver.Review, commit locallink.CommitDetail, file locallink.CommitFile) (locallink.PatchResult, error) {
		if !acquire(ctx, c.reads) {
			return locallink.PatchResult{}, ctx.Err()
		}
		defer func() { <-c.reads }()
		return locallink.CommitPatch(ctx, locallink.CommitPatchRequest{Dir: clone.Path, Repository: v.Repository, Commit: commit, File: file})
	}, File: func(ctx context.Context, v viewserver.Review, path string) (locallink.FileResult, error) {
		if !acquire(ctx, c.reads) {
			return locallink.FileResult{}, ctx.Err()
		}
		defer func() { <-c.reads }()
		return locallink.ReadFile(ctx, locallink.FileRequest{Dir: clone.Path, Repository: v.Repository, Head: v.Head, Path: path})
	}})
}

// close closes every viewer, those requests hold too, as the daemon does
// when it stops.
func (c *documentCache) close() {
	c.mu.Lock()
	c.closed = true
	if c.sweep != nil {
		c.sweep.Stop()
		c.sweep = nil
	}
	var shut []*viewserver.Server
	for _, entry := range c.entries {
		c.remove(entry)
		if entry.server != nil && !entry.shut {
			entry.shut = true
			shut = append(shut, entry.server)
		}
	}
	c.mu.Unlock()
	c.closeAll(shut)
	c.patcher.Close()
}

func (c *documentCache) invalidateSessions() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, entry := range c.entries {
		if entry.server != nil {
			entry.server.InvalidateSessions()
		}
	}
}
