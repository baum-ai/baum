// Package discovery maintains a private, conditional GitHub inbox cache. It
// performs no git fetch, generation or write to GitHub.
package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/github"
)

type PullRequest struct {
	Repository            string `json:"repository"`
	Number                int    `json:"number"`
	HeadSHA               string `json:"head_sha"`
	HeadRef               string `json:"head_ref"`
	Title                 string `json:"title"`
	URL                   string `json:"url"`
	Author                string `json:"author"`
	State                 string `json:"state"`
	Draft                 bool   `json:"draft"`
	Fork                  bool   `json:"fork"`
	AuthoredByMe          bool   `json:"authored_by_me"`
	ReviewRequestedFromMe bool   `json:"review_requested_from_me"`
	// Nil means the token could not establish repository write permission.
	AuthorHasWrite *bool `json:"author_has_write"`
	// False means team membership was unavailable; false review-request flags
	// must not be interpreted as proof that no team requested this viewer.
	TeamMembershipKnown bool `json:"team_membership_known"`
}

// Observation contains only safe current GitHub metadata, never description
// bodies, signed image URLs or session contents.
type Observation struct {
	Number    int       `json:"number"`
	HeadSHA   string    `json:"head_sha"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	Draft     bool      `json:"draft"`
	FetchedAt time.Time `json:"fetched_at"`
	Checks    string    `json:"checks"`
}

type RateLimit struct {
	Remaining *int      `json:"remaining"`
	Reset     time.Time `json:"reset"`
	RetryAt   time.Time `json:"retry_at"`
}

type Snapshot struct {
	Repository   string        `json:"repository"`
	Observations []Observation `json:"observations,omitempty"`
	Viewer       string        `json:"viewer"`
	PullRequests []PullRequest `json:"pull_requests"`
	Stale        bool          `json:"stale"`
	FetchedAt    time.Time     `json:"fetched_at"`
	NextPoll     time.Time     `json:"next_poll"`
	Rate         RateLimit     `json:"rate"`
	Error        string        `json:"error,omitempty"`
}

type cachedResponse struct {
	ETag string
	Body json.RawMessage
}
type diskCache struct {
	// Format is cacheFormat when the cache was written; a cache of an older
	// format drops its conditional responses, so they are read in full once.
	Format    int `json:",omitempty"`
	Snapshot  Snapshot
	Responses map[string]cachedResponse
	Interval  time.Duration
}

// cacheFormat changes when the minimal responses keep more fields: 1 added
// the pull requests' head branch. A 304 answer reuses the stored body, which
// would otherwise never gain the new fields.
const cacheFormat = 1

// Budget files are shared by service instances, alongside repository caches.
// Serialize in-process polls so a newly exhausted budget is visible before the
// next repository starts. Atomic disk replacement also preserves it on restart.
var budgetMu sync.Mutex

type Service struct {
	client    *github.Client
	directory string
	mu        sync.Mutex
	liveMu    sync.Mutex
	now       func() time.Time
	validated map[string]bool
}

// New uses directory as a dedicated private cache directory. An empty path
// selects $XDG_CACHE_HOME/baum/discovery (or the OS user cache directory).
func New(client *github.Client, directory string) *Service {
	return &Service{client: client, directory: directory, now: time.Now, validated: map[string]bool{}}
}

// DefaultDirectory is the cache directory used when New is given none: baum/discovery
// under the OS user cache directory ($XDG_CACHE_HOME on Linux).
func DefaultDirectory() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "baum", "discovery"), nil
}

func (s *Service) path(identity, repository string) (string, error) {
	directory := s.directory
	if directory == "" {
		var err error
		if directory, err = DefaultDirectory(); err != nil {
			return "", err
		}
	}
	sum := sha256.Sum256([]byte(identity + "\x00" + strings.ToLower(repository)))
	return filepath.Join(directory, hex.EncodeToString(sum[:])+".json"), nil
}

func load(path string) (diskCache, error) {
	file, err := os.Open(path)
	if err != nil {
		return diskCache{}, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return diskCache{}, err
	}
	if stat.Size() > 32<<20 {
		return diskCache{}, errors.New("discovery cache too large")
	}
	var cache diskCache
	if err := json.NewDecoder(file).Decode(&cache); err != nil {
		return diskCache{}, errors.New("invalid discovery cache")
	}
	return cache, nil
}

func save(path string, cache diskCache) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	if len(data) > 32<<20 {
		return errors.New("discovery cache too large")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".discovery-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

// Cached never makes a network request. Resolving the configured local token
// partitions results by credential, so changing accounts cannot reuse an inbox.
// Disk snapshots are always stale until a successful conditional poll.
func (s *Service) Cached(ctx context.Context, repository github.Repository) (Snapshot, error) {
	reader, err := s.client.REST(ctx, repository)
	if err != nil {
		return Snapshot{}, err
	}
	path, err := s.path(reader.CacheIdentity(), repository.String())
	if err != nil {
		return Snapshot{}, err
	}
	cache, err := load(path)
	if err != nil {
		return Snapshot{}, err
	}
	cache.Snapshot.Stale = cache.Snapshot.Stale || !s.isValidated(path) || !s.now().Before(cache.Snapshot.NextPoll)
	return cache.Snapshot, nil
}

type poll struct {
	reader    *github.RESTReader
	cache     diskCache
	rate      RateLimit
	now       time.Time
	unchanged bool
	minimum   time.Duration
}

var errRateLimit = errors.New("GitHub rate limit exhausted")

func (p *poll) get(ctx context.Context, path string, into any) error {
	if p.ResetIfExhausted().After(p.now) {
		return errRateLimit
	}
	old := p.cache.Responses[path]
	status, headers, body, err := p.reader.Read(ctx, path, old.ETag)
	if err != nil {
		return err
	}
	if remaining, err := strconv.Atoi(headers.Get("X-RateLimit-Remaining")); err == nil {
		p.rate.Remaining = &remaining
	}
	if reset, err := strconv.ParseInt(headers.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		p.rate.Reset = time.Unix(reset, 0)
	}
	if seconds, err := strconv.Atoi(headers.Get("X-Poll-Interval")); err == nil && seconds > 0 {
		p.minimum = max(p.minimum, time.Duration(seconds)*time.Second)
	}
	if status == 429 || status == 403 && (headers.Get("X-RateLimit-Remaining") == "0" || headers.Get("Retry-After") != "") {
		resume := p.now.Add(time.Minute)
		if seconds, err := strconv.Atoi(headers.Get("Retry-After")); err == nil && seconds >= 0 {
			resume = p.now.Add(time.Duration(seconds) * time.Second)
		} else if parsed, err := http.ParseTime(headers.Get("Retry-After")); err == nil {
			resume = parsed
		}
		p.rate.RetryAt = resume
		return errRateLimit
	}
	if status == 304 {
		if len(old.Body) == 0 {
			return errors.New("GitHub returned 304 without cached response")
		}
		body = old.Body
	} else if status != 200 {
		return fmt.Errorf("GitHub discovery HTTP %d", status)
	}
	if err := json.Unmarshal(body, into); err != nil {
		return errors.New("invalid GitHub discovery response")
	}
	if status == 200 {
		// Persist only inbox fields, never full PR bodies or signed URLs.
		minimal, err := json.Marshal(into)
		if err != nil {
			return errors.New("invalid GitHub discovery response")
		}
		if string(minimal) != string(old.Body) {
			p.unchanged = false
		}
		p.cache.Responses[path] = cachedResponse{ETag: headers.Get("ETag"), Body: minimal}
	}
	return nil
}
func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

type actor struct {
	Login string `json:"login"`
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
		Repo struct {
			ID       int64  `json:"id"`
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
	RequestedReviewers []actor `json:"requested_reviewers"`
	RequestedTeams     []team  `json:"requested_teams"`
}

func collect[T any](ctx context.Context, p *poll, path string) ([]T, error) {
	var result []T
	for page := 1; page <= 1000; page++ {
		var rows []T
		separator := "?"
		if strings.Contains(path, "?") {
			separator = "&"
		}
		if err := p.get(ctx, path+separator+"per_page=100&page="+strconv.Itoa(page), &rows); err != nil {
			return nil, err
		}
		result = append(result, rows...)
		if len(rows) < 100 {
			return result, nil
		}
	}
	return nil, errors.New("GitHub discovery pagination exceeded limit")
}

// Poll refreshes all pages conditionally and never waits through a rate limit.
// Callers schedule NextPoll; unchanged results back off from 30s to 15m.
// Failure returns the last complete snapshot, explicitly stale, and an error.
func (s *Service) Poll(ctx context.Context, repository github.Repository) (Snapshot, error) {
	return s.poll(ctx, repository, false)
}

// PollNow is Poll without waiting for NextPoll: something local (a push, an
// agent session ending) says the pull requests changed. It still honors the
// shared rate budget and its conditional requests, so an unchanged inbox
// costs only 304 answers.
func (s *Service) PollNow(ctx context.Context, repository github.Repository) (Snapshot, error) {
	return s.poll(ctx, repository, true)
}

func (s *Service) poll(ctx context.Context, repository github.Repository, force bool) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	budgetMu.Lock()
	defer budgetMu.Unlock()
	reader, err := s.client.REST(ctx, repository)
	if err != nil {
		return Snapshot{}, err
	}
	path, err := s.path(reader.CacheIdentity(), repository.String())
	if err != nil {
		return Snapshot{}, err
	}
	cache, loadErr := load(path)
	if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
		return Snapshot{}, loadErr
	}
	if cache.Format < cacheFormat {
		cache.Responses = nil
	}
	cache.Format = cacheFormat
	budgetPath, err := s.path(reader.CacheIdentity(), "")
	if err != nil {
		return Snapshot{}, err
	}
	budgetPath += ".budget"
	budget, budgetErr := load(budgetPath)
	if budgetErr != nil && !errors.Is(budgetErr, os.ErrNotExist) {
		return Snapshot{}, budgetErr
	}
	now := s.now()
	shared := poll{rate: budget.Snapshot.Rate}
	if deadline := shared.ResetIfExhausted(); deadline.After(now) {
		s.setValidated(path, false)
		snapshot := cache.Snapshot
		snapshot.Repository = repository.String()
		snapshot.Stale = true
		snapshot.Error = errRateLimit.Error()
		snapshot.Rate = shared.rate
		snapshot.NextPoll = maxTime(snapshot.NextPoll, deadline)
		return snapshot, errRateLimit
	}
	if !force && now.Before(cache.Snapshot.NextPoll) {
		return cache.Snapshot, nil
	}
	if cache.Responses == nil {
		cache.Responses = map[string]cachedResponse{}
	}
	p := poll{reader: reader, cache: cache, rate: shared.rate, now: now, unchanged: true, minimum: 30 * time.Second}
	snapshot, err := p.discover(ctx, repository)
	if err != nil {
		snapshot = cache.Snapshot
		snapshot.Repository = repository.String()
		snapshot.Stale = true
		snapshot.Error = err.Error()
		snapshot.Rate = p.rate
		snapshot.NextPoll = maxTime(now.Add(time.Minute), p.ResetIfExhausted())
	} else {
		interval := 30 * time.Second
		if p.unchanged {
			interval = min(max(cache.Interval, 30*time.Second)*2, 15*time.Minute)
		}
		interval = max(interval, p.minimum)
		snapshot.Observations = cache.Snapshot.Observations
		snapshot.FetchedAt = now
		snapshot.NextPoll = maxTime(now.Add(interval), p.ResetIfExhausted())
		snapshot.Rate = p.rate
		p.cache.Interval = interval
	}
	s.setValidated(path, err == nil)
	p.cache.Snapshot = snapshot
	if saveErr := save(budgetPath, diskCache{Snapshot: Snapshot{Rate: p.rate}}); saveErr != nil {
		return snapshot, fmt.Errorf("save discovery rate budget: %w", saveErr)
	}
	if saveErr := save(path, p.cache); saveErr != nil {
		return snapshot, fmt.Errorf("save discovery cache: %w", saveErr)
	}
	return snapshot, err
}
func (p *poll) ResetIfExhausted() time.Time {
	resume := p.rate.RetryAt
	if p.rate.Remaining != nil && *p.rate.Remaining == 0 {
		resume = maxTime(resume, p.rate.Reset)
	}
	return resume
}

func (p *poll) discover(ctx context.Context, repository github.Repository) (Snapshot, error) {
	snapshot := Snapshot{Repository: repository.String(), PullRequests: []PullRequest{}}
	var viewer actor
	if err := p.get(ctx, "/user", &viewer); err != nil {
		return snapshot, err
	}
	if viewer.Login == "" {
		return snapshot, errors.New("GitHub token has no user identity")
	}
	snapshot.Viewer = viewer.Login
	prefix := "/repos/" + url.PathEscape(repository.Owner) + "/" + url.PathEscape(repository.Name)
	prs, err := collect[apiPR](ctx, p, prefix+"/pulls?state=open")
	if err != nil {
		return snapshot, err
	}
	teams, teamErr := collect[team](ctx, p, "/user/teams")
	membership := map[int64]bool{}
	for _, team := range teams {
		membership[team.ID] = true
	}
	permissions := map[string]*bool{}
	for _, pr := range prs {
		if pr.Number <= 0 || pr.Head.SHA == "" {
			return snapshot, errors.New("incomplete GitHub pull request")
		}
		item := PullRequest{Repository: repository.String(), Number: pr.Number, Title: pr.Title, URL: "https://" + repository.Host + "/" + repository.Owner + "/" + repository.Name + "/pull/" + strconv.Itoa(pr.Number), HeadSHA: pr.Head.SHA, HeadRef: pr.Head.Ref, Author: pr.User.Login, State: "OPEN", Draft: pr.Draft, Fork: pr.Head.Repo == nil || pr.Base.Repo.ID == 0 || pr.Head.Repo.ID != pr.Base.Repo.ID || !strings.EqualFold(pr.Head.Repo.FullName, pr.Base.Repo.FullName), AuthoredByMe: strings.EqualFold(pr.User.Login, viewer.Login), TeamMembershipKnown: teamErr == nil}
		for _, user := range pr.RequestedReviewers {
			if strings.EqualFold(user.Login, viewer.Login) {
				item.ReviewRequestedFromMe = true
			}
		}
		for _, team := range pr.RequestedTeams {
			if membership[team.ID] {
				item.ReviewRequestedFromMe = true
			}
		}
		key := strings.ToLower(pr.User.Login)
		permission, seen := permissions[key]
		if !seen && pr.User.Login != "" {
			var response struct {
				Permission string `json:"permission"`
				User       struct {
					Permissions struct {
						Push *bool `json:"push"`
					} `json:"permissions"`
				} `json:"user"`
			}
			if err := p.get(ctx, prefix+"/collaborators/"+url.PathEscape(pr.User.Login)+"/permission", &response); err == nil {
				switch response.Permission {
				case "admin", "write", "maintain":
					value := true
					permission = &value
				case "read", "triage", "none":
					value := false
					permission = &value
				default:
					permission = response.User.Permissions.Push
				}
			}
			permissions[key] = permission
		}
		item.AuthorHasWrite = permission
		snapshot.PullRequests = append(snapshot.PullRequests, item)
	}
	return snapshot, nil
}

// Observe retains fresh free metadata for open and saved historical PRs. A
// separate service instance still treats the disk cache as stale on startup.
func (s *Service) Observe(ctx context.Context, repository github.Repository, value Observation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	reader, err := s.client.REST(ctx, repository)
	if err != nil {
		return err
	}
	path, err := s.path(reader.CacheIdentity(), repository.String())
	if err != nil {
		return err
	}
	cache, err := load(path)
	if err != nil {
		return err
	}
	for i, old := range cache.Snapshot.Observations {
		if old.Number == value.Number {
			cache.Snapshot.Observations[i] = value
			return save(path, cache)
		}
	}
	cache.Snapshot.Observations = append(cache.Snapshot.Observations, value)
	return save(path, cache)
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
}
