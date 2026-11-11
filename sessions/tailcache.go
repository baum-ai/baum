package sessions

import (
	"bytes"
	"container/list"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/workstats"
	"github.com/baum-ai/baum/sessions/claudetranscript"
	"github.com/baum-ai/baum/sessions/codextranscript"
	"github.com/baum-ai/baum/sessions/collector"
)

// DefaultTailCacheBytes bounds the estimated memory of the decoded lines Map
// keeps so it can read only what a transcript appended. Kept Claude Code lines
// take about a third of their transcript's size, so this holds the lines of a
// 150 MB transcript, the largest seen growing.
const DefaultTailCacheBytes = 64 << 20

// tailActiveFor is how recently a transcript must have changed for Map to
// keep its decoded lines. One unchanged for longer is unlikely to grow, and
// keeping its lines would only push out those of the transcripts that do.
const tailActiveFor = time.Hour

// tailIdleFor is how long a kept transcript must have gone unread before a
// newly read one may push it out. Within that time a newly read transcript is
// only kept if it fits: a session tree larger than the cache, read file by
// file on every refresh, would otherwise push out each file before it is read
// again, and none would ever be continued.
const tailIdleFor = 30 * time.Minute

// tailCheckBytes is how many bytes at the start of a transcript, and just
// before where the last read stopped, a continued read compares with what it
// saw then.
const tailCheckBytes = 4 << 10

// tailCache keeps, for each Claude Code transcript and Codex rollout Map read
// recently that was changing, its decoded lines and the byte offset they
// end at (a claudetranscript or codextranscript Reading). A transcript that
// grew is then decoded from that offset only, and the mapper runs again over
// all of its lines.
//
// A file is read from its start instead when it is not the file read last
// time (another inode: replaced), it is shorter than the offset (shrunk), or
// its first bytes or the bytes just before the offset differ from what they
// were (rewritten). A final line without a newline is never kept: a writer
// may still be writing it, so each read decodes it again.
//
// Only files changed within tailActiveFor are kept, within limit bytes of
// estimated line memory (Reading.Bytes); see account for what goes first.
// The graphs the mapping cache keeps do not depend on this: it sits below
// the mapping cache, which still keys a session on its files' sizes and
// modification times.
type tailCache struct {
	mu    sync.Mutex
	limit int64
	held  int64
	// covered is the transcript bytes the kept lines were decoded from.
	covered int64
	order   *list.List
	entries map[string]*list.Element
	// full, appended and lines count reads from a file's start, reads
	// continuing an earlier one, and lines decoded; tests read them.
	full, appended, lines int
}

// tailEntry is one file's Reading. mu serialises reads of the file; the
// cache's lock guards cost, used, kept and the entry's place in the cache.
type tailEntry struct {
	key     string
	path    string
	mu      sync.Mutex
	reading tailReading
	info    os.FileInfo
	offset  int64
	head    []byte
	before  []byte
	// cost and covered are the Reading's estimated memory and offset when
	// last accounted; used is when the file was last read; kept is false
	// until a read was kept.
	cost, covered int64
	used          time.Time
	kept          bool
}

// tailReading is a claudetranscript or codextranscript Reading.
type tailReading interface {
	Continue(io.Reader) (consumed int64, rest []byte, err error)
	Lines() int
	Bytes() int64
}

var tails = newTailCache(DefaultTailCacheBytes)

func init() {
	workstats.Gauge("sessions.tail_cache.bytes", func() int64 {
		c := tails
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.held
	})
	workstats.Gauge("sessions.tail_cache.transcript_bytes", func() int64 {
		c := tails
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.covered
	})
	workstats.Gauge("sessions.tail_cache.entries", func() int64 {
		c := tails
		c.mu.Lock()
		defer c.mu.Unlock()
		return int64(c.order.Len())
	})
}

func newTailCache(limit int64) *tailCache {
	return &tailCache{limit: limit, order: list.New(), entries: map[string]*list.Element{}}
}

// SetTailCacheLimit changes the estimated memory of the decoded lines Map
// keeps; zero or less turns that off and empties it, so every transcript is
// decoded whole each time it is mapped.
func SetTailCacheLimit(limit int64) {
	tails.mu.Lock()
	defer tails.mu.Unlock()
	tails.limit = limit
	tails.evict()
}

// claude reads one Claude Code transcript (claudetranscript.ReadTranscript).
func (c *tailCache) claude(path, name, agentID string) (*claudetranscript.File, error) {
	key := fmt.Sprintf("claude\x00%s\x00%s\x00%s", path, name, agentID)
	return tailRead(c, key, path, "transcript",
		func() tailReading { return claudetranscript.NewReading(name, path, agentID) },
		func(r tailReading, rest []byte) (*claudetranscript.File, error) {
			return r.(*claudetranscript.Reading).File(rest)
		})
}

// codex reads one Codex rollout (codextranscript.ReadRollout).
func (c *tailCache) codex(path string) (*codextranscript.File, error) {
	return tailRead(c, "codex\x00"+path, path, "rollout",
		func() tailReading { return codextranscript.NewReading(path) },
		func(r tailReading, rest []byte) (*codextranscript.File, error) {
			return r.(*codextranscript.Reading).File(rest)
		})
}

// tailRead reads the file at path with the Reading kept under key, or a new
// one from start, and returns what build makes of it and the final line it
// left unread. what names the file in an error opening it.
func tailRead[T any](c *tailCache, key, path, what string, start func() tailReading, build func(tailReading, []byte) (T, error)) (T, error) {
	var zero T
	f, err := os.Open(path)
	if err != nil {
		return zero, fmt.Errorf("opening %s %s: %w", what, path, err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return zero, fmt.Errorf("opening %s %s: %w", what, path, err)
	}

	e := c.entry(key, path, info)
	if e == nil {
		reading := start()
		_, rest, err := c.continueFrom(reading, f, 0)
		if err != nil {
			return zero, err
		}
		return build(reading, rest)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	var from int64
	if e.reading != nil {
		if reason := e.changed(f, info); reason != "" {
			workstats.Inc("sessions.tail.restarts." + reason)
			e.reading = nil
		} else {
			from = e.offset
		}
	}
	if e.reading == nil {
		e.reading, e.offset, e.head, e.before = start(), 0, nil, nil
	}
	consumed, rest, err := c.continueFrom(e.reading, f, from)
	if err != nil {
		e.reading = nil
		c.forget(e)
		return zero, err
	}
	e.info, e.offset = info, from+consumed
	e.head = readAt(f, 0, min(e.offset, tailCheckBytes))
	e.before = readAt(f, e.offset-min(e.offset, tailCheckBytes), min(e.offset, tailCheckBytes))
	c.account(e)
	return build(e.reading, rest)
}

// continueFrom continues reading at offset from and counts what it read.
func (c *tailCache) continueFrom(reading tailReading, f *os.File, from int64) (int64, []byte, error) {
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return 0, nil, err
	}
	before := reading.Lines()
	consumed, rest, err := reading.Continue(f)
	if err != nil {
		return 0, nil, err
	}
	lines := reading.Lines() - before
	if len(bytes.TrimSpace(rest)) > 0 {
		lines++
	}
	workstats.Add("sessions.map.decoded_bytes", consumed+int64(len(rest)))
	workstats.Add("sessions.map.decoded_lines", int64(lines))
	if from == 0 {
		workstats.Inc("sessions.tail.reads.full")
	} else {
		workstats.Inc("sessions.tail.reads.appended")
	}
	c.mu.Lock()
	if from == 0 {
		c.full++
	} else {
		c.appended++
	}
	c.lines += lines
	c.mu.Unlock()
	return consumed, rest, nil
}

// changed says why the file can no longer be read from the entry's offset,
// or "" when it can.
func (e *tailEntry) changed(f *os.File, info os.FileInfo) string {
	switch {
	case !os.SameFile(e.info, info):
		return "replaced"
	case info.Size() < e.offset:
		return "shrunk"
	case !bytes.Equal(readAt(f, 0, int64(len(e.head))), e.head),
		!bytes.Equal(readAt(f, e.offset-int64(len(e.before)), int64(len(e.before))), e.before):
		return "rewritten"
	}
	return ""
}

// readAt reads n bytes at off, or returns nil when it cannot read them all.
func readAt(f *os.File, off, n int64) []byte {
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, off); err != nil && !(err == io.EOF && n == 0) {
		return nil
	}
	return buf
}

// entry is the entry for key, created when the file is worth keeping lines
// for, or nil.
func (c *tailCache) entry(key, path string, info os.FileInfo) *tailEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[key]; ok {
		c.order.MoveToFront(element)
		e := element.Value.(*tailEntry)
		e.used = time.Now()
		return e
	}
	if c.limit <= 0 || time.Since(info.ModTime()) > tailActiveFor {
		return nil
	}
	e := &tailEntry{key: key, path: path, used: time.Now()}
	c.entries[key] = c.order.PushFront(e)
	return e
}

// pending is how much of the session's cost, the size of its files, is
// left to decode: the bytes its kept lines do not cover. It is what Map takes
// from the decode budget.
func (c *tailCache) pending(s collector.Session, cost int64) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) == 0 {
		return cost
	}
	paths := map[string]bool{}
	for _, f := range s.Files {
		if f.Data == nil {
			paths[f.Path] = true
		}
	}
	for _, element := range c.entries {
		if e := element.Value.(*tailEntry); paths[e.path] {
			cost -= e.covered
		}
	}
	return max(cost, 0)
}

// account records the Reading's size as the entry's cost, unless the entry
// was evicted meanwhile, and makes it fit: first by dropping entries unread
// for tailIdleFor, then, for a file read for the first time, by dropping it,
// and for one kept already, by dropping the least recently read.
func (c *tailCache) account(e *tailEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[e.key]
	if !ok || element.Value != e {
		return
	}
	c.held += e.reading.Bytes() - e.cost
	c.covered += e.offset - e.covered
	e.cost, e.covered = e.reading.Bytes(), e.offset
	idle := time.Now().Add(-tailIdleFor)
	for other := c.order.Back(); other != nil && c.held > c.limit; {
		previous := other.Prev()
		if o := other.Value.(*tailEntry); o != e && o.used.Before(idle) {
			c.remove(other)
		}
		other = previous
	}
	if c.held > c.limit && !e.kept {
		c.remove(element)
		return
	}
	e.kept = true
	c.evict()
}

// forget drops the entry, unless it was evicted meanwhile.
func (c *tailCache) forget(e *tailEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[e.key]; ok && element.Value == e {
		c.remove(element)
	}
}

func (c *tailCache) remove(element *list.Element) {
	e := element.Value.(*tailEntry)
	c.order.Remove(element)
	delete(c.entries, e.key)
	c.held -= e.cost
	c.covered -= e.covered
}

func (c *tailCache) evict() {
	for c.order.Len() > 0 && (c.limit <= 0 || c.held > c.limit) {
		c.remove(c.order.Back())
	}
}
