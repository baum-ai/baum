package prupdate

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/github"
)

// How long the Inbox panel's reads are served without asking GitHub again:
// a pull request's review threads while their count still matches
// discovery's, and a head commit's failing checks.
const (
	DefaultThreadTTL = 10 * time.Minute
	DefaultChecksTTL = 2 * time.Minute
)

// Cache keeps what the Inbox panel reads of a pull request, so moving through
// the Inbox does not read GitHub every time a panel shows one. A thread
// resolved from the app leaves the cached list at once, and a re-run of the
// checks forgets the head's failing checks. What it holds was read with the
// signed-in account's credential, so a change of account empties it
// (ResetIdentity). It is safe for concurrent use; the zero value is ready.
type Cache struct {
	ThreadTTL, ChecksTTL time.Duration
	Now                  func() time.Time

	mu      sync.Mutex
	threads map[string]cached[Threads]
	checks  map[string]cached[Checks]
	// gen counts account changes, so a read that began before one is not
	// stored.
	gen uint64
}

type cached[T any] struct {
	value T
	at    time.Time
}

func pullKey(repository github.Repository, number int) string {
	return fmt.Sprintf("%s#%d", strings.ToLower(repository.String()), number)
}

func headKey(repository github.Repository, sha string) string {
	return strings.ToLower(repository.String()) + "@" + sha
}

func (c *Cache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// ResetIdentity forgets everything: the GitHub account changed or was
// disconnected. A read already under way is not stored.
func (c *Cache) ResetIdentity() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.threads, c.checks = nil, nil
	c.gen++
}

// generation is what a read from GitHub that begins now stores under.
func (c *Cache) generation() uint64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}

// fresh returns the entry under key while it is younger than ttl.
func fresh[T any](entries map[string]cached[T], key string, ttl time.Duration, now time.Time) (T, bool) {
	entry, ok := entries[key]
	if !ok || now.Sub(entry.at) >= ttl {
		var zero T
		return zero, false
	}
	return entry.value, true
}

// store puts value under key, dropping entries older than ttl.
func store[T any](entries *map[string]cached[T], key string, value T, ttl time.Duration, now time.Time) {
	if *entries == nil {
		*entries = map[string]cached[T]{}
	}
	for k, entry := range *entries {
		if now.Sub(entry.at) >= ttl {
			delete(*entries, k)
		}
	}
	(*entries)[key] = cached[T]{value, now}
}

// cachedThreads returns the pull request's cached threads. unresolved is
// discovery's count, or negative when unknown; a list that no longer matches
// it is stale. A capped list is never compared.
func (c *Cache) cachedThreads(key string, unresolved int) (Threads, bool) {
	if c == nil {
		return Threads{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	threads, ok := fresh(c.threads, key, cmp.Or(c.ThreadTTL, DefaultThreadTTL), c.now())
	if !ok || (unresolved >= 0 && !threads.Capped && len(threads.Threads) != unresolved) {
		return Threads{}, false
	}
	return threads, true
}

// storeThreads stores what was just read, unless the account changed since
// the read began (gen).
func (c *Cache) storeThreads(key string, gen uint64, threads Threads) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen == c.gen {
		store(&c.threads, key, threads, cmp.Or(c.ThreadTTL, DefaultThreadTTL), c.now())
	}
}

// resolved drops a thread resolved from the app, keeping the entry's age.
func (c *Cache) resolved(key, id string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.threads[key]
	if !ok {
		return
	}
	entry.value.Threads = slices.DeleteFunc(slices.Clone(entry.value.Threads), func(t Thread) bool { return t.ID == id })
	c.threads[key] = entry
}

// cachedChecks returns the head's cached failing checks.
func (c *Cache) cachedChecks(key string) (Checks, bool) {
	if c == nil {
		return Checks{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return fresh(c.checks, key, cmp.Or(c.ChecksTTL, DefaultChecksTTL), c.now())
}

// storeChecks stores what was just read, unless the account changed since
// the read began (gen).
func (c *Cache) storeChecks(key string, gen uint64, checks Checks) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen == c.gen {
		store(&c.checks, key, checks, cmp.Or(c.ChecksTTL, DefaultChecksTTL), c.now())
	}
}

// rerun forgets the head's failing checks: some are running again.
func (c *Cache) rerun(key string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.checks, key)
}
