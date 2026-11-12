package reviewindex

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/store"
)

// A Cache's builds read only the repositories whose inputs changed. It keeps
// each repository's share of the last snapshot (a part: its rows, problems
// and next change), under a key taken before the part was read: the
// repository's discovery Revision, its clones as the registry lists them, and
// a stamp of each store it reads. A build that finds the same key, before the
// part's next change, uses the part as it is; anything else reads the
// repository again. A key is taken before reading, so a write landing during
// the read moves the key past what is kept, and the next build reads again.
//
// A part read with an error, or under a key that cannot vouch for its inputs
// (no Revision, a store file modified within racyWindow, a stat that failed)
// is never reused. Every part is read again once MaxAge has passed since the
// build that last read them all, and none is used once MaxAge has passed
// since it was read, so what no key covers (another process writing the
// discovery cache) is still seen within MaxAge, as before.
type parts struct {
	mu     sync.Mutex
	byRepo map[string]*part
	// fullAt is when a build last read every repository.
	fullAt time.Time
}

type part struct {
	key  partKey
	read time.Time
	// next is the earliest instant after read at which reading again could
	// give other rows with nothing written; zero when there is none.
	next     time.Time
	entries  []Entry
	problems []Problem
}

type partKey struct {
	revision discovery.Revision
	inputs   string
	// files are the store files inputs stamps, compared by identity too: a
	// file replaced by another of the same size and time is another file.
	files []os.FileInfo
}

func (k partKey) same(o partKey) bool {
	if k.revision != o.revision || k.inputs != o.inputs || len(k.files) != len(o.files) {
		return false
	}
	for i := range k.files {
		if !os.SameFile(k.files[i], o.files[i]) {
			return false
		}
	}
	return true
}

// racyWindow is how recently a store file may have been modified for its
// stamp to vouch for its content: a later write could keep the same size and
// time on a coarse clock. Discovery trusts its cache's stamp the same way.
const racyWindow = 3 * time.Second

// reusable reports whether a build at now may use kept parts, and the
// instant at which every part must be read again.
func (k *parts) reusable(now time.Time) (bool, time.Time) {
	if k == nil {
		return false, time.Time{}
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.fullAt.IsZero() || now.Before(k.fullAt) || now.Sub(k.fullAt) >= MaxAge {
		return false, time.Time{}
	}
	return true, k.fullAt.Add(MaxAge)
}

// finish records a successful build at now, which read every repository when
// full, and forgets repositories no longer listed.
func (k *parts) finish(now time.Time, full bool, listed map[string]*part) {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if full && now.After(k.fullAt) {
		k.fullAt = now
	}
	for identity := range k.byRepo {
		if listed[identity] == nil {
			delete(k.byRepo, identity)
		}
	}
}

func (k *parts) get(identity string) *part {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.byRepo[identity]
}

// put keeps p, or forgets the repository's part when p is nil. A part read
// before the kept one, by a build that ran alongside, does not replace it.
func (k *parts) put(identity string, p *part) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if p == nil {
		delete(k.byRepo, identity)
		return
	}
	if old := k.byRepo[identity]; old != nil && old.read.After(p.read) {
		return
	}
	if k.byRepo == nil {
		k.byRepo = map[string]*part{}
	}
	k.byRepo[identity] = p
}

// repositoryPart is g's part: the kept one when reuse allows and its key
// still holds, otherwise read now and kept for later builds. changeAt is
// told when a kept part reaches MaxAge.
func (s *Service) repositoryPart(ctx context.Context, now time.Time, g *group, kept *parts, reuse bool, changeAt func(time.Time)) (*part, error) {
	if kept == nil {
		p, _, err := s.readPart(ctx, now, g)
		return p, err
	}
	key, trusted, err := s.partKey(ctx, g)
	if err != nil {
		return nil, err
	}
	if reuse && trusted {
		if p := kept.get(g.identity); p != nil && p.key.same(key) && now.Sub(p.read) < MaxAge && (p.next.IsZero() || now.Before(p.next)) {
			changeAt(p.read.Add(MaxAge))
			return p, nil
		}
	}
	p, cacheable, err := s.readPart(ctx, now, g)
	if err != nil {
		return nil, err
	}
	if trusted && cacheable {
		p.key = key
		kept.put(g.identity, p)
	} else {
		kept.put(g.identity, nil)
	}
	return p, nil
}

// partKey stamps what readPart reads of g, without reading it. trusted is
// false when the stamp cannot vouch for it.
func (s *Service) partKey(ctx context.Context, g *group) (partKey, bool, error) {
	var b strings.Builder
	var files []os.FileInfo
	trusted := true
	_, _ = fmt.Fprintf(&b, "picked=%t bare=%d clone=%t\n", g.picked, g.bare, g.hasClone)
	for _, c := range g.clones {
		_, _ = fmt.Fprintf(&b, "%q %q %t %t", c.clone.ID, c.clone.CommonDir, c.clone.Available, c.owned)
		if c.owned {
			stamp, stamped, ok := s.storeStamp(c.clone.CommonDir)
			trusted = trusted && ok
			files = append(files, stamped...)
			_, _ = fmt.Fprintf(&b, " %q", stamp)
		}
		b.WriteByte('\n')
	}
	key := partKey{inputs: b.String(), files: files}
	if !g.picked || s.Discovery == nil {
		return key, trusted, nil
	}
	parsed, err := github.ParseRepository(g.identity)
	if err != nil {
		return partKey{}, false, err
	}
	revisions, ok := s.Discovery.(Revisions)
	if !ok {
		return key, false, nil
	}
	revision, err := revisions.Revision(ctx, parsed)
	if err != nil {
		return key, false, nil
	}
	key.revision = revision
	return key, trusted, nil
}

func (s *Service) storeStamp(commonDir string) (string, []os.FileInfo, bool) {
	switch {
	case s.StoreStamp != nil:
		stamp, ok := s.StoreStamp(commonDir)
		return stamp, nil, ok
	case s.OpenStore != nil:
		return "", nil, false
	}
	return fileStoreStamp(commonDir, time.Now())
}

// fileStoreStamp stamps the store store.Open reads for commonDir: the size,
// modification time and mode of versions.sqlite and its write-ahead log
// under both roots it chooses between, a missing file included, and the
// files themselves for their identity. It only stats them: closing a
// descriptor to a SQLite file drops this process's locks on it.
//
// Unlike the watcher's fingerprint, it does not look through lease renewals
// (store.SeenThroughHeartbeats): a renewal of a lease that had already
// lapsed makes an interrupted run read as generating again, so a renewal
// reads the store again, as any write does.
func fileStoreStamp(commonDir string, now time.Time) (string, []os.FileInfo, bool) {
	roots, err := store.Roots(commonDir)
	if err != nil {
		return "", nil, false
	}
	var b strings.Builder
	var files []os.FileInfo
	for _, root := range roots {
		for _, name := range []string{"versions.sqlite", "versions.sqlite-wal"} {
			path := filepath.Join(root, name)
			info, err := os.Stat(path)
			switch {
			case errors.Is(err, fs.ErrNotExist):
				_, _ = fmt.Fprintf(&b, "%q -\n", path)
				continue
			case err != nil:
				return "", nil, false
			}
			if now.Sub(info.ModTime()) < racyWindow {
				return "", nil, false
			}
			_, _ = fmt.Fprintf(&b, "%q %d %d %v\n", path, info.Size(), info.ModTime().UnixNano(), info.Mode())
			files = append(files, info)
		}
	}
	return b.String(), files, true
}
