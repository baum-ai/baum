package sessions

import (
	"container/list"
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/workstats"
)

// snapshotPlan is what loadEntire found before reading any checkpoint whole:
// the checkpoints whose metadata could not be read (whole), each session's
// snapshots newest first (picks), and the checkpoints holding a newest
// snapshot or read whole (first).
type snapshotPlan struct {
	root   string
	opts   Options
	native map[string]bool
	// trees is each checkpoint's tree, by id, "" when it is not known.
	trees      map[string]string
	whole      []string
	first      []string
	sessionIDs []string
	picks      map[string][]pick
}

// snapshotFacts is what choosing a snapshot reads from one session a
// checkpoint records: everything but its graph.
type snapshotFacts struct {
	sessionID  string
	vendor     string
	createdAt  time.Time
	warnings   []string
	err        error
	graph      bool
	transcript bool
}

// checkpointFacts is one checkpoint as choosing reads it: the facts of each
// session it records, in the order it records them, or why it could not be
// read.
type checkpointFacts struct {
	results []snapshotFacts
	err     error
}

func factsOf(outcome readOutcome) checkpointFacts {
	facts := checkpointFacts{err: outcome.err}
	for _, result := range outcome.results {
		facts.results = append(facts.results, snapshotFacts{
			sessionID: result.SessionID, vendor: result.Vendor, createdAt: result.CreatedAt,
			warnings: slices.Clone(result.Warnings), err: result.Err,
			graph: result.Graph != nil, transcript: HasTranscript(result.Graph),
		})
	}
	return facts
}

// sameFacts reports whether two readings of a checkpoint choose alike.
func sameFacts(a, b checkpointFacts) bool {
	errText := func(err error) string {
		if err == nil {
			return ""
		}
		return "error: " + err.Error()
	}
	if errText(a.err) != errText(b.err) || len(a.results) != len(b.results) {
		return false
	}
	for i, x := range a.results {
		y := b.results[i]
		if x.sessionID != y.sessionID || x.vendor != y.vendor || !x.createdAt.Equal(y.createdAt) ||
			!slices.Equal(x.warnings, y.warnings) || errText(x.err) != errText(y.err) ||
			x.graph != y.graph || x.transcript != y.transcript {
			return false
		}
	}
	return true
}

// entireKey is the Key of the session sessionID read from the checkpoint
// whose tree is tree in the clone at root: a tree names its content, so the
// same tree always maps to the same graphs. The clone is part of it, as of
// the checkpoint cache's key, so one clone's reads never answer for
// another's: a partial clone missing the objects still reports them.
func entireKey(root, tree, sessionID string) string {
	if tree == "" {
		return ""
	}
	return "entire:" + checkpointKey(root, tree) + "\x00" + sessionID
}

// snapshotLoad is one loadEntire's reads: the checkpoints read whole in it,
// and the facts of every checkpoint it looked at, read or remembered.
type snapshotLoad struct {
	plan  snapshotPlan
	store *checkpointStore
	read  map[string]readOutcome
	facts map[string]checkpointFacts
}

// choice is the snapshot chosen for one session: the checkpoint and the
// session's place among its results.
type choice struct {
	checkpoint string
	index      int
	facts      snapshotFacts
}

// load chooses each session's snapshot and returns the chosen ones, as
// loadEntire describes. Choosing needs only each checkpoint's facts, which
// the process remembers by tree (snapshotFactsCache), so a checkpoint read
// before is read again only when a session chosen from it must be mapped:
// when reuse does not accept its Key. If such a read finds the checkpoint
// other than remembered (it can no longer be read), the choice is made again
// with what the read found, as a load without memory would make it.
func (p snapshotPlan) load(ctx context.Context, store *checkpointStore, reuse Reuse) ([]Session, []Failure, error) {
	l := &snapshotLoad{plan: p, store: store, read: map[string]readOutcome{}, facts: map[string]checkpointFacts{}}
	var unknown []string
	for _, id := range p.first {
		if cached, ok := snapshotFactsCache.get(p.root, p.trees[id]); ok {
			l.facts[id] = cached
			continue
		}
		unknown = append(unknown, id)
	}
	if err := l.readNow(ctx, unknown); err != nil {
		return nil, nil, err
	}
	for {
		chosen, failures, err := l.choose(ctx)
		if err != nil {
			return nil, failures, err
		}
		var missing []string
		for _, c := range chosen {
			if _, read := l.read[c.checkpoint]; read {
				continue
			}
			if key := entireKey(p.root, p.trees[c.checkpoint], c.facts.sessionID); reuse != nil && key != "" {
				if _, ok := reuse(key); ok {
					continue
				}
			}
			missing = append(missing, c.checkpoint)
		}
		slices.Sort(missing)
		missing = slices.Compact(missing)
		remembered := make(map[string]checkpointFacts, len(missing))
		for _, id := range missing {
			remembered[id] = l.facts[id]
		}
		if err := l.readNow(ctx, missing); err != nil {
			return nil, nil, err
		}
		changed := false
		for _, id := range missing {
			changed = changed || !sameFacts(remembered[id], l.facts[id])
		}
		if !changed {
			return l.sessions(chosen), failures, nil
		}
	}
}

// readNow reads ids whole, several at once, and remembers their facts.
func (l *snapshotLoad) readNow(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	outcomes, err := l.store.readAll(ctx, ids)
	if err != nil {
		return err
	}
	for id, outcome := range outcomes {
		l.remember(id, outcome)
	}
	return nil
}

func (l *snapshotLoad) remember(id string, outcome readOutcome) {
	l.read[id] = outcome
	l.facts[id] = factsOf(outcome)
	// A checkpoint that could not be read may read next time.
	if outcome.err == nil {
		snapshotFactsCache.put(l.plan.root, l.plan.trees[id], l.facts[id])
	}
}

// factsFor is the checkpoint's facts, read now when they are not known.
func (l *snapshotLoad) factsFor(ctx context.Context, id string) (checkpointFacts, error) {
	if facts, ok := l.facts[id]; ok {
		return facts, nil
	}
	if cached, ok := snapshotFactsCache.get(l.plan.root, l.plan.trees[id]); ok {
		l.facts[id] = cached
		return cached, nil
	}
	if err := ctx.Err(); err != nil {
		return checkpointFacts{}, err
	}
	l.remember(id, l.store.read(ctx, id))
	return l.facts[id], nil
}

// choose picks each session's snapshot: the newest with a transcript, else
// the newest mapped, from the checkpoints' facts.
func (l *snapshotLoad) choose(ctx context.Context) ([]choice, []Failure, error) {
	p := l.plan
	var failures []Failure
	reported := map[string]bool{}
	fail := func(path string, err error) {
		if !reported[path] {
			reported[path] = true
			failures = append(failures, Failure{Source: Entire, Path: path, Err: err})
		}
	}
	best := map[string]choice{}
	offer := func(checkpoint string, index int, facts snapshotFacts) {
		next := choice{checkpoint: checkpoint, index: index, facts: facts}
		if current, ok := best[facts.sessionID]; ok && !Fuller(next.compared(), current.compared()) {
			return
		}
		best[facts.sessionID] = next
	}
	for _, id := range p.whole {
		facts := l.facts[id]
		if facts.err != nil {
			fail(id, facts.err)
			continue
		}
		for i, result := range facts.results {
			switch {
			case result.err != nil:
				fail(id+"/"+result.sessionID, result.err)
			case !result.graph || p.native[result.sessionID]:
			case !p.opts.Since.IsZero() && result.createdAt.Before(p.opts.Since):
			default:
				offer(id, i, result)
			}
		}
	}
	// The newest snapshot with a transcript wins. One without (Entire wrote
	// only metadata and a prompt) is kept only while nothing older has one.
	for _, sessionID := range p.sessionIDs {
		for _, pk := range p.picks[sessionID] {
			facts, err := l.factsFor(ctx, pk.checkpoint)
			if err != nil {
				return nil, failures, err
			}
			if facts.err != nil {
				fail(pk.checkpoint, facts.err)
				continue
			}
			i := slices.IndexFunc(facts.results, func(r snapshotFacts) bool { return r.sessionID == sessionID })
			if i < 0 {
				continue
			}
			if result := facts.results[i]; result.err != nil {
				fail(pk.checkpoint+"/"+sessionID, result.err)
				continue
			} else if result.graph {
				offer(pk.checkpoint, i, result)
				if result.transcript {
					break
				}
			}
		}
	}
	chosen := make([]choice, 0, len(best))
	for _, c := range best {
		chosen = append(chosen, c)
	}
	slices.SortFunc(chosen, func(a, b choice) int { return strings.Compare(a.facts.sessionID, b.facts.sessionID) })
	return chosen, failures, nil
}

// compared is the choice as Fuller compares sessions.
func (c choice) compared() Session {
	return Session{ModifiedAt: c.facts.createdAt, Unmapped: true, Transcript: c.facts.transcript}
}

// sessions are the chosen snapshots, mapped when their checkpoint was read
// here and unmapped otherwise.
func (l *snapshotLoad) sessions(chosen []choice) []Session {
	out := make([]Session, 0, len(chosen))
	for _, c := range chosen {
		s := Session{
			Source: Entire, Vendor: c.facts.vendor, ID: c.facts.sessionID, Path: c.checkpoint,
			RepoRoot: l.plan.root, ModifiedAt: c.facts.createdAt, Notes: slices.Clone(c.facts.warnings),
			Key: entireKey(l.plan.root, l.plan.trees[c.checkpoint], c.facts.sessionID),
		}
		if outcome, read := l.read[c.checkpoint]; read {
			s.Graph = outcome.results[c.index].Graph
		} else {
			workstats.Inc("sessions.entire.unmapped")
			s.Unmapped, s.Transcript = true, c.facts.transcript
		}
		out = append(out, s)
	}
	return out
}

// maxSnapshotFacts bounds the checkpoints whose facts the process
// remembers. Each is the ids, dates and warnings of a few sessions.
const maxSnapshotFacts = 8192

// snapshotFactsCache remembers each checkpoint's facts by its clone and
// tree (checkpointKey), so choosing a snapshot reads a checkpoint whole only
// once per process.
var snapshotFactsCache = newFactsCache(maxSnapshotFacts)

type factsCache struct {
	mu      sync.Mutex
	limit   int
	order   *list.List
	entries map[string]*list.Element
}

type factsEntry struct {
	key   string
	facts checkpointFacts
}

func newFactsCache(limit int) *factsCache {
	return &factsCache{limit: limit, order: list.New(), entries: map[string]*list.Element{}}
}

// get returns the facts of the checkpoint whose tree is tree in the clone at
// root; an unknown tree has none.
func (c *factsCache) get(root, tree string) (checkpointFacts, bool) {
	if tree == "" {
		return checkpointFacts{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[checkpointKey(root, tree)]
	if !ok {
		return checkpointFacts{}, false
	}
	c.order.MoveToFront(element)
	workstats.Inc("sessions.entire.facts_reused")
	return element.Value.(*factsEntry).facts, true
}

func (c *factsCache) put(root, tree string, facts checkpointFacts) {
	if tree == "" {
		return
	}
	key := checkpointKey(root, tree)
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.entries[key]; ok {
		c.order.Remove(old)
	}
	c.entries[key] = c.order.PushFront(&factsEntry{key: key, facts: facts})
	for c.order.Len() > c.limit {
		last := c.order.Back()
		c.order.Remove(last)
		delete(c.entries, last.Value.(*factsEntry).key)
	}
}
