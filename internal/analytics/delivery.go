package analytics

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/github"
)

// DeliveryTTL is how long one repository's search answer serves, and
// deliveryRetry how soon a failed search is tried again.
const (
	DeliveryTTL   = time.Hour
	deliveryRetry = 5 * time.Minute
)

// deliveryTimeout bounds one repository's search, which runs apart from any
// request.
const deliveryTimeout = 2 * time.Minute

// Search pacing. GitHub's search API allows 30 requests a minute, and a
// search is up to five serial requests (one a page), so searches run on a
// small pool and each request is given searchSpacing: 24 a minute across the
// pool, under the limit.
const (
	deliveryWorkers = 3
	searchSpacing   = 2500 * time.Millisecond
	searchPageSize  = 50
)

// PullRequestSearcher finds the pull requests the signed-in account opened;
// *github.Client is one.
type PullRequestSearcher interface {
	AuthoredPullRequests(ctx context.Context, repository github.Repository, since time.Time) ([]github.AuthoredPullRequest, error)
}

// Delivery caches, per picked repository, one GitHub search for the pull
// requests the person opened in the last Retention. A request never waits
// for GitHub: it gets what is cached and queues a search for what is missing
// or older than DeliveryTTL. Queued searches run on at most deliveryWorkers
// workers, paced to stay under GitHub's search limit; a rate limit pauses the
// rest of the queue, whose repositories keep what they had and are tried again
// after the pause.
type Delivery struct {
	Search PullRequestSearcher
	Now    func() time.Time
	// Paused is when the host's GitHub rate limit lets requests resume, as
	// the background refresh knows it; nil means unknown. Limited records a
	// rate limit a search hit, so the refresh waits it out too.
	Paused  func(host string) time.Time
	Limited func(host string, until time.Time)

	// tick and sleep pace the searches; tests replace them.
	tick  func() time.Time
	sleep func(ctx context.Context, until time.Time) error

	mu      sync.Mutex
	entries map[string]*deliveryEntry
	queue   []queuedSearch
	workers int
	// next is when the next search request may start.
	next time.Time
	// ctx is the daemon's, which searches run under.
	ctx context.Context
}

type queuedSearch struct {
	name  string
	entry *deliveryEntry
}

type deliveryEntry struct {
	fetchedAt time.Time
	pulls     []github.AuthoredPullRequest
	// reason is why the last search failed, in words; the pulls of an
	// earlier search stay.
	reason string
	// retryAt is the earliest a rate-limited search is tried again, zero
	// meaning deliveryRetry after fetchedAt.
	retryAt  time.Time
	fetching bool
	loaded   bool
}

func (d *Delivery) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Delivery) clock() time.Time {
	if d.tick != nil {
		return d.tick()
	}
	return time.Now()
}

// wait blocks until the pacing clock reaches until.
func (d *Delivery) wait(ctx context.Context, until time.Time) error {
	if d.sleep != nil {
		return d.sleep(ctx, until)
	}
	timer := time.NewTimer(time.Until(until))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Run gives the searches the daemon's lifetime: they stop when ctx ends.
func (d *Delivery) Run(ctx context.Context) {
	d.mu.Lock()
	d.ctx = ctx
	d.mu.Unlock()
	<-ctx.Done()
}

// repositoryPulls is one repository's cached answer.
type repositoryPulls struct {
	Repository string
	Pulls      []github.AuthoredPullRequest
	Loaded     bool
	Reason     string
	FetchedAt  time.Time
}

// Pulls returns what is cached for each repository and queues a search for
// those with nothing fresh.
func (d *Delivery) Pulls(repositories []string) []repositoryPulls {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.entries == nil {
		d.entries = map[string]*deliveryEntry{}
	}
	now := d.now()
	out := make([]repositoryPulls, 0, len(repositories))
	for _, name := range repositories {
		e := d.entries[name]
		if e == nil {
			e = &deliveryEntry{}
			d.entries[name] = e
		}
		due := !e.loaded && e.reason == ""
		switch {
		case e.reason != "":
			retryAt := e.retryAt
			if retryAt.IsZero() {
				retryAt = e.fetchedAt.Add(deliveryRetry)
			}
			due = !now.Before(retryAt)
		case e.loaded:
			due = now.Sub(e.fetchedAt) >= DeliveryTTL
		}
		if !e.fetching && due {
			e.fetching = true
			d.queue = append(d.queue, queuedSearch{name: name, entry: e})
		}
		out = append(out, repositoryPulls{Repository: name, Pulls: slices.Clone(e.pulls), Loaded: e.loaded, Reason: e.reason, FetchedAt: e.fetchedAt})
	}
	for d.workers < deliveryWorkers && d.workers < len(d.queue) {
		d.workers++
		go d.work()
	}
	return out
}

// work drains the queue, one search at a time, and ends when it is empty.
func (d *Delivery) work() {
	for {
		d.mu.Lock()
		if len(d.queue) == 0 {
			d.workers--
			d.mu.Unlock()
			return
		}
		item := d.queue[0]
		d.queue = d.queue[1:]
		parent := d.ctx
		d.mu.Unlock()
		if parent == nil {
			parent = context.Background()
		}
		d.fetch(parent, item.name, item.entry)
	}
}

// pages is how many requests a search of that many pull requests makes.
func pages(pulls int) int { return max(1, (pulls+searchPageSize-1)/searchPageSize) }

// reserve takes request slots for a search, as many as its last answer
// needed (one when it has none), and returns when it may start.
func (d *Delivery) reserve(requests int) time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	start := d.next
	if now := d.clock(); start.Before(now) {
		start = now
	}
	d.next = start.Add(time.Duration(requests) * searchSpacing)
	return start
}

// charge accounts for requests a search made beyond the slots it reserved.
func (d *Delivery) charge(pulls, reserved int) {
	if extra := pages(pulls) - reserved; extra > 0 {
		d.mu.Lock()
		d.next = d.next.Add(time.Duration(extra) * searchSpacing)
		d.mu.Unlock()
	}
}

// settle records a search's outcome unless the entries were reset meanwhile.
func (d *Delivery) settle(name string, e *deliveryEntry, update func(*deliveryEntry)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.entries[name] != e {
		// Reset while it ran: the answer is another account's.
		return
	}
	e.fetching = false
	update(e)
}

func (d *Delivery) fetch(parent context.Context, name string, e *deliveryEntry) {
	repo, err := github.ParseRepository(name)
	if err != nil {
		d.fail(name, e, err)
		return
	}
	// A rate limit the refresh or an earlier search hit holds this one back.
	if d.Paused != nil {
		if resume := d.Paused(repo.Host); resume.After(d.now()) {
			d.fail(name, e, &github.RateLimitedError{ResumeAt: resume})
			return
		}
	}
	d.mu.Lock()
	reserved := pages(len(e.pulls))
	d.mu.Unlock()
	if start := d.reserve(reserved); start.After(d.clock()) {
		if err := d.wait(parent, start); err != nil {
			// The daemon is stopping; leave the entry to be searched again.
			d.settle(name, e, func(*deliveryEntry) {})
			return
		}
	}
	ctx, cancel := context.WithTimeout(parent, deliveryTimeout)
	defer cancel()
	pulls, err := d.Search.AuthoredPullRequests(ctx, repo, d.now().Add(-Retention))
	if parent.Err() != nil {
		d.settle(name, e, func(*deliveryEntry) {})
		return
	}
	d.charge(len(pulls), reserved)
	if err != nil {
		d.fail(name, e, err)
		return
	}
	d.settle(name, e, func(e *deliveryEntry) {
		e.fetchedAt, e.pulls, e.loaded, e.reason, e.retryAt = d.now(), pulls, true, "", time.Time{}
	})
}

// fail records a failed search. A rate limit also holds back every queued
// search until the limit resets: their repositories keep what they had and
// carry the reason, so the panel shows the others, and are tried again then.
func (d *Delivery) fail(name string, e *deliveryEntry, err error) {
	now := d.now()
	var limited *github.RateLimitedError
	if !errors.As(err, &limited) {
		d.settle(name, e, func(e *deliveryEntry) { e.fetchedAt, e.reason, e.retryAt = now, deliveryReason(err), time.Time{} })
		return
	}
	resume := limited.ResumeAt
	if resume.IsZero() || resume.Before(now.Add(deliveryRetry)) {
		resume = now.Add(deliveryRetry)
	}
	if d.Limited != nil {
		if repo, perr := github.ParseRepository(name); perr == nil {
			d.Limited(repo.Host, resume)
		}
	}
	mark := func(e *deliveryEntry) { e.fetchedAt, e.reason, e.retryAt = now, deliveryReason(err), resume }
	d.settle(name, e, mark)
	d.mu.Lock()
	queued := d.queue
	d.queue = nil
	d.mu.Unlock()
	for _, item := range queued {
		d.settle(item.name, item.entry, mark)
	}
}

// Reset forgets every answer, as when the signed-in GitHub account changes.
func (d *Delivery) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.entries = nil
	// Queued searches belong to the old account; a running one is dropped
	// when it settles.
	d.queue = nil
}

// deliveryReason says why a search failed in fixed words, never GitHub's.
func deliveryReason(err error) string {
	var limited *github.RateLimitedError
	switch {
	case errors.As(err, &limited):
		return "GitHub's request limit is used up; baum tries again in a few minutes."
	case errors.Is(err, github.ErrNotFound):
		return "GitHub did not find the repository with the signed-in account."
	}
	return "baum could not read your pull requests from GitHub. Check the GitHub connection in Settings."
}
