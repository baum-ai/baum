package decisionlog

import (
	"context"
	"sync"
	"time"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/decisions"
	"github.com/baum-ai/baum/internal/livesessions"
	"github.com/baum-ai/baum/internal/transcript"
	"github.com/baum-ai/baum/sessions/cursortranscript"
)

// MaxCachedPrompts is how many sessions' prompts the Timeline keeps, the
// least recently read making room.
const MaxCachedPrompts = 16

// timelineTurn is a prompt a decision cites, as the viewer's DecisionDetail
// turns read it.
type timelineTurn struct {
	Ordinal     int        `json:"ordinal"`
	TriggeredAt *time.Time `json:"triggered_at"`
	Text        string     `json:"text"`
}

// timelinePrompts reads the human prompts of sessions' transcripts on this
// computer, as decision capture renders them: wrappers stripped and secrets
// redacted (decisions.Render). A session's are kept by its transcript's
// version, so an unchanged file is read once.
type timelinePrompts struct {
	once     sync.Once
	redactor *bundle.Redactor
	err      error

	mu    sync.Mutex
	cache map[SessionKey]*cachedPrompts
}

type cachedPrompts struct {
	version string
	used    time.Time
	turns   map[int]timelineTurn
}

// read answers with the session's prompts by ordinal, ok false when its
// transcript is not on this computer or cannot be read.
func (p *timelinePrompts) read(ctx context.Context, home, cursorStore string, key SessionKey, path string) (map[int]timelineTurn, bool) {
	p.once.Do(func() { p.redactor, p.err = bundle.NewRedactor() })
	if p.err != nil {
		return nil, false
	}
	loc, err := livesessions.Locate(home, cursorStore, key.Agent, key.ID, path)
	if err != nil {
		return nil, false
	}
	version, err := loc.Version()
	if err != nil {
		return nil, false
	}
	p.mu.Lock()
	if c := p.cache[key]; c != nil && c.version == version {
		c.used = time.Now()
		p.mu.Unlock()
		return c.turns, true
	}
	p.mu.Unlock()
	graph, err := loc.Graph()
	if err != nil || graph == nil {
		return nil, false
	}
	rendered, err := decisions.Render(ctx, graph, p.redactor, decisions.WithVendor(string(loc.Vendor)))
	if err != nil {
		return nil, false
	}
	times := turnTimes(graph)
	turns := map[int]timelineTurn{}
	for _, turn := range rendered.Turns {
		// A turn that was only the harness's wrappers is no prompt.
		if turn.Human == "" {
			continue
		}
		each := timelineTurn{Ordinal: turn.Ordinal, Text: turn.Human}
		if at, ok := times[turn.Ordinal]; ok {
			each.TriggeredAt = &at
		}
		turns[turn.Ordinal] = each
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cache == nil {
		p.cache = map[SessionKey]*cachedPrompts{}
	}
	delete(p.cache, key)
	for len(p.cache) >= MaxCachedPrompts {
		var oldest SessionKey
		var at time.Time
		for k, c := range p.cache {
			if at.IsZero() || c.used.Before(at) {
				oldest, at = k, c.used
			}
		}
		delete(p.cache, oldest)
	}
	p.cache[key] = &cachedPrompts{version: version, used: time.Now(), turns: turns}
	return turns, true
}

func (t *Timeline) home() string {
	if t.Home != "" {
		return t.Home
	}
	return transcript.UserHome()
}

func (t *Timeline) cursorStore() string {
	if t.CursorStore != "" {
		return t.CursorStore
	}
	return cursortranscript.DefaultStorePath()
}
