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

// PullRequestSearcher finds the pull requests the signed-in account opened;
// *github.Client is one.
type PullRequestSearcher interface {
	AuthoredPullRequests(ctx context.Context, repository github.Repository, since time.Time) ([]github.AuthoredPullRequest, error)
}

// Delivery caches, per picked repository, one GitHub search for the pull
// requests the person opened in the last Retention. A request never waits
// for GitHub: it gets what is cached and starts a search for what is missing
// or older than DeliveryTTL.
type Delivery struct {
	Search PullRequestSearcher
	Now    func() time.Time

	mu      sync.Mutex
	entries map[string]*deliveryEntry
	// ctx is the daemon's, which searches run under.
	ctx context.Context
}

type deliveryEntry struct {
	fetchedAt time.Time
	pulls     []github.AuthoredPullRequest
	// reason is why the last search failed, in words; the pulls of an
	// earlier search stay.
	reason   string
	fetching bool
	loaded   bool
}

func (d *Delivery) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
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

// Pulls returns what is cached for each repository and starts a search for
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
		ttl := DeliveryTTL
		if e.reason != "" {
			ttl = deliveryRetry
		}
		if !e.fetching && (!e.loaded && e.reason == "" || now.Sub(e.fetchedAt) >= ttl) {
			e.fetching = true
			go d.fetch(name, e)
		}
		out = append(out, repositoryPulls{Repository: name, Pulls: slices.Clone(e.pulls), Loaded: e.loaded, Reason: e.reason, FetchedAt: e.fetchedAt})
	}
	return out
}

func (d *Delivery) fetch(name string, e *deliveryEntry) {
	d.mu.Lock()
	parent := d.ctx
	d.mu.Unlock()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, deliveryTimeout)
	defer cancel()
	var pulls []github.AuthoredPullRequest
	repo, err := github.ParseRepository(name)
	if err == nil {
		since := d.now().Add(-Retention)
		pulls, err = d.Search.AuthoredPullRequests(ctx, repo, since)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.entries[name] != e {
		// Reset while it ran: the answer is another account's.
		return
	}
	e.fetching = false
	e.fetchedAt = d.now()
	if err != nil {
		e.reason = deliveryReason(err)
	} else {
		e.pulls, e.loaded, e.reason = pulls, true, ""
	}
}

// Reset forgets every answer, as when the signed-in GitHub account changes.
func (d *Delivery) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.entries = nil
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
