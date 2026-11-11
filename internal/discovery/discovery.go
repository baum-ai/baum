// Package discovery maintains a private GitHub inbox cache. It
// performs no git fetch, generation or write to GitHub.
package discovery

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/appdb"
	"github.com/baum-ai/baum/internal/github"
)

type PullRequest struct {
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	HeadSHA    string `json:"head_sha"`
	// HeadRef is the head branch's name, which links a local session on that
	// branch to the pull request. It is empty in a snapshot cached before it
	// was kept, until the next poll.
	HeadRef string `json:"head_ref"`
	// HeadRepository is the canonical host/repository owning the head branch.
	HeadRepository string `json:"head_repository,omitempty"`
	// BaseRef is the base branch's name; a pull request whose base is
	// another open pull request's head branch is stacked on it. Empty in a
	// snapshot cached before it was kept, until the next poll.
	BaseRef string `json:"base_ref"`
	// Additions, Deletions and ChangedFiles size the change as GitHub counts
	// it. They are zero in a snapshot cached before they were kept, until the
	// next poll.
	Additions    int    `json:"additions"`
	Deletions    int    `json:"deletions"`
	ChangedFiles int    `json:"changed_files"`
	Title        string `json:"title"`
	URL          string `json:"url"`
	Author       string `json:"author"`
	// AuthorBot is true for a GitHub App or other bot author. Snapshots cached
	// before it existed decode as false.
	AuthorBot             bool   `json:"author_bot"`
	State                 string `json:"state"`
	Draft                 bool   `json:"draft"`
	Fork                  bool   `json:"fork"`
	AuthoredByMe          bool   `json:"authored_by_me"`
	ReviewRequestedFromMe bool   `json:"review_requested_from_me"`
	// AssignedToMe is true when the viewer is one of the first 20 assignees.
	// Snapshots cached before it existed decode as false.
	AssignedToMe bool `json:"assigned_to_me"`
	// Assigned is true when the pull request has at least one of the first 20
	// assignees fetched, whoever they are. Snapshots cached before it existed
	// decode as false.
	Assigned bool `json:"assigned"`
	// ReviewRequested is true when a review is requested from at least one
	// user or team, whoever they are. Snapshots cached before it existed
	// decode as false.
	ReviewRequested bool `json:"review_requested"`
	// Nil means the token could not establish repository write permission.
	AuthorHasWrite *bool `json:"author_has_write"`
	// ViewerCanPush is whether the signed-in account may push to the
	// repository (GitHub's viewerPermission is write, maintain or admin),
	// which a fix needs; nil when GitHub did not say.
	ViewerCanPush *bool `json:"viewer_can_push"`
	// False means team membership was unavailable; false review-request flags
	// must not be interpreted as proof that no team requested this viewer.
	TeamMembershipKnown bool   `json:"team_membership_known"`
	Review              string `json:"review"`
	// ReviewRequired is true only when GitHub's reviewDecision is REVIEW_REQUIRED;
	// nil means an older snapshot did not retain the requirement.
	ReviewRequired *bool  `json:"review_required,omitempty"`
	Checks         string `json:"checks"`
	Merge          string `json:"merge"`
	// Behind is GitHub's report (mergeStateStatus BEHIND) that the head
	// branch is out of date with its base and not conflicting, which Update
	// branch fixes. A draft never reports it.
	Behind           bool `json:"behind"`
	Unresolved       *int `json:"unresolved"`
	UnresolvedCapped bool `json:"unresolved_capped"`
	// UpdatedAt is GitHub's last update of the pull request: a push, an edit,
	// a review or a comment. The background refresh re-reads a saved review's
	// evidence when it is newer than the last read.
	UpdatedAt time.Time `json:"updated_at,omitzero"`
}

// Observation contains only safe current GitHub metadata, never description
// bodies, signed image URLs or session contents.
type Observation struct {
	Identity  string    `json:"-"`
	Number    int       `json:"number"`
	HeadSHA   string    `json:"head_sha"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	Draft     bool      `json:"draft"`
	FetchedAt time.Time `json:"fetched_at"`
	Checks    string    `json:"checks"`
}

type RateLimit struct {
	Cost      *int      `json:"cost,omitempty"`
	Remaining *int      `json:"remaining"`
	Reset     time.Time `json:"reset"`
	RetryAt   time.Time `json:"retry_at"`
}

type Snapshot struct {
	Epoch        uint64        `json:"-"`
	Repository   string        `json:"repository"`
	Observations []Observation `json:"observations,omitempty"`
	Viewer       string        `json:"viewer"`
	PullRequests []PullRequest `json:"pull_requests"`
	Stale        bool          `json:"stale"`
	FetchedAt    time.Time     `json:"fetched_at"`
	NextPoll     time.Time     `json:"next_poll"`
	Rate         RateLimit     `json:"rate"`
	Error        string        `json:"error,omitempty"`
	// Settling: this poll found a new head or a mergeability GitHub forgot,
	// or GitHub still works one out since, so NextPoll comes after
	// settleInterval.
	Settling bool `json:"settling,omitempty"`
}

type cachedResponse struct {
	ETag string
	Body json.RawMessage
}
type diskCache struct {
	// Format is cacheFormat when the cache was written; a cache of an older
	// format drops what it cannot trust (upgrade) but keeps its snapshot, which
	// stays shown until the first full sweep replaces it.
	Format      int `json:",omitempty"`
	Snapshot    Snapshot
	Responses   map[string]cachedResponse
	GraphQLRate RateLimit
	Interval    time.Duration
	// Fingerprint is the last complete poll's, which Probe compares with.
	Fingerprint fingerprint
	// Settling counts the quick polls since one found a new head (settle).
	Settling int `json:",omitempty"`
	// ChecksSince is when the polls began to find checks running, or the last
	// one that found the pull requests changed meanwhile (checksHold).
	ChecksSince time.Time `json:",omitzero"`
	// Carried are the pull requests whose merge state the last poll kept from
	// an earlier one while GitHub worked it out again (carryMerge).
	Carried map[int]bool `json:",omitempty"`
	// Pulls are the open pull requests as GitHub last described them, by
	// number. Each poll builds the snapshot from them.
	Pulls []gqlPR `json:",omitempty"`
	// Complete says Pulls holds every open pull request: a full sweep finished
	// since the cache took this format. Until then the snapshot keeps the
	// older format's pull requests that no read has replaced or closed.
	Complete bool `json:",omitempty"`
	// Cursor is GitHub's update time up to which every change is in Pulls.
	// The next incremental read stops below Cursor minus cursorOverlap.
	Cursor time.Time `json:",omitzero"`
	// Swept is when the last full sweep finished, on this computer's clock.
	Swept time.Time `json:",omitzero"`
	// Sweep is a full sweep that a poll left unfinished; the next poll goes on
	// from its cursor.
	Sweep *sweep `json:",omitempty"`
	// Authors are the pull request authors' write permission, by lower-case
	// login, as GitHub answered within permissionTTL.
	Authors map[string]authorPermission `json:",omitempty"`
	// Teams is the account's team memberships; only the budget record, which
	// the credential's repositories share, keeps it.
	Teams teamMembership `json:",omitzero"`
}

// sweep is a full sweep under way: the cursor after its last page read, the
// pull requests it has seen, and GitHub's newest update when it began.
type sweep struct {
	After string    `json:",omitempty"`
	Seen  []int     `json:",omitempty"`
	Floor time.Time `json:",omitzero"`
}

// authorPermission is one author's write permission: nil Push when GitHub
// refused to say (403 or 404), which is an answer kept as long as the others.
type authorPermission struct {
	Push *bool     `json:",omitempty"`
	Read time.Time `json:",omitzero"`
}

// After a push GitHub works out in the background whether the pull request
// still merges cleanly, and a poll right after it can read the old answer or
// none. A poll that finds a new head, or a new pull request, so polls again
// after settleInterval, as do up to maxSettles more while GitHub still
// reports a mergeability it has not worked out. Then the usual backoff goes on.
// A merge into the base makes GitHub work out every other pull request's
// mergeability again, with no head moving and nothing the probe sees, so one
// whose answer GitHub forgot settles the same way.
const (
	settleInterval = 15 * time.Second
	maxSettles     = 3
)

// A check that finishes leaves the pull request's update time as it was, so
// neither the probe nor the backoff's "unchanged" sees it coming. While an
// open pull request has checks running the polls therefore stay at the first
// interval rather than backing off. Checks that never report (a required one
// nothing runs, say) would hold it for ever: the hold ends once the pull
// requests have stayed as they are for checksHold, and the usual backoff goes
// on until they change.
const checksHold = time.Hour

// How a poll reads the open pull requests. Most polls are incremental: they
// read the pull requests updated since the cursor, newest first and of any
// state, so one that closed or merged is seen and leaves the cache. A full
// sweep reads every open one at least every sweepInterval, and whenever the
// cache's count or newest pull request disagrees with GitHub's.
const (
	sweepInterval = 10 * time.Minute
	// cursorOverlap is how far below the cursor an incremental read goes on:
	// GitHub's update times have one-second precision and are set by more
	// than one server, so a pull request updated in the cursor's second, or
	// recorded a little late, is read again rather than skipped.
	cursorOverlap = 2 * time.Minute
	// permissionTTL is how long an author's permission answer is reused.
	permissionTTL = time.Hour
	// watchLimit bounds the pull requests an incremental poll re-reads by
	// number because GitHub's update time does not follow what changed.
	watchLimit = 50
	// windowPageLimit bounds an incremental read, 500 pull requests at
	// inboxPageSize; a longer window is replaced by a sweep.
	windowPageLimit = 20
	// pageLimit bounds the pages of one read.
	pageLimit = 1000
)

// cacheFormat changes when the cache keeps more than before: 1 added the pull
// requests' head branch, 2 their base branch, 3 GitHub's rows, the cursor and
// the permission answers of incremental polling.
const cacheFormat = 3

// ErrTimeout is a poll or probe that ran out of time. Whatever it read before
// is kept.
var ErrTimeout = errors.New("GitHub discovery timed out")

type Service struct {
	client    *github.Client
	directory string
	// mu guards locks, the per-repository locks that serialize a repository's
	// polls and cache writes. Polls of different repositories run at once.
	mu     sync.Mutex
	locks  map[string]*sync.Mutex
	liveMu sync.Mutex
	// writeMu serializes this service's cache writes to app.sqlite.
	writeMu sync.Mutex
	// docsMu guards docs, the cache documents as this service last saw them
	// (store.go).
	docsMu sync.Mutex
	docs   map[string]*document
	// writesMu guards writes, how many times this service wrote each cache
	// key (Revision).
	writesMu  sync.Mutex
	writes    map[string]uint64
	now       func() time.Time
	validated map[string]bool
	epoch     uint64
	// active cancels the polls and probes under way at an identity reset.
	active map[uint64]context.CancelFunc
	nextID uint64
	// Updated, when set before use, is called after anything Cached reads
	// changes: a cache write, or live freshness. It may run with the
	// service's locks held, so it must not call the service.
	Updated func()
}

func (s *Service) updated() {
	if s.Updated != nil {
		s.Updated()
	}
}

// ResetIdentity cancels the old transactions and invalidates live discovery.
// Disk partitions remain private and are keyed by their original credential.
func (s *Service) ResetIdentity() {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	s.epoch++
	for id, cancel := range s.active {
		cancel()
		delete(s.active, id)
	}
	s.validated = map[string]bool{}
	s.updated()
}
func (s *Service) Epoch() uint64 {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	return s.epoch
}

// begin registers a transaction's cancel for ResetIdentity and returns the
// epoch it runs in.
func (s *Service) begin(cancel context.CancelFunc) (uint64, func()) {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	if s.active == nil {
		s.active = map[uint64]context.CancelFunc{}
	}
	s.nextID++
	id := s.nextID
	s.active[id] = cancel
	return s.epoch, func() {
		s.liveMu.Lock()
		defer s.liveMu.Unlock()
		delete(s.active, id)
	}
}

// lock serializes one repository's polls and cache writes.
func (s *Service) lock(repository github.Repository) func() {
	key := strings.ToLower(repository.String())
	s.mu.Lock()
	if s.locks == nil {
		s.locks = map[string]*sync.Mutex{}
	}
	m := s.locks[key]
	if m == nil {
		m = &sync.Mutex{}
		s.locks[key] = m
	}
	s.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// New uses app.sqlite in directory, or the shared application database when empty.
func New(client *github.Client, directory string) *Service {
	return &Service{client: client, directory: directory, now: time.Now, validated: map[string]bool{}}
}

// DefaultDirectory is the shared application database directory.
func DefaultDirectory() (string, error) {
	return appdb.Directory()
}

func (s *Service) path(identity, repository string) (string, error) {
	sum := sha256.Sum256([]byte(identity + "\x00" + strings.ToLower(repository)))
	return hex.EncodeToString(sum[:]), nil
}

func (s *Service) databasePath() (string, error) {
	if s.directory == "" {
		return appdb.Path()
	}
	return filepath.Join(s.directory, "app.sqlite"), nil
}

// Revision stamps what Cached reads of a repository without opening the
// cache: Cached reads the same snapshot at two equal revisions, except that
// its Stale turns true once NextPoll passes. It changes when the identity
// resets or the credential changes, when this service changes the
// repository's cache (store, including times it keeps unsaved), and when a
// poll validates it or fails. Only this service writes the cache, and only
// the daemon, which runs once, makes one on app.sqlite.
type Revision struct {
	Epoch     uint64
	Partition string
	Writes    uint64
	Validated bool
}

// Revision never makes a request and reads nothing from disk.
func (s *Service) Revision(ctx context.Context, repository github.Repository) (Revision, error) {
	epoch := s.Epoch()
	reader, err := s.client.REST(ctx, repository)
	if err != nil {
		return Revision{}, err
	}
	path, err := s.path(reader.CacheIdentity(), repository.String())
	if err != nil {
		return Revision{}, err
	}
	s.writesMu.Lock()
	writes := s.writes[path]
	s.writesMu.Unlock()
	return Revision{Epoch: epoch, Partition: path, Writes: writes, Validated: s.isValidated(path)}, nil
}

// Cached never makes a network request. Resolving the configured local token
// partitions results by credential, so changing accounts cannot reuse an inbox.
// Disk snapshots are always stale until a successful conditional poll.
func (s *Service) Cached(ctx context.Context, repository github.Repository) (Snapshot, error) {
	epoch := s.Epoch()
	reader, err := s.client.REST(ctx, repository)
	if err != nil {
		return Snapshot{}, err
	}
	path, err := s.path(reader.CacheIdentity(), repository.String())
	if err != nil {
		return Snapshot{}, err
	}
	cache, err := s.view(path)
	if err != nil {
		return Snapshot{}, err
	}
	// The view is shared, so the caller gets its own slices. Their elements'
	// pointers are still shared and must not be written through.
	snapshot := cache.Snapshot
	snapshot.PullRequests = slices.Clone(snapshot.PullRequests)
	snapshot.Observations = slices.Clone(snapshot.Observations)
	snapshot.Stale = snapshot.Stale || !s.isValidated(path) || !s.now().Before(snapshot.NextPoll)
	if epoch != s.Epoch() {
		return Snapshot{}, github.ErrIdentityChanged
	}
	snapshot.Epoch = epoch
	return snapshot, nil
}

type poll struct {
	reader *github.RESTReader
	cache  diskCache
	budget *ledger
	now    time.Time
	// minimum is the shortest interval the responses allow.
	minimum time.Duration
	// server is the polling interval GitHub asked for, if any.
	server time.Duration
	// moved: a pull request has a head the last poll did not see. unknown: a
	// non-draft one has a mergeability GitHub has not worked out yet. forgot:
	// one GitHub had worked out at the same head is unknown again.
	moved, unknown, forgot bool
	// carried is the earlier poll's pull request whose merge state each
	// unknown one keeps (carryMerge).
	carried map[int]PullRequest
	// rows are the open pull requests as this poll leaves them.
	rows map[int]gqlPR
	// legacy are an older format's pull requests a first sweep has not yet
	// replaced; nil once the cache is complete.
	legacy map[int]PullRequest
	// read are the pull requests GitHub described in this poll, and removed
	// those it reported closed, merged or gone.
	read, removed map[int]bool
	// readAt is each read pull request's update time, so an older copy of it
	// on a later page does not replace a newer one.
	readAt map[int]time.Time
	// windowCursor is where the cursor moves once the poll's window and
	// re-reads are read.
	windowCursor time.Time
	// size is how many pull requests a query asks for (shrinking); zero
	// until the first.
	size int
	// merged are the base branches a pull request merged into since the cursor.
	merged map[string]bool
	// open and newest are GitHub's count of open pull requests and the newest
	// one's number when the incremental read began; nil when it did not say.
	open, newest *int
	// viewer and permission are the latest response's; identified once one
	// arrived.
	viewer     actor
	permission string
	identified bool
}

var errRateLimit = errors.New("GitHub rate limit exhausted")

// statusError is a GitHub answer other than success or a rate limit.
type statusError struct{ status int }

func (e *statusError) Error() string { return fmt.Sprintf("GitHub discovery HTTP %d", e.status) }

// refused is GitHub declining to answer this token: a permission it may not
// read, or teams without read:org. It is an answer, kept like one.
func (e *statusError) refused() bool {
	return e.status == http.StatusUnauthorized || e.status == http.StatusForbidden || e.status == http.StatusNotFound
}

// gateway is GitHub's gateway answering instead of its API, usually because
// the query took too long. It may succeed if asked again.
func (e *statusError) gateway() bool {
	return e.status == http.StatusBadGateway || e.status == http.StatusServiceUnavailable || e.status == http.StatusGatewayTimeout
}

// failed turns a request that returned no response into the poll's error: a
// poll out of time, or one GitHub stopped answering (WithIdleTimeout), is a
// timeout.
func failed(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, github.ErrIdentityChanged):
		return err
	case errors.Is(context.Cause(ctx), context.DeadlineExceeded):
		return ErrTimeout
	case ctx.Err() != nil:
		return ctx.Err()
	}
	return err
}

func (p *poll) get(ctx context.Context, responses map[string]cachedResponse, path string, into any) error {
	if rest, _ := p.budget.budgets(); rateDeadline(rest).After(p.now) {
		return errRateLimit
	}
	old := responses[path]
	status, headers, body, err := p.reader.Read(ctx, path, old.ETag)
	if err != nil {
		return failed(ctx, err)
	}
	restartIdle(ctx)
	var remaining *int
	if n, err := strconv.Atoi(headers.Get("X-RateLimit-Remaining")); err == nil {
		remaining = &n
	}
	var reset time.Time
	if n, err := strconv.ParseInt(headers.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		reset = time.Unix(n, 0)
	}
	if seconds, err := strconv.Atoi(headers.Get("X-Poll-Interval")); err == nil && seconds > 0 {
		p.minimum = max(p.minimum, time.Duration(seconds)*time.Second)
		p.server = max(p.server, time.Duration(seconds)*time.Second)
	}
	limited := status == 429 || status == 403 && (headers.Get("X-RateLimit-Remaining") == "0" || headers.Get("Retry-After") != "")
	p.budget.update(func(rest, _ *RateLimit) {
		observe(rest, remaining, reset)
		if limited {
			rest.RetryAt = maxTime(rest.RetryAt, retryAt(p.now, headers))
		}
	})
	if limited {
		return errRateLimit
	}
	if status == 304 {
		if len(old.Body) == 0 {
			return errors.New("GitHub returned 304 without cached response")
		}
		body = old.Body
	} else if status != 200 {
		return &statusError{status}
	}
	if err := json.Unmarshal(body, into); err != nil {
		return errInvalid
	}
	if status == 200 {
		// Persist only inbox fields, never full PR bodies or signed URLs.
		minimal, err := json.Marshal(into)
		if err != nil {
			return errInvalid
		}
		responses[path] = cachedResponse{ETag: headers.Get("ETag"), Body: minimal}
	}
	return nil
}

// retryAt is when a rate-limited response says to ask again: its Retry-After,
// or a minute.
func retryAt(now time.Time, headers http.Header) time.Time {
	if seconds, err := strconv.Atoi(headers.Get("Retry-After")); err == nil && seconds >= 0 {
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if at, err := http.ParseTime(headers.Get("Retry-After")); err == nil {
		return at
	}
	return now.Add(time.Minute)
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

type actor struct {
	Login string `json:"login"`
	// Typename is GraphQL's __typename ("User", "Bot", ...); the REST shape has none.
	Typename string `json:"__typename,omitempty"`
}
type team struct {
	ID int64 `json:"id"`
}
type apiPR struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Draft  bool   `json:"draft"`
	User   actor  `json:"user"`
	Head   struct {
		SHA  string `json:"sha"`
		Ref  string `json:"ref"`
		Repo *struct {
			ID       int64  `json:"id"`
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref  string `json:"ref"`
		Repo struct {
			ID       int64  `json:"id"`
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
	RequestedReviewers []actor `json:"requested_reviewers"`
	RequestedTeams     []team  `json:"requested_teams"`
}

func collect[T any](ctx context.Context, p *poll, responses map[string]cachedResponse, path string) ([]T, error) {
	var result []T
	for page := 1; page <= 1000; page++ {
		var rows []T
		separator := "?"
		if strings.Contains(path, "?") {
			separator = "&"
		}
		if err := p.get(ctx, responses, path+separator+"per_page=100&page="+strconv.Itoa(page), &rows); err != nil {
			return nil, err
		}
		result = append(result, rows...)
		if len(rows) < 100 {
			return result, nil
		}
	}
	return nil, errors.New("GitHub discovery pagination exceeded limit")
}

// Poll refreshes the open pull requests and conditional REST metadata without
// waiting through a rate limit. Callers schedule NextPoll; unchanged results
// back off from 30s to 15m. Failure returns the cached snapshot, with what this
// poll did read merged into it, explicitly stale, and an error.
func (s *Service) Poll(ctx context.Context, repository github.Repository) (Snapshot, error) {
	return s.poll(ctx, repository, false)
}

// PollNow is Poll without waiting for NextPoll: something local (a push, an
// agent session ending) says the pull requests changed. It still honors the
// shared REST and GraphQL rate budgets.
func (s *Service) PollNow(ctx context.Context, repository github.Repository) (Snapshot, error) {
	return s.poll(ctx, repository, true)
}

// upgrade drops what an older cache format cannot vouch for. Its snapshot
// stays, so the inbox is still shown until the first full sweep.
func upgrade(cache diskCache) diskCache {
	if cache.Format >= cacheFormat {
		return cache
	}
	cache.Responses = nil
	cache.Pulls, cache.Complete, cache.Cursor, cache.Swept, cache.Sweep, cache.Authors = nil, false, time.Time{}, time.Time{}, nil, nil
	return cache
}

func (s *Service) poll(ctx context.Context, repository github.Repository, force bool) (Snapshot, error) {
	pauseIdle(ctx)
	defer s.lock(repository)()
	restartIdle(ctx)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	epoch, done := s.begin(cancel)
	defer done()
	reader, err := s.client.REST(ctx, repository)
	if err != nil {
		return Snapshot{}, err
	}
	path, err := s.path(reader.CacheIdentity(), repository.String())
	if err != nil {
		return Snapshot{}, err
	}
	cache, loadErr := s.load(path)
	if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
		return Snapshot{}, loadErr
	}
	cache.Snapshot.Epoch = epoch
	cache = upgrade(cache)
	cache.Format = cacheFormat
	budgetPath, err := s.path(reader.CacheIdentity(), "")
	if err != nil {
		return Snapshot{}, err
	}
	budgetPath += ".budget"
	budget, err := s.ledger(budgetPath)
	if err != nil {
		return Snapshot{}, err
	}
	now := s.now()
	p := poll{reader: reader, cache: cache, budget: budget, now: now, minimum: 30 * time.Second}
	if deadline := p.ResetIfExhausted(); deadline.After(now) {
		s.setValidated(path, false)
		snapshot := cache.Snapshot
		snapshot.Repository = repository.String()
		snapshot.Stale = true
		snapshot.Error = errRateLimit.Error()
		snapshot.Rate = p.visibleRate()
		snapshot.NextPoll = maxTime(snapshot.NextPoll, deadline)
		return snapshot, errRateLimit
	}
	if !force && now.Before(cache.Snapshot.NextPoll) {
		return cache.Snapshot, nil
	}
	if p.cache.Responses == nil {
		p.cache.Responses = map[string]cachedResponse{}
	}
	if p.cache.Authors == nil {
		p.cache.Authors = map[string]authorPermission{}
	}
	err = p.refresh(ctx, repository)
	if err != nil && ctx.Err() != nil && !errors.Is(err, errRateLimit) {
		err = failed(ctx, err)
	}
	snapshot, buildErr := p.build(ctx, repository, err == nil)
	if err == nil {
		err = buildErr
	}
	if err != nil {
		if !p.identified && len(p.removed) == 0 {
			snapshot = cache.Snapshot
		}
		snapshot.Repository = repository.String()
		snapshot.Stale = true
		snapshot.Error = err.Error()
		snapshot.Observations = cache.Snapshot.Observations
		snapshot.FetchedAt = cache.Snapshot.FetchedAt
		snapshot.Settling = false
		// The failed poll's snapshot is saved with a new head it read: the next
		// one would not see the head move, so the settling starts here.
		if p.moved || p.forgot {
			p.cache.Settling = max(p.cache.Settling, 1)
		}
		snapshot.Rate = p.visibleRate()
		snapshot.NextPoll = maxTime(now.Add(time.Minute), p.ResetIfExhausted())
	} else {
		interval := 30 * time.Second
		same := unchanged(cache.Snapshot.PullRequests, snapshot.PullRequests)
		switch {
		case !slices.ContainsFunc(slices.Collect(maps.Values(p.rows)), checksRunning):
			p.cache.ChecksSince = time.Time{}
		case !same || p.cache.ChecksSince.IsZero():
			p.cache.ChecksSince = now
		}
		held := !p.cache.ChecksSince.IsZero() && now.Sub(p.cache.ChecksSince) < checksHold
		if same && !held {
			interval = min(max(cache.Interval, 30*time.Second)*2, 15*time.Minute)
		}
		interval = max(interval, p.minimum)
		switch {
		case p.moved || p.forgot:
			p.cache.Settling = 1
		case p.unknown && p.cache.Settling > 0 && p.cache.Settling <= maxSettles:
			p.cache.Settling++
		default:
			p.cache.Settling = 0
		}
		if p.cache.Settling > 0 {
			// Saved as the interval, so the backoff starts again from it.
			snapshot.Settling = true
			interval = max(settleInterval, p.server)
		}
		snapshot.Observations = cache.Snapshot.Observations
		snapshot.FetchedAt = now
		next := now.Add(interval)
		// The backoff does not put off the next full sweep, though the
		// server's poll interval still does.
		if !p.cache.Swept.IsZero() {
			if due := maxTime(p.cache.Swept.Add(sweepInterval), now.Add(p.minimum)); due.Before(next) {
				next = due
			}
		}
		snapshot.NextPoll = maxTime(next, p.ResetIfExhausted())
		snapshot.Rate = p.visibleRate()
		p.cache.Interval = interval
		p.cache.Fingerprint = p.fingerprint()
		p.cache.Carried = nil
		for number := range p.carried {
			if p.cache.Carried == nil {
				p.cache.Carried = map[int]bool{}
			}
			p.cache.Carried[number] = true
		}
	}
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	// A reset meanwhile, or a credential that changed under the requests:
	// nothing read under the old identity is kept.
	if s.epoch != epoch || errors.Is(err, github.ErrIdentityChanged) {
		return Snapshot{}, github.ErrIdentityChanged
	}
	s.validated[path] = err == nil
	s.updated()
	snapshot.Epoch = epoch
	p.cache.Snapshot = snapshot
	if saveErr := s.save(budgetPath, budget.record()); saveErr != nil {
		return snapshot, fmt.Errorf("save discovery rate budget: %w", saveErr)
	}
	if saveErr := s.store(path, p.cache, true); saveErr != nil {
		return snapshot, fmt.Errorf("save discovery cache: %w", saveErr)
	}
	return snapshot, err
}

func (p *poll) ResetIfExhausted() time.Time {
	rest, graph := p.budget.budgets()
	resume := maxTime(rest.RetryAt, graph.RetryAt)
	if graph.Remaining != nil && *graph.Remaining == 0 {
		resume = maxTime(resume, graph.Reset)
	}
	if rest.Remaining != nil && *rest.Remaining == 0 {
		resume = maxTime(resume, rest.Reset)
	}
	return resume
}

// unchanged is whether a poll found the pull requests as the last one left
// them. Update times are left out: a comment alone does not reset the backoff.
func unchanged(before, after []PullRequest) bool {
	strip := func(prs []PullRequest) string {
		stable := slices.Clone(prs)
		for i := range stable {
			stable[i].UpdatedAt = time.Time{}
		}
		raw, _ := json.Marshal(stable)
		return string(raw)
	}
	return len(before) == len(after) && strip(before) == strip(after)
}

// fingerprint is the open pull requests' as this poll leaves them, which the
// probe compares with GitHub's.
func (p *poll) fingerprint() fingerprint {
	f := fingerprint{Open: len(p.rows)}
	for number, row := range p.rows {
		f.Newest = max(f.Newest, number)
		f.Updated = maxTime(f.Updated, row.UpdatedAt)
	}
	return f
}

// build makes the snapshot from the rows: the viewer's flags, the account's
// teams and the authors' permissions, which fresh asks GitHub for when they
// are due. A poll that failed builds from what it has, without asking.
func (p *poll) build(ctx context.Context, repository github.Repository, fresh bool) (Snapshot, error) {
	snapshot := Snapshot{Repository: repository.String(), PullRequests: []PullRequest{}, Viewer: p.cache.Snapshot.Viewer}
	if p.identified {
		snapshot.Viewer = p.viewer.Login
	}
	if snapshot.Viewer == "" {
		return snapshot, errors.New("GitHub token has no user identity")
	}
	var buildErr error
	var membership map[int64]bool
	var known bool
	if fresh {
		membership, known, buildErr = p.membership(ctx)
	} else {
		p.budget.mu.Lock()
		teams := p.budget.teams
		p.budget.mu.Unlock()
		membership, known = map[int64]bool{}, teams.Known
		for _, id := range teams.IDs {
			membership[id] = true
		}
	}
	if fresh && buildErr == nil {
		buildErr = p.permissions(ctx, repository)
	}
	var push *bool
	permission := p.permission
	if !p.identified {
		permission = ""
	}
	switch permission {
	case "ADMIN", "MAINTAIN", "WRITE":
		push = new(true)
	case "READ", "TRIAGE":
		push = new(false)
	}
	numbers := slices.Sorted(maps.Keys(p.rows))
	known0 := map[int]PullRequest{}
	for _, pr := range p.cache.Snapshot.PullRequests {
		known0[pr.Number] = pr
	}
	compare := !p.cache.Snapshot.FetchedAt.IsZero()
	items := map[int]PullRequest{}
	for number, item := range p.legacy {
		if !p.removed[number] {
			items[number] = item
		}
	}
	for _, number := range numbers {
		row := p.rows[number]
		pr := row.legacy()
		old := known0[number]
		unknown := !row.IsDraft && (row.Mergeable == "UNKNOWN" || row.MergeStateStatus == "UNKNOWN")
		if compare {
			p.moved = p.moved || old.HeadSHA != row.HeadRefOid
			p.unknown = p.unknown || unknown
		}
		item := PullRequest{Repository: repository.String(), Number: pr.Number, Title: pr.Title, URL: "https://" + repository.Host + "/" + repository.Owner + "/" + repository.Name + "/pull/" + strconv.Itoa(pr.Number), HeadSHA: pr.Head.SHA, HeadRef: pr.Head.Ref, BaseRef: pr.Base.Ref, Additions: row.Additions, Deletions: row.Deletions, ChangedFiles: row.ChangedFiles, Author: pr.User.Login, AuthorBot: pr.User.Typename == "Bot", State: "OPEN", Draft: pr.Draft, Fork: pr.Head.Repo == nil || pr.Base.Repo.ID == 0 || pr.Head.Repo.ID != pr.Base.Repo.ID || !strings.EqualFold(pr.Head.Repo.FullName, pr.Base.Repo.FullName), AuthoredByMe: strings.EqualFold(pr.User.Login, snapshot.Viewer), TeamMembershipKnown: known}
		if pr.Head.Repo != nil {
			head, parseErr := github.ParseRepository(repository.Host + "/" + pr.Head.Repo.FullName)
			if parseErr == nil {
				item.HeadRepository = strings.ToLower(head.String())
			}
		}
		item.Assigned = len(row.Assignees.Nodes) > 0
		item.ReviewRequested = len(pr.RequestedReviewers) > 0 || len(pr.RequestedTeams) > 0
		for _, user := range row.Assignees.Nodes {
			if strings.EqualFold(user.Login, snapshot.Viewer) {
				item.AssignedToMe = true
			}
		}
		for _, user := range pr.RequestedReviewers {
			if strings.EqualFold(user.Login, snapshot.Viewer) {
				item.ReviewRequestedFromMe = true
			}
		}
		for _, team := range pr.RequestedTeams {
			if membership[team.ID] {
				item.ReviewRequestedFromMe = true
			}
		}
		if answer, ok := p.cache.Authors[strings.ToLower(pr.User.Login)]; ok {
			item.AuthorHasWrite = answer.Push
		} else if legacy, ok := p.legacy[number]; ok && strings.EqualFold(legacy.Author, pr.User.Login) {
			// Not asked yet since an upgrade: the older snapshot's answer.
			item.AuthorHasWrite = legacy.AuthorHasWrite
		}
		item.ViewerCanPush = push
		if !p.identified {
			item.ViewerCanPush = old.ViewerCanPush
		}
		item.Review, item.Checks, item.Merge, item.Unresolved, item.UnresolvedCapped = row.states()
		item.ReviewRequired = new(row.ReviewDecision != nil && *row.ReviewDecision == "REVIEW_REQUIRED")
		item.Behind = row.behind()
		item.ReviewRequired = new(row.ReviewDecision != nil && *row.ReviewDecision == "REVIEW_REQUIRED")
		if compare && carryMerge(old, row, unknown) {
			if p.carried == nil {
				p.carried = map[int]PullRequest{}
			}
			p.carried[number] = old
			p.forgot = p.forgot || !p.cache.Carried[number]
			item.Merge, item.Behind = old.Merge, old.Behind
		}
		item.UpdatedAt = row.UpdatedAt
		items[number] = item
	}
	for _, number := range slices.Sorted(maps.Keys(items)) {
		snapshot.PullRequests = append(snapshot.PullRequests, items[number])
	}
	p.cache.Pulls = make([]gqlPR, 0, len(numbers))
	for _, number := range numbers {
		p.cache.Pulls = append(p.cache.Pulls, p.rows[number])
	}
	return snapshot, buildErr
}

// permissions asks GitHub for the write permission of each author of an open
// pull request whose answer is missing or older than permissionTTL. A 403 or
// 404 is an answer too; a failed request keeps the earlier answer and is
// asked again by the next poll. Authors with no open pull request left are
// forgotten once the cache is complete.
func (p *poll) permissions(ctx context.Context, repository github.Repository) error {
	logins := map[string]string{}
	for _, row := range p.rows {
		if row.Author.Login != "" {
			logins[strings.ToLower(row.Author.Login)] = row.Author.Login
		}
	}
	prefix := "/repos/" + url.PathEscape(repository.Owner) + "/" + url.PathEscape(repository.Name)
	for _, key := range slices.Sorted(maps.Keys(logins)) {
		entry, ok := p.cache.Authors[key]
		if ok && !p.now.Before(entry.Read) && p.now.Before(entry.Read.Add(permissionTTL)) {
			continue
		}
		var response struct {
			Permission string `json:"permission"`
			User       struct {
				Permissions struct {
					Push *bool `json:"push"`
				} `json:"permissions"`
			} `json:"user"`
		}
		err := p.get(ctx, p.cache.Responses, permissionPath(prefix, logins[key]), &response)
		var status *statusError
		switch {
		case errors.Is(err, errRateLimit):
			return err
		case err == nil:
			entry = authorPermission{Read: p.now}
			switch response.Permission {
			case "admin", "write", "maintain":
				entry.Push = new(true)
			case "read", "triage", "none":
				entry.Push = new(false)
			default:
				entry.Push = response.User.Permissions.Push
			}
		case errors.As(err, &status) && status.refused():
			entry = authorPermission{Read: p.now}
		case ctx.Err() != nil:
			return failed(ctx, err)
		default:
			continue
		}
		p.cache.Authors[key] = entry
	}
	if p.cache.Complete {
		for key := range p.cache.Authors {
			if _, ok := logins[key]; !ok {
				delete(p.cache.Authors, key)
			}
		}
		for path := range p.cache.Responses {
			if strings.HasPrefix(path, prefix+"/collaborators/") {
				login, _ := url.PathUnescape(strings.TrimSuffix(strings.TrimPrefix(path, prefix+"/collaborators/"), "/permission"))
				if _, ok := logins[strings.ToLower(login)]; !ok {
					delete(p.cache.Responses, path)
				}
			}
		}
	}
	return nil
}

func permissionPath(prefix, login string) string {
	return prefix + "/collaborators/" + url.PathEscape(login) + "/permission"
}

// carryMerge is whether a pull request GitHub reports an unknown mergeability
// for keeps the merge state of the earlier poll that read the same head:
// after a merge into its base GitHub works the answer out again, and until it
// does the pull request stays where it was rather than looking like one that
// waits on reviewers. A new head, or one that was a draft, has nothing to keep,
// and a response one of whose fields still settles the merge state (CONFLICTING,
// or BEHIND) keeps its own.
func carryMerge(old PullRequest, row gqlPR, unknown bool) bool {
	if !unknown || old.HeadSHA != row.HeadRefOid || old.Merge == "" || old.Merge == "draft" {
		return false
	}
	_, _, merge, _, _ := row.states()
	return merge == ""
}

// Observe retains fresh free metadata for open and saved historical PRs. A
// separate service instance still treats the disk cache as stale on startup.
// An observation older than the one kept is dropped: a read under way when
// the app merged the pull request (Merged) does not undo it.
func (s *Service) Observe(ctx context.Context, repository github.Repository, value Observation) error {
	defer s.lock(repository)()
	reader, err := s.client.REST(ctx, repository)
	if err != nil {
		return err
	}
	if value.Identity != "" && value.Identity != reader.CacheIdentity() {
		return github.ErrIdentityChanged
	}
	path, err := s.path(reader.CacheIdentity(), repository.String())
	if err != nil {
		return err
	}
	cache, err := s.load(path)
	if err != nil {
		return err
	}
	for i, old := range cache.Snapshot.Observations {
		if old.Number == value.Number {
			if old.FetchedAt.After(value.FetchedAt) {
				return nil
			}
			cache.Snapshot.Observations[i] = value
			return s.save(path, cache)
		}
	}
	cache.Snapshot.Observations = append(cache.Snapshot.Observations, value)
	return s.save(path, cache)
}

// Merged records that the app just merged the pull request at head: GitHub
// accepted the merge, so it leaves the open pull requests and is observed
// merged without another read. Its title and draft state are the cached ones.
func (s *Service) Merged(ctx context.Context, repository github.Repository, number int, head string) error {
	defer s.lock(repository)()
	reader, err := s.client.REST(ctx, repository)
	if err != nil {
		return err
	}
	path, err := s.path(reader.CacheIdentity(), repository.String())
	if err != nil {
		return err
	}
	cache, err := s.load(path)
	if err != nil {
		return err
	}
	observation := Observation{Number: number, HeadSHA: head, State: "MERGED", FetchedAt: s.now()}
	for _, pr := range cache.Snapshot.PullRequests {
		if pr.Number == number {
			observation.Title = pr.Title
			observation.Checks = pr.Checks
		}
	}
	cache.Snapshot.PullRequests = slices.DeleteFunc(cache.Snapshot.PullRequests, func(pr PullRequest) bool { return pr.Number == number })
	// The rows the next poll builds from leave it too, so a poll that fails
	// before reading the merge does not list it open again.
	cache.Pulls = slices.DeleteFunc(cache.Pulls, func(row gqlPR) bool { return row.Number == number })
	for i, old := range cache.Snapshot.Observations {
		if old.Number == number {
			observation.Title = cmp.Or(observation.Title, old.Title)
			if old.HeadSHA == head {
				observation.Checks = cmp.Or(observation.Checks, old.Checks)
			}
			cache.Snapshot.Observations[i] = observation
			return s.save(path, cache)
		}
	}
	cache.Snapshot.Observations = append(cache.Snapshot.Observations, observation)
	return s.save(path, cache)
}

// Unresolved records the count of an open pull request's unresolved review
// threads that the app just read from GitHub, ahead of the next poll: GitHub
// reports no update to a pull request when a thread is resolved, so a probe
// cannot see it. It reports whether the count changed.
func (s *Service) Unresolved(ctx context.Context, repository github.Repository, number, count int) (bool, error) {
	defer s.lock(repository)()
	reader, err := s.client.REST(ctx, repository)
	if err != nil {
		return false, err
	}
	path, err := s.path(reader.CacheIdentity(), repository.String())
	if err != nil {
		return false, err
	}
	cache, err := s.load(path)
	if err != nil {
		return false, err
	}
	for i, pr := range cache.Snapshot.PullRequests {
		if pr.Number != number || pr.UnresolvedCapped || pr.Unresolved == nil || *pr.Unresolved == count {
			continue
		}
		cache.Snapshot.PullRequests[i].Unresolved = &count
		// A resolution moves no update time, so an incremental poll would not
		// read the pull request again: its row takes the count too.
		for j, row := range cache.Pulls {
			if row.Number == number && row.ReviewThreads != nil && !row.ReviewThreads.PageInfo.HasNextPage {
				cache.Pulls[j].ReviewThreads.Nodes = make([]struct{ IsResolved bool }, count)
			}
		}
		return true, s.save(path, cache)
	}
	return false, nil
}

func (s *Service) isValidated(path string) bool {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	return s.validated[path]
}
func (s *Service) setValidated(path string, value bool) {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	s.validated[path] = value
	s.updated()
}

// visibleRate exposes the active limiting budget without combining REST request
// allowances with GraphQL points, which GitHub accounts independently.
func (p *poll) visibleRate() RateLimit {
	rest, graph := p.budget.budgets()
	if rateDeadline(rest).After(p.now) {
		return rest
	}
	return graph
}
func rateDeadline(rate RateLimit) time.Time {
	deadline := rate.RetryAt
	if rate.Remaining != nil && *rate.Remaining == 0 {
		deadline = maxTime(deadline, rate.Reset)
	}
	return deadline
}
