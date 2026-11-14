package tilechecks

import (
	"context"
	"slices"
	"time"

	"github.com/baum-ai/baum/internal/tilelibrary"
	"github.com/baum-ai/baum/tiles/library"
)

// The bounds of the results a Checker keeps for Result.
const (
	// keepResults is how many checks' results are kept, the newest.
	keepResults = 64
	// keepFor is how long a check's result is kept.
	keepFor = time.Hour
)

var _ tilelibrary.Checks = (*Checker)(nil)

// kept is a check's result as a proposal reads it, and when it ended.
type kept struct {
	at     time.Time
	result tilelibrary.CheckResult
}

// Result implements tilelibrary.Checks: the result of a check that ended in
// the last hour, among the newest keepResults, with the definition it
// checked (with its HTML) and each of the candidate's own samples, whether
// it passed and whether it is synthetic. Preflights are not kept, nor
// checks whose definition was not valid.
func (c *Checker) Result(_ context.Context, id string) (tilelibrary.CheckResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k, ok := c.results[id]
	if !ok || c.now().Sub(k.at) >= keepFor {
		return tilelibrary.CheckResult{}, false
	}
	r := k.result
	r.Samples = slices.Clone(r.Samples)
	return r, true
}

// keep records a check's result for Result.
func (c *Checker) keep(j *job, result Result) {
	if j.Kind != JobCheck || len(j.encoded) == 0 {
		return
	}
	r := tilelibrary.CheckResult{ID: j.ID, Verdict: result.Verdict, Digest: j.Digest, Definition: slices.Clone(j.encoded)}
	for _, s := range j.own {
		passed := result.Verdict == VerdictPass
		if !passed && result.Verdict == VerdictFail {
			// A failed check: a sample passed when no error is its own or
			// the whole candidate's.
			passed = !slices.ContainsFunc(result.Errors, func(p Problem) bool { return p.Sample == s.Name || p.Sample == "" })
		}
		r.Samples = append(r.Samples, tilelibrary.CheckedSample{Sample: s, Passed: passed, Synthetic: synthetic(s)})
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.results == nil {
		c.results = map[string]kept{}
	}
	c.results[j.ID] = kept{at: now, result: r}
	c.order = append(c.order, j.ID)
	for len(c.order) > 0 && (len(c.order) > keepResults || now.Sub(c.results[c.order[0]].at) >= keepFor) {
		delete(c.results, c.order[0])
		c.order = c.order[1:]
	}
}

// synthetic reports whether a sample is its own: it has hunks or items, and
// every citation of its data, on either face, resolves to them.
func synthetic(s library.Sample) bool {
	if len(s.Hunks)+len(s.Items) == 0 {
		return false
	}
	for _, face := range []string{"glance", "detail"} {
		if _, unresolved := s.Refs(face); len(unresolved) > 0 {
			return false
		}
	}
	return true
}

func (c *Checker) now() time.Time {
	if c.opts.Now != nil {
		return c.opts.Now()
	}
	return time.Now()
}
