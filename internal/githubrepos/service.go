// Package githubrepos lists the signed-in account's GitHub repositories for
// picking and clones a picked repository with the same sign-in. See README.md.
package githubrepos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/githubauth"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/repos"
)

const (
	// CacheTTL is how long a listing is reused; ?refresh=1 skips it.
	CacheTTL = 5 * time.Minute
	// listTimeout bounds one listing, which several requests share.
	listTimeout = 2 * time.Minute
)

var errRegistry = errors.New("repository registry")

// Lister reads the account's repositories from GitHub.
type Lister interface {
	ViewerRepositories(ctx context.Context, host string, first int, cursor *string) (github.ViewerRepositoryList, error)
	SearchRepositories(ctx context.Context, host, query string, scope github.SearchScope, cursor *string) (github.ViewerRepositoryList, error)
}

type repositoryLookup struct {
	list    github.ViewerRepositoryList
	at      time.Time
	running bool
	err     error
	cancel  context.CancelFunc
	// scope is what a search started with; its later pages keep it.
	scope github.SearchScope
}

// Registry is the part of the repository registry the service uses.
type Registry interface {
	Repositories(context.Context) ([]repos.Repository, error)
	CloneRows(context.Context) ([]repos.Clone, error)
	Pick(ctx context.Context, repository string, settings *repos.Settings) (repos.Repository, error)
	AddFor(ctx context.Context, path, repository string, settings *repos.Settings) (repos.Clone, error)
}

// Service serves the listing and the clone API. It has no authentication: the
// daemon mounts Handler behind its authenticated host and origin checks,
// because it returns and writes local paths.
type Service struct {
	Lister   Lister
	Registry Registry
	// Token returns the GitHub token for github.com; an error wrapping
	// config.ErrNoGitHubToken means signed out, and clones are anonymous.
	Token func(ctx context.Context) (string, error)
	// Clone runs the clone; nil uses gitlocal.Clone.
	Clone func(ctx context.Context, opts gitlocal.CloneOptions) error
	// Organizations reports the organizations the person belongs to that this
	// sign-in cannot reach (githubauth.Manager.Organizations); nil reports none.
	Organizations func(ctx context.Context, refresh bool) (githubauth.OrganizationAccess, error)
	// Health reports the runtime health of the GitHub sign-in after a failed
	// request (config.GitHubAuth.Health); nil means healthy.
	Health func() error
	// Home is the home directory used for clone targets (including top-level
	// code-folder symlinks to another disk).
	Home string
	// Context ends running clones when the daemon stops; nil means never.
	Context context.Context
	// Changed tells the app the registry changed.
	Changed func()
	Now     func() time.Time

	mu       sync.Mutex
	cache    *github.ViewerRepositoryList
	cached   time.Time
	topMore  *repositoryLookup
	searches map[string]*repositoryLookup
	flight   singleflight.Group
	clones   map[string]*cloneState
	running  sync.WaitGroup
	// gen counts account changes; a listing started under an older one is not cached.
	gen      uint64
	stopping bool
	// reserved are the folders of running clones, case-folded.
	reserved map[string]string
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) ctx() context.Context {
	if s.Context != nil {
		return s.Context
	}
	return context.Background()
}

// ResetIdentity drops the cached listing, which belongs to the account that
// was signed in.
func (s *Service) ResetIdentity() {
	s.mu.Lock()
	s.cache, s.cached = nil, time.Time{}
	if s.topMore != nil {
		s.topMore.cancel()
		s.topMore = nil
	}
	for _, lookup := range s.searches {
		lookup.cancel()
	}
	s.searches = nil
	s.gen++
	s.mu.Unlock()
}

// Run waits for the daemon to stop, then for running clones to clean up.
func (s *Service) Run(ctx context.Context) {
	<-ctx.Done()
	s.mu.Lock()
	s.stopping = true
	if s.topMore != nil {
		s.topMore.cancel()
	}
	for _, lookup := range s.searches {
		lookup.cancel()
	}
	s.mu.Unlock()
	s.running.Wait()
}

// Item is one repository in the listing.
type Item struct {
	Repository  string     `json:"repository"`
	Private     bool       `json:"private"`
	Top         bool       `json:"top"`
	Fork        bool       `json:"fork"`
	PushedAt    *time.Time `json:"pushed_at"`
	Description string     `json:"description"`
	Picked      bool       `json:"picked"`
	// Clone is an available registered clone of a picked repository, or "".
	// Clones are looked for only once a repository is picked.
	Clone string `json:"clone"`
	// EarlierNames are names GitHub forwards to a picked repository that its
	// clones' remotes were registered with.
	EarlierNames []string `json:"earlier_names,omitempty"`
}

// Listing is the JSON of GET /api/github/repositories.
type Listing struct {
	FetchedAt       time.Time `json:"fetched_at"`
	TotalCount      int       `json:"total_count"`
	Repositories    []Item    `json:"repositories"`
	Searching       bool      `json:"searching,omitempty"`
	SearchError     string    `json:"search_error,omitempty"`
	TopRepositories []Item    `json:"top_repositories,omitempty"`
	// HasMore says GitHub has another page for this query, which ?more=1 loads.
	HasMore bool `json:"has_more,omitempty"`
	// OrganizationAccess is set when the person belongs to an organization
	// whose repositories this sign-in cannot see, typically because it
	// restricts OAuth app access until an owner approves baum.
	OrganizationAccess *githubauth.OrganizationAccess `json:"organization_access,omitempty"`
}

// maxSearchResults is the most results GitHub search returns for one query.
const maxSearchResults = 1000

func (s *Service) viewerRepositories(refresh bool) (github.ViewerRepositoryList, time.Time, error) {
	s.mu.Lock()
	gen := s.gen
	if refresh && s.topMore != nil {
		s.topMore.cancel()
		s.topMore = nil
	}
	if !refresh && s.cache != nil && s.now().Sub(s.cached) < CacheTTL {
		list, at := *s.cache, s.cached
		s.mu.Unlock()
		return list, at, nil
	}
	s.mu.Unlock()
	// Requests asking together share one call, which one client leaving does
	// not cancel.
	type result struct {
		list github.ViewerRepositoryList
		at   time.Time
	}
	v, err, _ := s.flight.Do("list-"+strconv.FormatUint(gen, 10), func() (any, error) {
		ctx, cancel := context.WithTimeout(s.ctx(), listTimeout)
		defer cancel()
		list, err := s.Lister.ViewerRepositories(ctx, config.GitHubHost, github.InitialViewerRepositories, nil)
		if err != nil {
			return nil, err
		}
		if list.Repositories == nil {
			list.Repositories = []github.ViewerRepository{}
		}
		at := s.now()
		s.mu.Lock()
		if s.gen == gen {
			if s.topMore != nil {
				s.topMore.cancel()
				s.topMore = nil
			}
			shown := list
			if list.NextCursor != nil && s.cache != nil {
				// A reload keeps the earlier extra rows until the new
				// additional page replaces them, so the list never shrinks to
				// the first page and grows back.
				shown.Repositories = mergeRepositories(list.Repositories, s.cache.Repositories)
			}
			s.cache, s.cached = &shown, at
			if list.NextCursor != nil && !s.stopping {
				moreCtx, cancelMore := context.WithTimeout(s.ctx(), listTimeout)
				lookup := &repositoryLookup{list: list, running: true, cancel: cancelMore}
				s.topMore = lookup
				s.running.Add(1)
				go s.loadMoreTopRepositories(moreCtx, lookup, list.NextCursor)
			}
		}
		s.mu.Unlock()
		return result{list, at}, nil
	})
	if err != nil {
		return github.ViewerRepositoryList{}, time.Time{}, err
	}
	r := v.(result)
	return r.list, r.at, nil
}

func (s *Service) loadMoreTopRepositories(ctx context.Context, lookup *repositoryLookup, cursor *string) {
	defer s.running.Done()
	defer lookup.cancel()
	list, err := s.Lister.ViewerRepositories(ctx, config.GitHubHost, github.AdditionalViewerRepositories, cursor)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.topMore != lookup {
		return
	}
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil {
		// The rows this request continues, then the new rows; rows from an
		// earlier listing that neither returned are dropped.
		merged := mergeRepositories(lookup.list.Repositories, list.Repositories)
		s.cache = &github.ViewerRepositoryList{Repositories: merged, TotalCount: list.TotalCount, Scope: lookup.list.Scope, NextCursor: list.NextCursor}
		s.cached = s.now()
	}
	lookup.running, lookup.err = false, err
}

// mergeRepositories appends the rows of more that first lacks, keeping first's
// order and first's copy of a repository both contain.
func mergeRepositories(first, more []github.ViewerRepository) []github.ViewerRepository {
	out := make([]github.ViewerRepository, 0, len(first)+len(more))
	seen := map[string]bool{}
	for _, batch := range [][]github.ViewerRepository{first, more} {
		for _, r := range batch {
			if !seen[r.Repository] {
				seen[r.Repository] = true
				out = append(out, r)
			}
		}
	}
	return out
}

func (s *Service) searchRepositories(query string, refresh bool) (github.ViewerRepositoryList, time.Time, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return github.ViewerRepositoryList{}, time.Time{}, false, context.Canceled
	}
	if s.searches == nil {
		s.searches = map[string]*repositoryLookup{}
	}
	for key, lookup := range s.searches {
		if !lookup.running && s.now().Sub(lookup.at) >= CacheTTL {
			delete(s.searches, key)
		}
	}
	lookup := s.searches[query]
	if refresh || lookup == nil {
		if lookup != nil {
			lookup.cancel()
		}
		if len(s.searches) >= 64 {
			var oldest string
			for key, entry := range s.searches {
				if oldest == "" || entry.at.Before(s.searches[oldest].at) {
					oldest = key
				}
			}
			s.searches[oldest].cancel()
			delete(s.searches, oldest)
		}
		ctx, cancel := context.WithTimeout(s.ctx(), listTimeout)
		lookup = &repositoryLookup{list: github.ViewerRepositoryList{Repositories: []github.ViewerRepository{}}, at: s.now(), running: true, cancel: cancel, scope: s.searchScope()}
		s.searches[query] = lookup
		s.running.Add(1)
		go s.loadSearch(ctx, query, nil, lookup)
	}
	return lookup.list, lookup.at, lookup.running, lookup.err
}

// searchScope is what a search covers: the viewer's and its organizations'
// repositories and the repositories it collaborates on. Owners of top
// repositories are not added: the viewer may only have contributed to one of
// their repositories. Top repositories still match from the cache. The caller
// holds s.mu.
func (s *Service) searchScope() github.SearchScope {
	if s.cache == nil {
		return github.SearchScope{}
	}
	return s.cache.Scope
}

// loadSearch loads the first page of a search, or with a cursor the next one,
// which is appended to the rows already loaded.
func (s *Service) loadSearch(ctx context.Context, query string, cursor *string, lookup *repositoryLookup) {
	defer s.running.Done()
	s.mu.Lock()
	cancel, scope := lookup.cancel, lookup.scope
	s.mu.Unlock()
	defer cancel()
	list, err := s.Lister.SearchRepositories(ctx, config.GitHubHost, query, scope, cursor)
	if err == nil && ctx.Err() != nil {
		list, err = github.ViewerRepositoryList{}, ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.searches[query] != lookup {
		return
	}
	lookup.err, lookup.running, lookup.at = err, false, s.now()
	switch {
	case err != nil && cursor != nil:
		// The rows already loaded stay; a retry starts the search again.
	case cursor != nil:
		merged := mergeRepositories(lookup.list.Repositories, list.Repositories)
		lookup.list = github.ViewerRepositoryList{Repositories: merged, TotalCount: list.TotalCount, NextCursor: list.NextCursor}
	default:
		lookup.list = list
	}
}

// loadMore starts loading the next page of the top list (an empty query) or
// of query's search, unless it has none or a page is already loading. It
// reports whether a page is loading. The caller holds s.mu.
func (s *Service) loadMore(query string) bool {
	if s.stopping {
		return false
	}
	if query == "" {
		if s.topMore != nil && s.topMore.running {
			return true
		}
		if s.cache == nil || s.cache.NextCursor == nil {
			return false
		}
		ctx, cancel := context.WithTimeout(s.ctx(), listTimeout)
		lookup := &repositoryLookup{list: *s.cache, running: true, cancel: cancel}
		s.topMore = lookup
		s.running.Add(1)
		go s.loadMoreTopRepositories(ctx, lookup, s.cache.NextCursor)
		return true
	}
	lookup := s.searches[query]
	if lookup == nil {
		return false
	}
	if lookup.running {
		return true
	}
	if lookup.err != nil || lookup.list.NextCursor == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(s.ctx(), listTimeout)
	lookup.running, lookup.cancel = true, cancel
	s.running.Add(1)
	go s.loadSearch(ctx, query, lookup.list.NextCursor, lookup)
	return true
}

func matches(name string, terms []string) bool {
	for _, t := range terms {
		if !strings.Contains(name, t) {
			return false
		}
	}
	return true
}

// Listing combines immediate cached top matches with a cached GitHub search.
// more starts loading the query's next page.
func (s *Service) Listing(ctx context.Context, query string, refresh, more bool) (Listing, error) {
	query = strings.Join(strings.Fields(strings.ToLower(query)), " ")
	s.mu.Lock()
	gen := s.gen
	s.mu.Unlock()
	var list github.ViewerRepositoryList
	var at time.Time
	var err error
	var searching bool
	if query == "" {
		list, at, err = s.viewerRepositories(refresh)
	} else {
		// A search is scoped to the owners the top list names, and ranks and
		// answers from it, so a search that comes first loads its first page.
		s.mu.Lock()
		missing := s.cache == nil
		s.mu.Unlock()
		if missing {
			_, _, err = s.viewerRepositories(false)
		}
		if err == nil {
			list, at, searching, err = s.searchRepositories(query, refresh)
		}
	}
	s.mu.Lock()
	if gen != s.gen {
		s.mu.Unlock()
		return Listing{}, context.Canceled
	}
	if more && err == nil && s.loadMore(query) {
		searching = true
		if lookup := s.searches[query]; query != "" && lookup != nil {
			list = lookup.list
		}
	}
	var top github.ViewerRepositoryList
	if s.cache != nil {
		top = *s.cache
	}
	if s.topMore != nil {
		searching = searching || s.topMore.running
		if query == "" && err == nil {
			err = s.topMore.err
		}
	}
	s.mu.Unlock()
	if query == "" && err != nil && len(top.Repositories) == 0 {
		return Listing{}, err
	}
	picked, registryErr := s.Registry.Repositories(ctx)
	if registryErr != nil {
		return Listing{}, fmt.Errorf("%w: %w", errRegistry, registryErr)
	}
	pickedSet := map[string]bool{}
	registered := map[string]string{}
	earlier := map[string][]string{}
	for _, p := range picked {
		name := strings.ToLower(p.Repository)
		pickedSet[name], earlier[name] = true, p.EarlierNames
		for _, c := range p.Clones {
			if c.Available && registered[name] == "" {
				registered[name] = c.Path
			}
		}
	}
	topSet := map[string]bool{}
	for _, r := range top.Repositories {
		topSet[r.Repository] = true
	}
	itemOf := func(r github.ViewerRepository) Item {
		item := Item{Repository: r.Repository, Private: r.Private, Top: topSet[r.Repository], Fork: r.Fork, Description: r.Description, Picked: pickedSet[r.Repository], Clone: registered[r.Repository], EarlierNames: earlier[r.Repository]}
		if !r.PushedAt.IsZero() {
			pushed := r.PushedAt
			item.PushedAt = &pushed
		}
		return item
	}
	terms := strings.Fields(query)
	candidates := []github.ViewerRepository{}
	for _, r := range top.Repositories {
		if matches(r.Repository, terms) {
			candidates = append(candidates, r)
		}
	}
	if query != "" {
		// Search rows first so their fresher metadata wins; sorting below
		// decides the order.
		candidates = mergeRepositories(list.Repositories, candidates)
	}
	if err != nil && len(candidates) == 0 {
		return Listing{}, err
	}
	total, hasMore := top.TotalCount, top.NextCursor != nil
	if query == "" && total == 0 {
		total = list.TotalCount
	}
	if query != "" {
		// Every match: the loaded ones, and the search results GitHub has not
		// returned yet (it returns at most a thousand).
		total, hasMore = len(candidates), list.NextCursor != nil
		if rest := min(list.TotalCount, maxSearchResults) - len(list.Repositories); hasMore && rest > 0 {
			total += rest
		}
	}
	out := Listing{FetchedAt: at, TotalCount: total, Repositories: []Item{}, Searching: searching, HasMore: hasMore}
	if err != nil {
		out.SearchError = s.classify(err).message
	}
	for _, r := range candidates {
		out.Repositories = append(out.Repositories, itemOf(r))
	}
	if query != "" {
		for _, r := range top.Repositories {
			out.TopRepositories = append(out.TopRepositories, itemOf(r))
		}
	}
	if query != "" {
		sort.SliceStable(out.Repositories, func(i, j int) bool { return repositoryBefore(out.Repositories[i], out.Repositories[j], query) })
	}
	if s.Organizations != nil {
		if access, err := s.Organizations(ctx, refresh); err == nil && len(access.Hidden) > 0 {
			out.OrganizationAccess = &access
		}
	}
	return out, nil
}

func repositoryBefore(a, b Item, query string) bool {
	exact := func(r Item) bool {
		_, name, _ := strings.Cut(r.Repository, "/")
		return query != "" && (r.Repository == query || name == query)
	}
	if ae, be := exact(a), exact(b); ae != be {
		return ae
	}
	if a.Top != b.Top {
		return a.Top
	}
	var ap, bp time.Time
	if a.PushedAt != nil {
		ap = *a.PushedAt
	}
	if b.PushedAt != nil {
		bp = *b.PushedAt
	}
	if !ap.Equal(bp) {
		return ap.After(bp)
	}
	return a.Repository < b.Repository
}

type apiFailure struct {
	status  int
	code    string
	message string
	resume  time.Time
}

// classify maps a GitHub failure to the codes the app already explains for the
// GitHub connection, plus not_connected and rate_limited. It never repeats the
// upstream error, which may quote a response.
func (s *Service) classify(err error) apiFailure {
	if f, ok := classifyKnown(err); ok {
		return f
	}
	if s.Health != nil {
		if health := s.Health(); health != nil {
			if f, ok := classifyKnown(health); ok {
				return f
			}
		}
	}
	return apiFailure{status: http.StatusBadGateway, code: "github_unavailable", message: "GitHub could not be reached or refused the request. Try again."}
}

func classifyKnown(err error) (apiFailure, bool) {
	var limited *github.RateLimitedError
	switch {
	case errors.Is(err, config.ErrNoGitHubToken), errors.Is(err, githubauth.ErrNotConnected):
		return apiFailure{status: http.StatusConflict, code: "not_connected", message: "Connect GitHub to list your repositories."}, true
	case errors.As(err, &limited):
		return apiFailure{status: http.StatusTooManyRequests, code: "rate_limited", message: "GitHub rate limit reached. Try again later.", resume: limited.ResumeAt}, true
	}
	for _, known := range []struct {
		err     error
		status  int
		code    string
		message string
	}{
		{githubauth.ErrStore, http.StatusServiceUnavailable, "credential_store_unavailable", "Unlock your operating system credential store and try again."},
		{githubauth.ErrReconnect, http.StatusConflict, "reconnect_required", "Reconnect GitHub; authorization expired or was revoked."},
		{githubauth.ErrScopes, http.StatusConflict, "insufficient_scopes", "GitHub authorization requires repository, organization read and workflow access. Reconnect and grant all three permissions."},
		{githubauth.ErrSSO, http.StatusConflict, "sso_required", "Authorize baum for your organization's GitHub SSO and try again."},
		{githubauth.ErrPermissions, http.StatusConflict, "permission_denied", "Check repository access and GitHub authorization permissions."},
	} {
		if errors.Is(err, known.err) {
			return apiFailure{status: known.status, code: known.code, message: known.message}, true
		}
	}
	return apiFailure{}, false
}

func (f apiFailure) write(w http.ResponseWriter) {
	body := map[string]any{"code": f.code, "message": f.message}
	if !f.resume.IsZero() {
		body["resume_at"] = f.resume.UTC()
		if wait := time.Until(f.resume); wait > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		}
	}
	writeJSON(w, f.status, body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"code": code, "message": message})
}

// Handler serves the listing and the clone routes.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/github/repositories", func(w http.ResponseWriter, r *http.Request) {
		flag := func(name string) bool { v := r.URL.Query().Get(name); return v == "1" || v == "true" }
		listing, err := s.Listing(r.Context(), r.URL.Query().Get("query"), flag("refresh"), flag("more"))
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			if errors.Is(err, errRegistry) {
				fail(w, http.StatusInternalServerError, "registry_unavailable", "The repository registry could not be read.")
				return
			}
			s.classify(err).write(w)
			return
		}
		writeJSON(w, http.StatusOK, listing)
	})
	mux.HandleFunc("GET /api/repositories/{owner}/{name}/clone-target", s.cloneTarget)
	mux.HandleFunc("GET /api/repositories/{owner}/{name}/clone", s.cloneStatus)
	mux.HandleFunc("POST /api/repositories/{owner}/{name}/clone", s.startClone)
	return mux
}

// homes returns the home directory and the same directory with symlinks
// resolved.
func (s *Service) homes() (home, realHome string, err error) {
	home = s.Home
	if home == "" {
		if home, err = os.UserHomeDir(); err != nil {
			return "", "", err
		}
	}
	if !filepath.IsAbs(home) {
		return "", "", errors.New("home directory is not absolute")
	}
	home = filepath.Clean(home)
	realHome, err = filepath.EvalSymlinks(home)
	if err != nil {
		realHome = home
	}
	return home, realHome, nil
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// resolve follows symlinks in the deepest existing part of p and appends the
// rest.
func resolve(p string) string {
	existing, rest := p, ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		next := filepath.Dir(existing)
		if next == existing {
			return p
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = next
	}
	real, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return p
	}
	return filepath.Join(real, rest)
}

// checkLocation says whether dir may hold a clone. The rule: dir is below the
// home directory (as written, or as resolved), not the home directory itself,
// and its resolved location is inside the resolved home directory, or inside
// the resolved target of a symlink that is a direct child of the home
// directory (a ~/code linked to another disk). A symlink deeper in the path
// may not lead out of the home directory.
func checkLocation(dir, home, realHome string) error {
	if dir == home || dir == realHome {
		return errors.New("dir cannot be the home directory itself")
	}
	real := resolve(dir)
	switch {
	case within(realHome, real):
		if within(home, dir) || within(realHome, dir) {
			return nil
		}
	case within(home, dir):
		rel, _ := filepath.Rel(home, dir)
		top := filepath.Join(home, strings.Split(rel, string(filepath.Separator))[0])
		if info, err := os.Lstat(top); err == nil && info.Mode()&os.ModeSymlink != 0 {
			if target, err := filepath.EvalSymlinks(top); err == nil && within(target, real) {
				return nil
			}
		}
	}
	return errors.New("dir must be inside the home directory")
}

// atHome rewrites a path under the resolved home directory to the same place
// under the home directory as written.
func atHome(p, home, realHome string) (string, bool) {
	switch {
	case p == home || within(home, p):
		return p, true
	case p == realHome || within(realHome, p):
		rel, _ := filepath.Rel(realHome, p)
		return filepath.Join(home, rel), true
	}
	return "", false
}

// DefaultDir is where a repository is cloned unless the person chooses. Beside
// the registered clones: the most common parent directory below home, so
// <parent>/<name>; but if the clones are laid out as <root>/<owner>/<name>
// (their parent is named after their owner), <root>/<owner>/<name>. With no
// clone, ~/code/<owner>/<name>. -2, -3... is appended while the path exists.
// The result always passes the location rule POST applies.
func (s *Service) DefaultDir(ctx context.Context, owner, name string) (string, error) {
	home, realHome, err := s.homes()
	if err != nil {
		return "", err
	}
	clones, err := s.Registry.CloneRows(ctx)
	if err != nil {
		return "", err
	}
	type layout struct {
		byOwner bool
		base    string
	}
	votes := map[layout]int{}
	for _, c := range clones {
		if !filepath.IsAbs(c.Path) {
			continue
		}
		path, ok := atHome(filepath.Clean(c.Path), home, realHome)
		if !ok {
			continue
		}
		parent := filepath.Dir(path)
		if parent != home && !within(home, parent) {
			continue
		}
		ownerOf, _, _ := strings.Cut(c.Repository, "/")
		if root := filepath.Dir(parent); ownerOf != "" && strings.EqualFold(filepath.Base(parent), ownerOf) && (root == home || within(home, root)) {
			votes[layout{true, root}]++
		} else {
			votes[layout{false, parent}]++
		}
	}
	best, bestVotes := layout{true, filepath.Join(home, "code")}, 0
	for l, n := range votes {
		if n > bestVotes || (n == bestVotes && (!l.byOwner && best.byOwner || l.byOwner == best.byOwner && l.base < best.base)) {
			best, bestVotes = l, n
		}
	}
	pick := func(l layout) string {
		if l.byOwner {
			return filepath.Join(l.base, owner, name)
		}
		return filepath.Join(l.base, name)
	}
	base := pick(best)
	if checkLocation(base, home, realHome) != nil {
		base = pick(layout{true, filepath.Join(home, "code")})
	}
	candidate := base
	for i := 2; i < 1000; i++ {
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			if err := checkLocation(candidate, home, realHome); err != nil {
				return "", err
			}
			return candidate, nil
		}
		candidate = base + "-" + strconv.Itoa(i)
	}
	return "", errors.New("no free folder name")
}

// repositoryName validates the owner/name of a route and returns the
// lowercase identity.
func repositoryName(r *http.Request) (identity string, ok bool) {
	name := r.PathValue("owner") + "/" + r.PathValue("name")
	if _, err := gitlocal.GitHubCloneURL(name); err != nil {
		return "", false
	}
	return strings.ToLower(name), true
}

func (s *Service) cloneTarget(w http.ResponseWriter, r *http.Request) {
	if _, ok := repositoryName(r); !ok {
		fail(w, http.StatusBadRequest, "invalid_repository", "Repository must be a github.com owner/name.")
		return
	}
	dir, err := s.DefaultDir(r.Context(), r.PathValue("owner"), r.PathValue("name"))
	if err != nil {
		fail(w, http.StatusInternalServerError, "clone_target_unavailable", "No folder could be suggested.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"dir": dir})
}

// CloneState is a clone's progress, as the API serves it.
type CloneState struct {
	Repository string `json:"repository"`
	Dir        string `json:"dir"`
	// State is running, done or failed.
	State string `json:"state"`
	Error string `json:"error,omitempty"`
	// Code names a failure the app explains itself, such as url_rewritten.
	Code string `json:"code,omitempty"`
}

type cloneState struct {
	CloneState
	key string
}

func (s *Service) cloneStatus(w http.ResponseWriter, r *http.Request) {
	identity, ok := repositoryName(r)
	if !ok {
		fail(w, http.StatusBadRequest, "invalid_repository", "Repository must be a github.com owner/name.")
		return
	}
	s.mu.Lock()
	c := s.clones[identity]
	var state CloneState
	if c != nil {
		state = c.CloneState
	}
	s.mu.Unlock()
	if c == nil {
		fail(w, http.StatusNotFound, "no_clone", "No clone was started for this repository.")
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// validateDir checks a requested clone folder: absolute, in a permitted
// location (see checkLocation), and either missing or an empty directory.
func (s *Service) validateDir(dir string) (string, error) {
	if dir == "" || !filepath.IsAbs(dir) {
		return "", errors.New("dir must be an absolute path")
	}
	dir = filepath.Clean(dir)
	home, realHome, err := s.homes()
	if err != nil {
		return "", errors.New("the home directory is unknown")
	}
	if err := checkLocation(dir, home, realHome); err != nil {
		return "", err
	}
	if info, err := os.Lstat(dir); err == nil {
		if !info.IsDir() {
			return "", errors.New("dir exists and is not a directory")
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return "", errors.New("dir cannot be read")
		}
		if len(entries) > 0 {
			return "", errors.New("dir exists and is not empty")
		}
	}
	return dir, nil
}

// reservationKey resolves aliases before comparing running targets. Case folding
// also prevents overlaps on file systems that treat case variants alike.
func reservationKey(dir string) string { return strings.ToLower(resolve(filepath.Clean(dir))) }

func overlaps(a, b string) bool {
	sep := string(filepath.Separator)
	return a == b || strings.HasPrefix(a, b+sep) || strings.HasPrefix(b, a+sep)
}

func (s *Service) startClone(w http.ResponseWriter, r *http.Request) {
	identity, ok := repositoryName(r)
	if !ok {
		fail(w, http.StatusBadRequest, "invalid_repository", "Repository must be a github.com owner/name.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	var body struct {
		Dir string `json:"dir"`
	}
	if err := d.Decode(&body); err != nil && !errors.Is(err, io.EOF) || (err == nil && d.Decode(new(any)) != io.EOF) {
		fail(w, http.StatusBadRequest, "invalid_request", "Invalid JSON request.")
		return
	}
	dir := body.Dir
	if dir == "" {
		var err error
		if dir, err = s.DefaultDir(r.Context(), r.PathValue("owner"), r.PathValue("name")); err != nil {
			fail(w, http.StatusInternalServerError, "clone_target_unavailable", "No folder could be suggested.")
			return
		}
	}
	dir, err := s.validateDir(dir)
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid_dir", err.Error())
		return
	}
	url, err := gitlocal.GitHubCloneURL(r.PathValue("owner") + "/" + r.PathValue("name"))
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid_repository", "Repository must be a github.com owner/name.")
		return
	}

	s.mu.Lock()
	if s.stopping || s.ctx().Err() != nil {
		s.mu.Unlock()
		fail(w, http.StatusServiceUnavailable, "stopping", "baum is stopping; try again after it restarts.")
		return
	}
	if c := s.clones[identity]; c != nil && c.State == "running" {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "clone_running", "A clone of this repository is already running.")
		return
	}
	key := reservationKey(dir)
	for other, by := range s.reserved {
		if overlaps(key, other) {
			s.mu.Unlock()
			fail(w, http.StatusConflict, "dir_in_use", "Another clone ("+by+") is using that folder.")
			return
		}
	}
	if s.reserved == nil {
		s.reserved = map[string]string{}
	}
	s.reserved[key] = identity
	state := &cloneState{CloneState: CloneState{Repository: identity, Dir: dir, State: "running"}, key: key}
	if s.clones == nil {
		s.clones = map[string]*cloneState{}
	}
	s.clones[identity] = state
	snapshot := state.CloneState
	s.running.Add(1)
	s.mu.Unlock()

	// Picking is part of cloning: asking for a clone is choosing the repository.
	if _, err := s.Registry.Pick(r.Context(), identity, nil); err != nil {
		s.finish(state, errors.New("the repository could not be picked"))
		s.running.Done()
		fail(w, http.StatusInternalServerError, "registry_unavailable", "The repository could not be picked.")
		return
	}
	if s.Changed != nil {
		s.Changed()
	}
	go func() {
		defer s.running.Done()
		s.finish(state, s.runClone(identity, url, dir))
	}()
	writeJSON(w, http.StatusAccepted, snapshot)
}

func (s *Service) finish(state *cloneState, err error) {
	s.mu.Lock()
	delete(s.reserved, state.key)
	if err != nil {
		state.State, state.Error = "failed", err.Error()
		var coded *codedError
		if errors.As(err, &coded) {
			state.Code = coded.code
		}
	} else {
		state.State = "done"
	}
	s.mu.Unlock()
	if s.Changed != nil {
		s.Changed()
	}
}

type codedError struct{ code, message string }

func (e *codedError) Error() string { return e.message }

func (s *Service) runClone(identity, url, dir string) error {
	ctx := s.ctx()
	token := ""
	if s.Token != nil {
		t, err := s.Token(ctx)
		switch {
		case err == nil:
			token = t
		case errors.Is(err, config.ErrNoGitHubToken), errors.Is(err, githubauth.ErrNotConnected),
			strings.Contains(err.Error(), config.ErrNoGitHubToken.Error()):
			// Signed out: a public repository still clones.
		default:
			return errors.New(s.classify(err).message)
		}
	}
	clone := s.Clone
	if clone == nil {
		clone = gitlocal.Clone
	}
	if err := clone(ctx, gitlocal.CloneOptions{URL: url, Dir: dir, Token: token}); err != nil {
		if ctx.Err() != nil {
			return errors.New("the clone was cancelled")
		}
		if errors.Is(err, gitlocal.ErrURLRewritten) {
			return &codedError{"url_rewritten", gitlocal.ErrURLRewritten.Error()}
		}
		return errors.New(safeMessage(err, token))
	}
	if _, err := s.Registry.AddFor(ctx, dir, identity, nil); err != nil {
		return errors.New("cloned, but the clone could not be registered: " + safeMessage(err, token))
	}
	return nil
}

// safeMessage is err's text without the token, bounded.
func safeMessage(err error, token string) string {
	msg := err.Error()
	if token != "" {
		msg = strings.ReplaceAll(msg, token, "***")
	}
	if len(msg) > 512 {
		msg = strings.ToValidUTF8(msg[:512], "") + "..."
	}
	return msg
}
