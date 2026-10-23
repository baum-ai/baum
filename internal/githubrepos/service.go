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

	"github.com/baum-ai/baum/internal/clonefind"
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
	ViewerRepositories(ctx context.Context, host string) ([]github.ViewerRepository, error)
}

// Registry is the part of the repository registry the service uses.
type Registry interface {
	Repositories(context.Context) ([]repos.Repository, error)
	List(context.Context) ([]repos.Clone, error)
	Pick(ctx context.Context, repository string, settings *repos.Settings) (repos.Repository, error)
	AddFor(ctx context.Context, path, repository string, settings *repos.Settings) (repos.Clone, error)
}

// Service serves the listing and the clone API. It has no authentication: the
// daemon mounts Handler behind its authenticated host and origin checks,
// because it returns and writes local paths.
type Service struct {
	Lister   Lister
	Registry Registry
	// Found returns the clone finder's last result; nil means none found.
	Found func() clonefind.Found
	// Token returns the GitHub token for github.com; an error wrapping
	// config.ErrNoGitHubToken means signed out, and clones are anonymous.
	Token func(ctx context.Context) (string, error)
	// Clone runs the clone; nil uses gitlocal.Clone.
	Clone func(ctx context.Context, opts gitlocal.CloneOptions) error
	// Health reports the runtime health of the GitHub sign-in after a failed
	// request (config.GitHubAuth.Health); nil means healthy.
	Health func() error
	// Home is the home directory clones must stay inside.
	Home string
	// Context ends running clones when the daemon stops; nil means never.
	Context context.Context
	// Changed tells the app the registry changed.
	Changed func()
	Now     func() time.Time

	mu      sync.Mutex
	cache   []github.ViewerRepository
	cached  time.Time
	flight  singleflight.Group
	clones  map[string]*cloneState
	running sync.WaitGroup
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
	s.mu.Unlock()
	s.flight.Forget("list")
}

// Run waits for the daemon to stop, then for running clones to clean up.
func (s *Service) Run(ctx context.Context) {
	<-ctx.Done()
	s.running.Wait()
}

// Item is one repository in the listing.
type Item struct {
	Repository       string     `json:"repository"`
	Private          bool       `json:"private"`
	Fork             bool       `json:"fork"`
	PushedAt         *time.Time `json:"pushed_at"`
	Description      string     `json:"description"`
	OpenPullRequests int        `json:"open_pull_requests"`
	Picked           bool       `json:"picked"`
	// Clone is the best local clone the clone finder found, or "".
	Clone string `json:"clone"`
}

// Listing is the JSON of GET /api/github/repositories.
type Listing struct {
	FetchedAt    time.Time `json:"fetched_at"`
	Repositories []Item    `json:"repositories"`
}

func (s *Service) viewerRepositories(refresh bool) ([]github.ViewerRepository, time.Time, error) {
	s.mu.Lock()
	if !refresh && s.cache != nil && s.now().Sub(s.cached) < CacheTTL {
		list, at := s.cache, s.cached
		s.mu.Unlock()
		return list, at, nil
	}
	s.mu.Unlock()
	// Requests asking together share one call, which one client leaving does
	// not cancel.
	type result struct {
		list []github.ViewerRepository
		at   time.Time
	}
	v, err, _ := s.flight.Do("list", func() (any, error) {
		ctx, cancel := context.WithTimeout(s.ctx(), listTimeout)
		defer cancel()
		list, err := s.Lister.ViewerRepositories(ctx, config.GitHubHost)
		if err != nil {
			return nil, err
		}
		if list == nil {
			list = []github.ViewerRepository{}
		}
		at := s.now()
		s.mu.Lock()
		s.cache, s.cached = list, at
		s.mu.Unlock()
		return result{list, at}, nil
	})
	if err != nil {
		return nil, time.Time{}, err
	}
	r := v.(result)
	return r.list, r.at, nil
}

// ranked orders repositories with open pull requests involving the account
// first, most first, then by latest push.
func ranked(list []github.ViewerRepository) []github.ViewerRepository {
	out := append([]github.ViewerRepository(nil), list...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.OpenPullRequests != b.OpenPullRequests {
			return a.OpenPullRequests > b.OpenPullRequests
		}
		if !a.PushedAt.Equal(b.PushedAt) {
			return a.PushedAt.After(b.PushedAt)
		}
		return a.Repository < b.Repository
	})
	return out
}

func matches(name string, terms []string) bool {
	for _, t := range terms {
		if !strings.Contains(name, t) {
			return false
		}
	}
	return true
}

// Listing builds the ranked, filtered listing.
func (s *Service) Listing(ctx context.Context, query string, refresh bool) (Listing, error) {
	list, at, err := s.viewerRepositories(refresh)
	if err != nil {
		return Listing{}, err
	}
	picked, err := s.Registry.Repositories(ctx)
	if err != nil {
		return Listing{}, fmt.Errorf("%w: %w", errRegistry, err)
	}
	pickedSet := map[string]bool{}
	for _, p := range picked {
		pickedSet[strings.ToLower(p.Repository)] = true
	}
	var found map[string][]clonefind.FoundClone
	if s.Found != nil {
		found = s.Found().Repositories
	}
	terms := strings.Fields(strings.ToLower(query))
	out := Listing{FetchedAt: at, Repositories: []Item{}}
	for _, r := range ranked(list) {
		if !matches(r.Repository, terms) {
			continue
		}
		item := Item{Repository: r.Repository, Private: r.Private, Fork: r.Fork, Description: r.Description,
			OpenPullRequests: r.OpenPullRequests, Picked: pickedSet[r.Repository]}
		if !r.PushedAt.IsZero() {
			pushed := r.PushedAt
			item.PushedAt = &pushed
		}
		if clones := found[r.Repository]; len(clones) > 0 {
			item.Clone = clones[0].Path
		}
		out.Repositories = append(out.Repositories, item)
	}
	return out, nil
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
		{githubauth.ErrScopes, http.StatusConflict, "insufficient_scopes", "GitHub authorization requires repository and organization read access."},
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
		refresh := r.URL.Query().Get("refresh")
		listing, err := s.Listing(r.Context(), r.URL.Query().Get("query"), refresh == "1" || refresh == "true")
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

// home is the home directory, resolved through symlinks.
func (s *Service) home() (string, error) {
	home := s.Home
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return "", err
		}
	}
	if !filepath.IsAbs(home) {
		return "", errors.New("home directory is not absolute")
	}
	return filepath.Clean(home), nil
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// DefaultDir is where a repository is cloned unless the person chooses: beside
// the registered clones (their most common parent directory below home), else
// ~/code/<owner>/<name>, with -2, -3... appended while the path exists.
func (s *Service) DefaultDir(ctx context.Context, owner, name string) (string, error) {
	home, err := s.home()
	if err != nil {
		return "", err
	}
	clones, err := s.Registry.List(ctx)
	if err != nil {
		return "", err
	}
	parents := map[string]int{}
	for _, c := range clones {
		if !filepath.IsAbs(c.Path) {
			continue
		}
		if parent := filepath.Dir(filepath.Clean(c.Path)); parent == home || within(home, parent) {
			parents[parent]++
		}
	}
	base := filepath.Join(home, "code", owner, name)
	best := ""
	for parent, n := range parents {
		if best == "" || n > parents[best] || (n == parents[best] && parent < best) {
			best = parent
		}
	}
	if best != "" {
		base = filepath.Join(best, name)
	}
	candidate := base
	for i := 2; i < 1000; i++ {
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
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
}

type cloneState struct{ CloneState }

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

// validateDir checks a requested clone folder: absolute, inside home but not
// home, with no symlink leading out, and either missing or an empty directory.
func (s *Service) validateDir(dir string) (string, error) {
	if dir == "" || !filepath.IsAbs(dir) {
		return "", errors.New("dir must be an absolute path")
	}
	dir = filepath.Clean(dir)
	home, err := s.home()
	if err != nil {
		return "", errors.New("the home directory is unknown")
	}
	if dir == home {
		return "", errors.New("dir cannot be the home directory itself")
	}
	if !within(home, dir) {
		return "", errors.New("dir must be inside the home directory")
	}
	// Resolve the deepest existing ancestor: a symlink in the path must not
	// lead out of home.
	realHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		realHome = home
	}
	existing := dir
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		next := filepath.Dir(existing)
		if next == existing {
			break
		}
		existing = next
	}
	if real, err := filepath.EvalSymlinks(existing); err == nil && real != realHome && !within(realHome, real) {
		return "", errors.New("dir must be inside the home directory")
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
	if c := s.clones[identity]; c != nil && c.State == "running" {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "clone_running", "A clone of this repository is already running.")
		return
	}
	state := &cloneState{CloneState{Repository: identity, Dir: dir, State: "running"}}
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
	if err != nil {
		state.State, state.Error = "failed", err.Error()
	} else {
		state.State = "done"
	}
	s.mu.Unlock()
	if s.Changed != nil {
		s.Changed()
	}
}

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
