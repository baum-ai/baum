package uiserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// decisionViewTTL is how long DecisionViews reuses a resolved decision model
// for unchanged preferences.
const decisionViewTTL = 45 * time.Second

// DecisionViews answers what GET /api/settings/decisions answers, for the MCP
// server's Settings tool. The route resolves an auto backend by running the
// harnesses' version checks (DecisionModel) on every read, as the app expects
// when it opens the section; a tool an agent may call again and again must not
// start those processes each time. DecisionViews resolves at most once per
// TTL for the same preferences (any change to them resolves again), and
// concurrent calls share one resolution. The route itself is unchanged.
type DecisionViews struct {
	API LocalAPI
	// TTL defaults to 45 seconds; Now to time.Now.
	TTL time.Duration
	Now func() time.Time

	mu     sync.Mutex
	key    string
	at     time.Time
	view   decisionView
	flight singleflight.Group
}

func (d *DecisionViews) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Get returns {"decisions": ...} as the route does.
func (d *DecisionViews) Get(ctx context.Context) (json.RawMessage, error) {
	prefs, err := d.API.Registry.Preferences(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(prefs)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	key := hex.EncodeToString(sum[:])
	ttl := d.TTL
	if ttl <= 0 {
		ttl = decisionViewTTL
	}
	cached := func() (decisionView, bool) {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.view, d.key == key && d.now().Sub(d.at) < ttl
	}
	if view, ok := cached(); ok {
		return json.Marshal(map[string]decisionView{"decisions": view})
	}
	// One resolution for every caller waiting on these preferences; a caller
	// that goes away does not cancel it for the others. A caller that missed
	// the cache just before a resolution finished finds its answer here.
	v, _, _ := d.flight.Do(key, func() (any, error) {
		if view, ok := cached(); ok {
			return view, nil
		}
		view := d.API.decisionView(context.WithoutCancel(ctx), prefs)
		d.mu.Lock()
		d.key, d.at, d.view = key, d.now(), view
		d.mu.Unlock()
		return view, nil
	})
	return json.Marshal(map[string]decisionView{"decisions": v.(decisionView)})
}
