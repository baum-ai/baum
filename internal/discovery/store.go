package discovery

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/appdb"
)

// A service decodes each cache document once per change rather than once per
// read. It keeps the document as it is on disk, with the fields readers use
// (view) decoded on first use and shared by every reader until it changes.
//
// The service trusts what it kept while app.sqlite, and the write-ahead log
// beside it, keep the identity, size and modification time they had when it
// was read. A commit appends to the log; app.sqlite itself moves only when a
// checkpoint copies the log into it. When
// they changed, or were modified within racyWindow of being looked at (a
// later write could keep the same time on a coarse clock), it reads the
// document again and decodes it only if its bytes differ. So a write by
// another process or another service is seen at the next read, and the
// daemon's own writes to other parts of app.sqlite cost a read, not a decode.
const racyWindow = 3 * time.Second

// unsavedLimit bounds how long a poll that changed nothing but its times
// leaves the disk copy behind (store): at most this much older than the
// cache this service reads.
const unsavedLimit = time.Hour

// maxDocument bounds a cache document.
const maxDocument = 32 << 20

var (
	errDocumentTooLarge = errors.New("discovery cache too large")
	errInvalidDocument  = errors.New("invalid discovery cache")
)

// view is what readers take from a cache document. Decoding it skips the rows
// and conditional responses. Field names match diskCache's. A view is shared:
// nothing may change it, or what its slices, maps and pointers point at.
type view struct {
	Snapshot    Snapshot
	GraphQLRate RateLimit
	Interval    time.Duration
	Fingerprint fingerprint
	Swept       time.Time      `json:",omitzero"`
	Teams       teamMembership `json:",omitzero"`
}

// times are the fields a poll changes even when GitHub's answer did not: when
// it fetched, when the next poll is due, the budget it saw, the backoff and
// when a sweep last finished. Losing them at a restart only brings the next
// poll or sweep forward.
type times struct {
	FetchedAt, NextPoll, Swept time.Time
	Rate                       RateLimit
	Interval                   time.Duration
}

func (c *diskCache) times() times {
	return times{FetchedAt: c.Snapshot.FetchedAt, NextPoll: c.Snapshot.NextPoll, Swept: c.Swept, Rate: c.Snapshot.Rate, Interval: c.Interval}
}

func (c *diskCache) setTimes(t times) {
	c.Snapshot.FetchedAt, c.Snapshot.NextPoll, c.Swept, c.Snapshot.Rate, c.Interval = t.FetchedAt, t.NextPoll, t.Swept, t.Rate, t.Interval
}

func (v *view) times() times {
	return times{FetchedAt: v.Snapshot.FetchedAt, NextPoll: v.Snapshot.NextPoll, Swept: v.Swept, Rate: v.Snapshot.Rate, Interval: v.Interval}
}

func (v *view) setTimes(t times) {
	v.Snapshot.FetchedAt, v.Snapshot.NextPoll, v.Swept, v.Snapshot.Rate, v.Interval = t.FetchedAt, t.NextPoll, t.Swept, t.Rate, t.Interval
}

// document is one cache document as this service last saw it. It is never
// changed once stored; a newer one replaces it.
type document struct {
	// raw is the document on disk.
	raw []byte
	// stamp is app.sqlite's when raw was read; not ok after this service
	// wrote raw, so the next read checks it.
	stamp stamp
	// saved are raw's times when known without decoding it.
	saved *times
	// unsaved are times newer than raw's that a poll did not write; nil when
	// raw is current.
	unsaved *times
	decoded *decodedView
}

type decodedView struct {
	once sync.Once
	view *view
	err  error
}

func newDocument(raw []byte, st stamp, saved, unsaved *times) *document {
	return &document{raw: raw, stamp: st, saved: saved, unsaved: unsaved, decoded: &decodedView{}}
}

// view decodes the document once; every caller shares the result.
func (d *document) view() (*view, error) {
	d.decoded.once.Do(func() {
		var v view
		if json.Unmarshal(d.raw, &v) != nil {
			d.decoded.err = errInvalidDocument
			return
		}
		if d.unsaved != nil {
			v.setTimes(*d.unsaved)
		}
		d.decoded.view = &v
	})
	return d.decoded.view, d.decoded.err
}

// mutable decodes a copy of the whole document that the caller owns.
func (d *document) mutable() (diskCache, error) {
	var cache diskCache
	if json.Unmarshal(d.raw, &cache) != nil {
		return diskCache{}, errInvalidDocument
	}
	if d.unsaved != nil {
		cache.setTimes(*d.unsaved)
	}
	return cache, nil
}

// savedTimes are the times on disk.
func (d *document) savedTimes() (times, error) {
	if d.saved != nil {
		return *d.saved, nil
	}
	// Without saved there is nothing unsaved either: the view is raw's.
	v, err := d.view()
	if err != nil {
		return times{}, err
	}
	return v.times(), nil
}

// stamp identifies app.sqlite's state; ok is false when it cannot vouch for
// anything.
type stamp struct {
	main, wal os.FileInfo
	ok        bool
}

// stampOf looks at the database and its WAL without opening either: closing
// a descriptor to a SQLite file would drop this process's locks on it.
func stampOf(path string) stamp {
	main, err := os.Stat(path)
	if err != nil {
		return stamp{}
	}
	wal, err := os.Stat(path + "-wal")
	switch {
	case errors.Is(err, fs.ErrNotExist):
		wal = nil
	case err != nil:
		return stamp{}
	}
	now := time.Now()
	for _, info := range []os.FileInfo{main, wal} {
		if info != nil && now.Sub(info.ModTime()) < racyWindow {
			return stamp{}
		}
	}
	return stamp{main: main, wal: wal, ok: true}
}

func (a stamp) same(b stamp) bool {
	return a.ok && b.ok && sameFile(a.main, b.main) && sameFile(a.wal, b.wal)
}

func sameFile(a, b os.FileInfo) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && os.SameFile(a, b)
}

// document returns the cache document under key, read again only when
// app.sqlite may have changed since. fs.ErrNotExist when there is none.
func (s *Service) document(key string) (*document, error) {
	path, err := s.databasePath()
	if err != nil {
		return nil, err
	}
	st := stampOf(path)
	s.docsMu.Lock()
	old := s.docs[key]
	s.docsMu.Unlock()
	if old != nil && old.stamp.same(st) {
		return old, nil
	}
	raw, err := appdb.Read(path, "cache", "discovery/"+key)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.replace(key, old, nil)
		}
		return nil, err
	}
	if len(raw) > maxDocument {
		return nil, errDocumentTooLarge
	}
	var doc *document
	if old != nil && bytes.Equal(raw, old.raw) {
		// Unchanged: keep what was decoded, and what a poll left unsaved.
		kept := *old
		kept.stamp = st
		doc = &kept
	} else {
		doc = newDocument(raw, st, nil, nil)
	}
	s.replace(key, old, doc)
	return doc, nil
}

// replace stores doc in place of old, unless a save replaced old meanwhile:
// what it stored is newer than what this read saw.
func (s *Service) replace(key string, old, doc *document) {
	s.docsMu.Lock()
	defer s.docsMu.Unlock()
	if s.docs[key] != old {
		return
	}
	s.set(key, doc)
}

// set stores doc; s.docsMu must be held.
func (s *Service) set(key string, doc *document) {
	if doc == nil {
		delete(s.docs, key)
		return
	}
	if s.docs == nil {
		s.docs = map[string]*document{}
	}
	s.docs[key] = doc
}

// view is the shared, decoded document under key.
func (s *Service) view(key string) (*view, error) {
	doc, err := s.document(key)
	if err != nil {
		return nil, err
	}
	return doc.view()
}

// load decodes a copy of the document under key that the caller may change
// and save.
func (s *Service) load(key string) (diskCache, error) {
	doc, err := s.document(key)
	if err != nil {
		return diskCache{}, err
	}
	return doc.mutable()
}

// save writes cache under key, unless the document on disk already holds it.
func (s *Service) save(key string, cache diskCache) error {
	return s.store(key, cache, false)
}

// store writes cache under key. A poll passes deferTimes: when its cache
// differs from the disk copy only in its times, and the disk copy is less
// than unsavedLimit old, the times are kept in memory and nothing is written.
// Cached, Revision and the next poll see them all the same; a restart, or
// another process, reads the disk copy, so at worst polls or sweeps sooner.
// Any other change writes the whole cache, those times included, in one
// transaction as before.
func (s *Service) store(key string, cache diskCache, deferTimes bool) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	path, err := s.databasePath()
	if err != nil {
		return err
	}
	old, err := s.document(key)
	if err != nil {
		// Missing or unreadable: written over, as before.
		old = nil
	}
	if old != nil && deferTimes {
		saved, err := old.savedTimes()
		if err == nil && s.now().Sub(saved.FetchedAt) < unsavedLimit {
			same := cache
			same.setTimes(saved)
			raw, err := json.Marshal(same)
			if err != nil {
				return err
			}
			if bytes.Equal(raw, old.raw) {
				unsaved := cache.times()
				s.docsMu.Lock()
				s.set(key, newDocument(old.raw, old.stamp, &saved, &unsaved))
				s.docsMu.Unlock()
				s.wrote(key)
				s.updatedBy(key)
				return nil
			}
		}
	}
	raw, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	if len(raw) > maxDocument {
		return errDocumentTooLarge
	}
	if old != nil && old.unsaved == nil && bytes.Equal(raw, old.raw) {
		// Nothing Cached reads changes.
		return nil
	}
	err = appdb.Write(path, "cache", "discovery/"+key, raw)
	s.docsMu.Lock()
	if err == nil {
		written := cache.times()
		s.set(key, newDocument(raw, stamp{}, &written, nil))
	} else {
		// What the disk holds now is unknown: read it again.
		s.set(key, nil)
	}
	s.docsMu.Unlock()
	// Counted after the write, failed or not, so a Revision read before it
	// never vouches for what it wrote.
	s.wrote(key)
	if err != nil {
		return err
	}
	s.updatedBy(key)
	return nil
}

// updatedBy reports a write of the document under key, unless it is a
// credential's budget record, which Cached does not read: polls and probes
// save it on their own, and the review index must not rebuild for it.
func (s *Service) updatedBy(key string) {
	if !strings.HasSuffix(key, budgetSuffix) {
		s.updated()
	}
}

func (s *Service) wrote(key string) {
	s.writesMu.Lock()
	defer s.writesMu.Unlock()
	if s.writes == nil {
		s.writes = map[string]uint64{}
	}
	s.writes[key]++
}
