package locallink

import (
	"bytes"
	"container/list"
	"context"
	"io"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/workstats"
	"github.com/baum-ai/baum/linkage/sessioncorrelate"
	"github.com/baum-ai/baum/sessions"
	"github.com/baum-ai/baum/sessions/codextranscript"
	"github.com/baum-ai/baum/sessions/session"
)

// DefaultDigestBytes bounds the memory the session digests take, roughly
// estimated. A digest is a few kilobytes for most sessions, against the
// megabytes of the transcript it was read from.
const DefaultDigestBytes = 64 << 20

// digestOverhead is what a digest costs beyond its facts and projections:
// the entry, its key and its list element.
const digestOverhead = 256

// maxProjections bounds the evidence kept per session: one for each pull
// request it is linked to whose evidence of it differs (a path one names
// that another does not).
const maxProjections = 4

// sessionDigests keeps, for every session the process mapped to build
// evidence, what building evidence reads from its graph, by the session's
// Key: its facts, and its evidence as each pull request it was linked to saw
// it. A session whose files did not change is then not mapped again.
var sessionDigests = newDigestCache(DefaultDigestBytes)

func init() {
	workstats.Gauge("locallink.session_digests.bytes", func() int64 {
		c := sessionDigests
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.held
	})
	workstats.Gauge("locallink.evidence_basis.per_session_limit", func() int64 { return maxEvidenceBasisBytes })
	workstats.Gauge("locallink.evidence_basis.bytes", func() int64 {
		c := sessionDigests
		c.mu.Lock()
		defer c.mu.Unlock()
		var bytes int64
		for _, e := range c.entries {
			bytes += e.Value.(*sessionDigest).basisCost
		}
		return bytes
	})
	workstats.Gauge("locallink.session_digests.entries", func() int64 {
		c := sessionDigests
		c.mu.Lock()
		defer c.mu.Unlock()
		return int64(c.order.Len())
	})
}

// sessionFacts is what linking reads from a session's graph.
type sessionFacts struct {
	generationRun bool
	// transcript is sessions.HasTranscript of the graph.
	transcript bool
	// root says the graph has a top-level session, whose start, end and
	// branch follow.
	root               bool
	startedAt, endedAt *time.Time
	gitBranch          *string
	title              *string
	outcome            session.Outcome
	correlated         sessioncorrelate.Result
	// writes are the session's Git-writing shell calls (gitWrites), which
	// the reflog matcher places in the clone's working trees.
	writes []gitWrite
}

// graphFacts reads the facts of s from its graph.
func graphFacts(s sessions.Session) sessionFacts {
	facts := sessionFacts{
		generationRun: sessions.IsGenerationRun(s.Graph),
		transcript:    sessions.HasTranscript(s.Graph),
		correlated:    sessioncorrelate.Correlate(s.Graph, s.Vendor),
		writes:        gitWrites(context.Background(), s),
	}
	if root := s.Root(); root != nil {
		facts.root = true
		facts.startedAt, facts.endedAt, facts.gitBranch = root.StartedAt, root.EndedAt, root.GitBranch
		facts.title, facts.outcome = root.Title, root.Outcome
	}
	return facts
}

// detached is a copy of f sharing no memory with the graph it was read
// from, so keeping it does not keep the graph.
func (f sessionFacts) detached() sessionFacts {
	copyTime := func(t *time.Time) *time.Time {
		if t == nil {
			return nil
		}
		at := *t
		return &at
	}
	out := f
	out.startedAt, out.endedAt = copyTime(f.startedAt), copyTime(f.endedAt)
	if f.gitBranch != nil {
		branch := strings.Clone(*f.gitBranch)
		out.gitBranch = &branch
	}
	if f.title != nil {
		title := strings.Clone(*f.title)
		out.title = &title
	}
	out.correlated = detach(f.correlated)
	out.writes = make([]gitWrite, len(f.writes))
	for i, w := range f.writes {
		out.writes[i] = gitWrite{start: w.start, end: w.end, branch: strings.Clone(w.branch), dirs: make([]string, len(w.dirs))}
		for j, dir := range w.dirs {
			out.writes[i].dirs[j] = strings.Clone(dir)
		}
	}
	return out
}

// sessionDigest is one session's entry in sessionDigests. Its facts never
// change; its projections are guarded by mu.
type sessionDigest struct {
	key   string
	facts sessionFacts
	cost  int64

	mu             sync.Mutex
	projections    []*projection
	basis          *session.Graph
	basisCost      int64
	basisOversized bool
}

// projection is the evidence sessionEvidence gave a session, with
// everything beside the graph it depended on. Confidence is not part of it:
// the build sets it.
type projection struct {
	id, vendor, path string
	maxPrompts       int
	maxCommandEvents int
	// names are the Codex thread names looked up for the title, by index
	// and thread id.
	names map[[2]string]threadName
	// noRoots says the clone roots were all unusable, which the path
	// resolver treats apart; placed are the absolute paths it placed
	// against them and where (sessioncorrelate.WithCloneLookups).
	noRoots bool
	placed  map[string]placement
	// known are the answers the knownPath predicate gave.
	known map[string]bool
	item  bundle.Session
	ok    bool
}

type placement struct {
	relative string
	ok       bool
}

type threadName struct {
	name   string
	failed bool
}

func lookUpThreadName(index, id string) threadName {
	name, err := codextranscript.ThreadName(index, id)
	return threadName{name: name, failed: err != nil}
}

// fits reports whether p is what sessionEvidence would give s now.
func (p *projection) fits(s sessions.Session, roots sessioncorrelate.CloneRoots, knownPath func(string) bool, maxPrompts, maxCommandEvents int) bool {
	if p.id != s.ID || p.vendor != s.Vendor || p.path != s.Path || p.maxPrompts != maxPrompts ||
		p.maxCommandEvents != maxCommandEvents || p.noRoots != (len(roots) == 0) {
		return false
	}
	for lookup, want := range p.names {
		if lookUpThreadName(lookup[0], lookup[1]) != want {
			return false
		}
	}
	for path, want := range p.placed {
		if relative, ok := roots.Place(path); relative != want.relative || ok != want.ok {
			return false
		}
	}
	for path, want := range p.known {
		if knownPath(path) != want {
			return false
		}
	}
	return true
}

// digestBook is one evidence build's view of sessionDigests: the digests it
// relies on are held here from the moment LoadReusing is told it may skip
// their sessions, so an eviction meanwhile cannot take them away. A nil book
// reads everything from the graphs.
type digestBook struct {
	cache          *digestCache
	held           map[string]*sessionDigest
	basisAttempted map[string]bool
	// refused are keys whose sessions must be mapped; refusingAll maps all.
	refused     map[string]bool
	refusingAll bool
	// missing is set when an unmapped session's digest could not be found,
	// which leaves its facts unknown: the build maps everything instead.
	missing bool
	// afterReuse, when set, runs after reuse holds a digest; tests use it.
	afterReuse func(key string)
}

// newBuildBook makes the book BuildSessionEvidence uses; tests replace it.
var newBuildBook = newDigestBook

func newDigestBook(cache *digestCache) *digestBook {
	return &digestBook{cache: cache, held: map[string]*sessionDigest{}, basisAttempted: map[string]bool{}, refused: map[string]bool{}}
}

// reuse is the book's sessions.Reuse.
func (b *digestBook) reuse(key string) (transcript, ok bool) {
	if b.refusingAll || b.refused[key] {
		return false, false
	}
	d, ok := b.held[key]
	if !ok {
		if d, ok = b.cache.get(key); !ok {
			return false, false
		}
		b.held[key] = d
	}
	if b.afterReuse != nil {
		b.afterReuse(key)
	}
	workstats.Inc("locallink.session_digests.reused")
	return d.facts.transcript, true
}

// refuse has the next load map the sessions with keys.
func (b *digestBook) refuse(keys ...string) {
	for _, key := range keys {
		b.refused[key] = true
	}
}

// refuseAll has the next load map every session.
func (b *digestBook) refuseAll() { b.refusingAll = true }

// digest is the digest of s: the one the book holds or the cache keeps, else
// one made from its graph and kept. It is nil for a session without a Key,
// with a nil book, and for an unmapped session whose digest is gone, which
// sets missing.
func (b *digestBook) digest(s sessions.Session) *sessionDigest {
	if b == nil || s.Key == "" {
		return nil
	}
	if d, ok := b.held[s.Key]; ok {
		b.keepBasis(d, s)
		return d
	}
	// Another build may have made it meanwhile: its projections are kept.
	if d, ok := b.cache.get(s.Key); ok {
		b.held[s.Key] = d
		b.keepBasis(d, s)
		return d
	}
	if s.Unmapped {
		// LoadReusing returns a session unmapped only when reuse held its
		// digest, so this is not expected; the build recovers by mapping.
		b.missing = true
		return nil
	}
	d := &sessionDigest{key: s.Key, facts: graphFacts(s).detached()}
	d.cost = digestOverhead + estimateCost(reflect.ValueOf(d.facts))
	b.cache.put(d)
	b.keepBasis(d, s)
	b.held[s.Key] = d
	workstats.Inc("locallink.session_digests.built")
	return d
}

// keepBasis seeds optional reusable evidence inputs only from a real mapped
// graph. A later full-read fallback may refill a basis shed under pressure.
func (b *digestBook) keepBasis(d *sessionDigest, s sessions.Session) {
	if s.Unmapped || s.Graph == nil || !d.facts.root || d.facts.generationRun || b.basisAttempted[s.Key] {
		return
	}
	b.basisAttempted[s.Key] = true
	d.mu.Lock()
	present := d.basis != nil || d.basisOversized
	d.mu.Unlock()
	if present {
		return
	}
	limit := b.cache.basisAllowance(d)
	if limit <= 0 {
		workstats.Inc("locallink.evidence_basis.no_space")
		return
	}
	workstats.Add("locallink.evidence_basis.source_graph_bytes", estimateCost(reflect.ValueOf(s.Graph)))
	basis, cost := newEvidenceBasis(s.Graph, limit)
	if basis == nil {
		if limit == maxEvidenceBasisBytes {
			d.mu.Lock()
			d.basisOversized = true
			d.mu.Unlock()
			workstats.Inc("locallink.evidence_basis.oversized")
		} else {
			workstats.Inc("locallink.evidence_basis.no_space")
		}
		return
	}
	b.cache.addBasis(d, basis, cost)
}

// facts are what linking reads from s.
func (b *digestBook) facts(s sessions.Session) sessionFacts {
	if d := b.digest(s); d != nil {
		return d.facts
	}
	return graphFacts(s)
}

// gitWrites are the Git-writing shell calls of s, which the reflog matcher
// places: from its digest, so an unmapped session still has them.
func (b *digestBook) gitWrites(ctx context.Context, s sessions.Session) []gitWrite {
	if d := b.digest(s); d != nil {
		return d.facts.writes
	}
	return gitWrites(ctx, s)
}

// generationRun reports whether s is one of baum's own generation runs.
func (b *digestBook) generationRun(s sessions.Session) bool {
	if d := b.digest(s); d != nil {
		return d.facts.generationRun
	}
	return sessions.IsGenerationRun(s.Graph)
}

// evidence is sessionEvidence for s, reused from its digest when a kept
// projection fits what it would read now, and kept for next time otherwise.
// needsGraph says s is unmapped and nothing kept fits: it must be mapped.
func (b *digestBook) evidence(s sessions.Session, confidence float64, roots []string, knownPath func(string) bool, maxPrompts, maxCommandEvents int) (item bundle.Session, ok, needsGraph bool) {
	d := b.digest(s)
	if d == nil {
		if s.Unmapped {
			return bundle.Session{}, false, false
		}
		item, ok = sessionEvidence(s, confidence, roots, knownPath, maxPrompts, maxCommandEvents)
		return item, ok, false
	}
	if !d.facts.root {
		// Nothing but the session itself is read.
		if len(s.CreatedCommits) > 0 {
			return bundle.Session{RootSessionID: s.ID, Vendor: s.Vendor, Confidence: confidence}, true, false
		}
		return bundle.Session{}, false, false
	}
	parsed := sessioncorrelate.ParseCloneRoots(roots...)
	if p := d.find(s, parsed, knownPath, maxPrompts, maxCommandEvents); p != nil {
		workstats.Inc("locallink.session_projections.reused")
		item = deepCopy(p.item)
		item.Confidence = confidence
		return item, p.ok, false
	}
	if s.Unmapped {
		d.mu.Lock()
		basis := d.basis
		d.mu.Unlock()
		if basis == nil {
			workstats.Inc("locallink.evidence_basis.missing")
			return bundle.Session{}, false, true
		}
		s.Graph, s.Unmapped = basis, false
		workstats.Inc("locallink.evidence_basis.reused")
	}
	p := &projection{
		id: s.ID, vendor: s.Vendor, path: s.Path, maxPrompts: maxPrompts, maxCommandEvents: maxCommandEvents,
		noRoots: len(parsed) == 0, placed: map[string]placement{}, known: map[string]bool{}, names: map[[2]string]threadName{},
	}
	recordKnown := func(path string) bool {
		answer := knownPath(path)
		p.known[strings.Clone(path)] = answer
		return answer
	}
	hooks := evidenceHooks{
		resolver: []sessioncorrelate.Option{sessioncorrelate.WithCloneLookups(func(path, relative string, ok bool) {
			p.placed[strings.Clone(path)] = placement{relative: strings.Clone(relative), ok: ok}
		})},
		threadName: func(index, id string) (string, error) {
			name, err := codextranscript.ThreadName(index, id)
			p.names[[2]string{index, id}] = threadName{name: name, failed: err != nil}
			return name, err
		},
	}
	item, ok = sessionEvidenceWith(s, confidence, roots, recordKnown, maxPrompts, maxCommandEvents, hooks)
	p.item, p.ok = detach(item), ok
	p.item.Confidence = 0
	b.cache.addProjection(d, p)
	workstats.Inc("locallink.session_projections.built")
	return item, ok, false
}

// find returns the kept projection that fits s now, nil when none does.
func (d *sessionDigest) find(s sessions.Session, roots sessioncorrelate.CloneRoots, knownPath func(string) bool, maxPrompts, maxCommandEvents int) *projection {
	d.mu.Lock()
	kept := append([]*projection(nil), d.projections...)
	d.mu.Unlock()
	for _, p := range kept {
		if p.fits(s, roots, knownPath, maxPrompts, maxCommandEvents) {
			return p
		}
	}
	return nil
}

// digestCache is sessionDigests: least recently used digests go first once
// their estimated size passes the limit.
type digestCache struct {
	mu      sync.Mutex
	limit   int64
	held    int64
	order   *list.List
	entries map[string]*list.Element
}

func newDigestCache(limit int64) *digestCache {
	return &digestCache{limit: limit, order: list.New(), entries: map[string]*list.Element{}}
}

func (c *digestCache) get(key string) (*sessionDigest, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(element)
	return element.Value.(*sessionDigest), true
}

// put keeps d, in place of a digest with the same key, unless it alone
// passes the limit.
func (c *digestCache) put(d *sessionDigest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d.cost > c.limit {
		return
	}
	if old, ok := c.entries[d.key]; ok {
		c.held -= old.Value.(*sessionDigest).cost
		c.order.Remove(old)
	}
	c.entries[d.key] = c.order.PushFront(d)
	c.held += d.cost
	c.evict()
}

// addProjection keeps p first among d's projections, dropping the oldest
// past maxProjections, and accounts for the change.
func (c *digestCache) addProjection(d *sessionDigest, p *projection) {
	cost := estimateCost(reflect.ValueOf(*p))
	c.mu.Lock()
	defer c.mu.Unlock()
	d.mu.Lock()
	d.projections = append([]*projection{p}, d.projections...)
	delta := cost
	for len(d.projections) > maxProjections {
		last := d.projections[len(d.projections)-1]
		d.projections = d.projections[:len(d.projections)-1]
		delta -= estimateCost(reflect.ValueOf(*last))
	}
	d.cost += delta
	d.mu.Unlock()
	// Only d itself counts toward held: another digest under its key (one a
	// concurrent build put) is not d.
	if element, kept := c.entries[d.key]; kept && element.Value.(*sessionDigest) == d {
		c.held += delta
		c.evict()
	}
}

// Bases are optional: do not evict compact facts to admit one, and discard
// bases before facts when later essential data needs their space.
func (c *digestCache) basisAllowance(d *sessionDigest) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[d.key]; e == nil || e.Value.(*sessionDigest) != d {
		return 0
	}
	capacity := c.limit - c.held
	for _, e := range c.entries {
		capacity += e.Value.(*sessionDigest).basisCost
	}
	return min(maxEvidenceBasisBytes, max(int64(0), capacity))
}

func (c *digestCache) addBasis(d *sessionDigest, basis *session.Graph, cost int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[d.key]
	if e == nil || e.Value.(*sessionDigest) != d {
		workstats.Inc("locallink.evidence_basis.no_space")
		return
	}
	d.mu.Lock()
	present := d.basis != nil
	d.mu.Unlock()
	if present {
		return
	}
	for e := c.order.Back(); e != nil && c.held+cost > c.limit; e = e.Prev() {
		c.dropBasis(e.Value.(*sessionDigest))
	}
	if c.held+cost > c.limit {
		workstats.Inc("locallink.evidence_basis.no_space")
		return
	}
	d.mu.Lock()
	d.basis, d.basisCost = basis, cost
	d.cost += cost
	d.mu.Unlock()
	c.held += cost
	workstats.Inc("locallink.evidence_basis.built")
	workstats.Add("locallink.evidence_basis.retained_bytes", cost)
}

// dropBasis sheds optional data only; the compact facts stay reusable.
// c.mu is held throughout every accounting change.
func (c *digestCache) dropBasis(d *sessionDigest) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.basisCost == 0 {
		return
	}
	c.held -= d.basisCost
	d.cost -= d.basisCost
	d.basisCost = 0
	d.basis = nil
	workstats.Inc("locallink.evidence_basis.evicted")
}

func (c *digestCache) evict() {
	for c.held > c.limit {
		for e := c.order.Back(); e != nil && c.held > c.limit; e = e.Prev() {
			c.dropBasis(e.Value.(*sessionDigest))
		}
		if c.held <= c.limit {
			return
		}
		last := c.order.Back()
		if last == nil {
			return
		}
		d := last.Value.(*sessionDigest)
		c.order.Remove(last)
		delete(c.entries, d.key)
		c.held -= d.cost
	}
}

// estimateCost is a rough count of the bytes v holds: its strings, slices
// and maps, and a word for everything else.
func estimateCost(v reflect.Value) int64 {
	switch v.Kind() {
	case reflect.String:
		return 16 + int64(v.Len())
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return 8
		}
		return 8 + estimateCost(v.Elem())
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return 24 + int64(v.Len())
		}
		cost := int64(24)
		for i := range v.Len() {
			cost += estimateCost(v.Index(i))
		}
		return cost
	case reflect.Array:
		var cost int64
		for i := range v.Len() {
			cost += estimateCost(v.Index(i))
		}
		return cost
	case reflect.Map:
		cost := int64(48)
		iter := v.MapRange()
		for iter.Next() {
			cost += estimateCost(iter.Key()) + estimateCost(iter.Value())
		}
		return cost
	case reflect.Struct:
		if v.Type() == reflect.TypeFor[time.Time]() {
			return 24
		}
		var cost int64
		for i := range v.NumField() {
			cost += estimateCost(v.Field(i))
		}
		return cost
	default:
		return 8
	}
}

// heldWarning holds warnings for w until flush; with no w it holds none.
type heldWarning struct {
	w   io.Writer
	buf bytes.Buffer
}

func heldWarnings(w io.Writer) *heldWarning { return &heldWarning{w: w} }

// writer is where the warnings go meanwhile, nil when they are discarded.
func (h *heldWarning) writer() io.Writer {
	if h.w == nil {
		return nil
	}
	return &h.buf
}

func (h *heldWarning) reset() { h.buf.Reset() }

func (h *heldWarning) flush() {
	if h.w != nil && h.buf.Len() > 0 {
		_, _ = h.w.Write(h.buf.Bytes())
	}
	h.buf.Reset()
}
