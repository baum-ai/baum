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
// discovery's, and a head commit's failing checks. Threads resolved on GitHub
// do not change what discovery's probe sees, so the panel reads them again
// soon, when shown. All of a head's checks, whose runs finish meanwhile, are
// kept only briefly.
const (
	DefaultThreadTTL    = time.Minute
	DefaultChecksTTL    = 2 * time.Minute
	DefaultAllChecksTTL = 20 * time.Second
)

// Cache keeps what the Inbox panel reads of a pull request, so moving through
// the Inbox does not read GitHub every time a panel shows one. A thread
// resolved from the app leaves the cached list at once, and a re-run of the
// checks forgets the head's failing checks. What it holds was read with the
// signed-in account's credential, so a change of account empties it
// (ResetIdentity). It is safe for concurrent use; the zero value is ready.
type Cache struct {
	ThreadTTL, ChecksTTL, AllChecksTTL time.Duration
	Now                                func() time.Time

	mu      sync.Mutex
	threads map[string]cached[Threads]
	checks  map[string]cached[Checks]
	// allChecks are all of a head's checks.
	allChecks map[string]cached[CheckList]
	// logs are the tails of finished check runs' logs, least recently used
	// first in logOrder.
	logs     map[string]CheckLog
	logOrder []string
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
	c.threads, c.checks, c.allChecks = nil, nil, nil
	c.logs, c.logOrder = nil, nil
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

// cachedAllChecks returns the head's cached list of all its checks.
func (c *Cache) cachedAllChecks(key string) (CheckList, bool) {
	if c == nil {
		return CheckList{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return fresh(c.allChecks, key, cmp.Or(c.AllChecksTTL, DefaultAllChecksTTL), c.now())
}

// storeAllChecks stores what was just read, unless the account changed since
// the read began (gen).
func (c *Cache) storeAllChecks(key string, gen uint64, checks CheckList) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen == c.gen {
		store(&c.allChecks, key, checks, cmp.Or(c.AllChecksTTL, DefaultAllChecksTTL), c.now())
	}
}

func logKey(repository github.Repository, check int64) string {
	return fmt.Sprintf("%s/%d", strings.ToLower(repository.String()), check)
}

// cachedLog returns the check run's cached log tail, and marks it the most
// recently used.
func (c *Cache) cachedLog(key string) (CheckLog, bool) {
	if c == nil {
		return CheckLog{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	log, ok := c.logs[key]
	if ok {
		c.logOrder = append(slices.DeleteFunc(c.logOrder, func(k string) bool { return k == key }), key)
	}
	return log, ok
}

// storeLog keeps a finished check run's log tail, the least recently used
// of logCacheSize going first, unless the account changed since the read
// began (gen). A finished log does not change, so it has no age limit.
func (c *Cache) storeLog(key string, gen uint64, log CheckLog) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen {
		return
	}
	if c.logs == nil {
		c.logs = map[string]CheckLog{}
	}
	c.logs[key] = log
	c.logOrder = append(slices.DeleteFunc(c.logOrder, func(k string) bool { return k == key }), key)
	for len(c.logOrder) > logCacheSize {
		delete(c.logs, c.logOrder[0])
		c.logOrder = c.logOrder[1:]
	}
}

// rerun forgets the head's checks: some are running again.
func (c *Cache) rerun(key string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.checks, key)
	delete(c.allChecks, key)
	// A re-run replaces check runs, so no kept log is trusted to be the
	// latest one.
	c.logs, c.logOrder = nil, nil
}
