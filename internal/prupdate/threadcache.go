package prupdate

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/github"
)

// DefaultThreadTTL is how long a pull request's thread list is served
// without reading GitHub again, while its count still matches discovery's.
const DefaultThreadTTL = 10 * time.Minute

// ThreadCache keeps the review threads read for each pull request, so moving
// through the Inbox does not read GitHub every time a panel shows one. An
// entry is served while it is younger than TTL and holds as many threads as
// discovery counts unresolved; a thread resolved from the app leaves it at
// once. What it holds was read with the signed-in account's credential, so
// a change of account empties it (ResetIdentity). It is safe for concurrent
// use; the zero value is ready.
type ThreadCache struct {
	TTL time.Duration
	Now func() time.Time

	mu      sync.Mutex
	entries map[string]cachedThreads
	// gen counts account changes, so a read that began before one is not
	// stored.
	gen uint64
}

type cachedThreads struct {
	threads Threads
	at      time.Time
}

func threadKey(repository github.Repository, number int) string {
	return fmt.Sprintf("%s#%d", strings.ToLower(repository.String()), number)
}

func (c *ThreadCache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *ThreadCache) ttl() time.Duration {
	if c.TTL > 0 {
		return c.TTL
	}
	return DefaultThreadTTL
}

// ResetIdentity forgets every list: the GitHub account changed or was
// disconnected. A read already under way is not stored.
func (c *ThreadCache) ResetIdentity() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = nil
	c.gen++
}

// get returns the cached threads of the pull request, or the generation a
// read from GitHub must store under. unresolved is discovery's count, or
// negative when unknown; a list that no longer matches it is stale. A capped
// list is never compared.
func (c *ThreadCache) get(key string, unresolved int) (Threads, uint64, bool) {
	if c == nil {
		return Threads{}, 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	switch {
	case !ok || c.now().Sub(entry.at) >= c.ttl():
		return Threads{}, c.gen, false
	case unresolved >= 0 && !entry.threads.Capped && len(entry.threads.Threads) != unresolved:
		return Threads{}, c.gen, false
	}
	return entry.threads, c.gen, true
}

// put stores what was just read, unless the account changed since the read
// began (gen), dropping expired entries.
func (c *ThreadCache) put(key string, gen uint64, threads Threads) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen {
		return
	}
	now := c.now()
	if c.entries == nil {
		c.entries = map[string]cachedThreads{}
	}
	for k, entry := range c.entries {
		if now.Sub(entry.at) >= c.ttl() {
			delete(c.entries, k)
		}
	}
	c.entries[key] = cachedThreads{threads, now}
}

// resolved drops a thread resolved from the app, keeping the entry's age.
func (c *ThreadCache) resolved(key, id string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return
	}
	entry.threads.Threads = slices.DeleteFunc(slices.Clone(entry.threads.Threads), func(t Thread) bool { return t.ID == id })
	c.entries[key] = entry
}
