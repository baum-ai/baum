package githubrepos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/clonefind"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/githubauth"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/repos"
)

type fakeLister struct {
	list  []github.ViewerRepository
	err   error
	calls atomic.Int32
}

func (f *fakeLister) ViewerRepositories(context.Context, string) ([]github.ViewerRepository, error) {
	f.calls.Add(1)
	return f.list, f.err
}

var pushed = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

func sampleList() []github.ViewerRepository {
	return []github.ViewerRepository{
		{Repository: "acme/quiet", PushedAt: pushed.Add(-10 * time.Hour)},
		{Repository: "acme/busy", PushedAt: pushed.Add(-5 * time.Hour), OpenPullRequests: 3, Private: true, Description: "busy repo"},
		{Repository: "acme/recent", PushedAt: pushed, Fork: true},
		{Repository: "other/one-pr", PushedAt: pushed.Add(-20 * time.Hour), OpenPullRequests: 1},
		{Repository: "acme/also-busy", PushedAt: pushed.Add(-30 * time.Hour), OpenPullRequests: 3},
	}
}

type harness struct {
	t        *testing.T
	home     string
	registry *repos.Registry
	lister   *fakeLister
	service  *Service
	changed  atomic.Int32
	handler  http.Handler
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	registry, err := repos.Open(filepath.Join(t.TempDir(), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	h := &harness{t: t, home: t.TempDir(), registry: registry, lister: &fakeLister{list: sampleList()}}
	h.service = &Service{
		Lister: h.lister, Registry: registry, Home: h.home, Changed: func() { h.changed.Add(1) },
		Token: func(context.Context) (string, error) { return "tok", nil },
		Clone: func(_ context.Context, opts gitlocal.CloneOptions) error { return fakeClone(opts) },
	}
	h.handler = h.service.Handler()
	t.Cleanup(h.service.running.Wait)
	return h
}

// fakeClone makes a working tree whose origin is the requested URL.
func fakeClone(opts gitlocal.CloneOptions) error {
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return err
	}
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", opts.URL}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = opts.Dir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %v: %v: %s", args, err, out)
		}
	}
	return nil
}

func (h *harness) do(method, path, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, httptest.NewRequest(method, path, reader))
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("body %q: %v", rec.Body, err)
	}
	return v
}

func names(l Listing) []string {
	var out []string
	for _, r := range l.Repositories {
		out = append(out, r.Repository)
	}
	return out
}

func TestListingRanksFiltersAndAnnotates(t *testing.T) {
	h := newHarness(t)
	if _, err := h.registry.Pick(context.Background(), "acme/quiet", nil); err != nil {
		t.Fatal(err)
	}
	h.service.Found = func() clonefind.Found {
		return clonefind.Found{Repositories: map[string][]clonefind.FoundClone{
			"acme/busy": {{Path: "/work/code/busy"}, {Path: "/work/old/busy"}},
		}}
	}
	rec := h.do("GET", "/api/github/repositories", "")
	if rec.Code != 200 {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	l := decode[Listing](t, rec)
	// Open pull requests first (3, 3, 1; the more recent push breaks the tie), then by push.
	if got, want := strings.Join(names(l), " "), "acme/busy acme/also-busy other/one-pr acme/recent acme/quiet"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
	busy := l.Repositories[0]
	if !busy.Private || busy.OpenPullRequests != 3 || busy.Description != "busy repo" || busy.Clone != "/work/code/busy" || busy.Picked || busy.PushedAt == nil {
		t.Errorf("busy = %+v", busy)
	}
	if quiet := l.Repositories[4]; !quiet.Picked || quiet.Clone != "" {
		t.Errorf("quiet = %+v", quiet)
	}
	if recent := l.Repositories[3]; !recent.Fork {
		t.Errorf("recent = %+v", recent)
	}
	if !strings.Contains(rec.Body.String(), `"clone":""`) {
		t.Error("an item without a clone must still carry the field")
	}

	filtered := decode[Listing](t, h.do("GET", "/api/github/repositories?query=%20ACME/b", ""))
	if got := strings.Join(names(filtered), " "); got != "acme/busy" {
		t.Errorf("query acme/b = %s", got)
	}
	filtered = decode[Listing](t, h.do("GET", "/api/github/repositories?query=busy%20acme", ""))
	if got := strings.Join(names(filtered), " "); got != "acme/busy acme/also-busy" {
		t.Errorf("query terms = %s", got)
	}
	if empty := decode[Listing](t, h.do("GET", "/api/github/repositories?query=zzz", "")); empty.Repositories == nil || len(empty.Repositories) != 0 {
		t.Errorf("no match = %+v, want an empty array", empty.Repositories)
	}
	if h.lister.calls.Load() != 1 {
		t.Errorf("GitHub was called %d times; filtering must not call it", h.lister.calls.Load())
	}
}

func TestListingIsCachedUntilTheTTLOrRefresh(t *testing.T) {
	h := newHarness(t)
	now := pushed
	h.service.Now = func() time.Time { return now }
	for range 3 {
		h.do("GET", "/api/github/repositories", "")
	}
	if n := h.lister.calls.Load(); n != 1 {
		t.Fatalf("calls = %d, want 1 (cached)", n)
	}
	h.lister.list = h.lister.list[:1]
	if l := decode[Listing](t, h.do("GET", "/api/github/repositories", "")); len(l.Repositories) != 5 {
		t.Errorf("served %d repositories, want the cached 5", len(l.Repositories))
	}
	if l := decode[Listing](t, h.do("GET", "/api/github/repositories?refresh=1", "")); len(l.Repositories) != 1 || h.lister.calls.Load() != 2 {
		t.Errorf("refresh served %d repositories after %d calls", len(l.Repositories), h.lister.calls.Load())
	}
	now = now.Add(CacheTTL - time.Second)
	h.do("GET", "/api/github/repositories", "")
	if h.lister.calls.Load() != 2 {
		t.Error("refetched before the TTL")
	}
	now = now.Add(2 * time.Second)
	h.do("GET", "/api/github/repositories", "")
	if h.lister.calls.Load() != 3 {
		t.Error("did not refetch after the TTL")
	}
	h.service.ResetIdentity()
	h.do("GET", "/api/github/repositories", "")
	if h.lister.calls.Load() != 4 {
		t.Error("did not refetch after the account changed")
	}
}

func TestListingMapsGitHubFailuresToConnectionCodes(t *testing.T) {
	resume := time.Now().Add(time.Hour).Truncate(time.Second)
	for _, tc := range []struct {
		name   string
		err    error
		health error
		status int
		code   string
	}{
		{"no token", fmt.Errorf("%w for github.com: connect GitHub in baum Settings", config.ErrNoGitHubToken), nil, 409, "not_connected"},
		{"not connected", githubauth.ErrNotConnected, nil, 409, "not_connected"},
		{"store", fmt.Errorf("token: %w", githubauth.ErrStore), nil, 503, "credential_store_unavailable"},
		{"reconnect", githubauth.ErrReconnect, nil, 409, "reconnect_required"},
		{"scopes", githubauth.ErrScopes, nil, 409, "insufficient_scopes"},
		{"sso", githubauth.ErrSSO, nil, 409, "sso_required"},
		{"permissions", githubauth.ErrPermissions, nil, 409, "permission_denied"},
		{"rate limit", &github.RateLimitedError{ResumeAt: resume, Err: errors.New("raw upstream body SECRET")}, nil, 429, "rate_limited"},
		{"401 observed by the auth hook", errors.New("non-200 OK status code: 401 Unauthorized body=SECRET"), githubauth.ErrReconnect, 409, "reconnect_required"},
		{"sso observed by the auth hook", errors.New("non-200 OK status code: 403 body=SECRET"), githubauth.ErrSSO, 409, "sso_required"},
		{"unknown", errors.New("Post https://api.github.com: token SECRET refused"), nil, 502, "github_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.lister.err = tc.err
			h.service.Health = func() error { return tc.health }
			rec := h.do("GET", "/api/github/repositories", "")
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			body := decode[map[string]any](t, rec)
			if body["code"] != tc.code || body["message"] == "" {
				t.Errorf("body = %v, want code %s", body, tc.code)
			}
			if strings.Contains(rec.Body.String(), "SECRET") {
				t.Errorf("upstream text leaked: %s", rec.Body)
			}
			if tc.code == "rate_limited" {
				if at, _ := body["resume_at"].(string); at != resume.UTC().Format(time.RFC3339) || rec.Header().Get("Retry-After") == "" {
					t.Errorf("resume_at = %v, Retry-After = %q", body["resume_at"], rec.Header().Get("Retry-After"))
				}
			}
			// A failure is not cached: the next request asks again.
			h.lister.err, h.service.Health = nil, nil
			if rec := h.do("GET", "/api/github/repositories", ""); rec.Code != 200 {
				t.Errorf("after recovery: %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestDefaultDirectoryIsBesideExistingClones(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	target := func(repo string) string {
		t.Helper()
		rec := h.do("GET", "/api/repositories/"+repo+"/clone-target", "")
		if rec.Code != 200 {
			t.Fatalf("status %d %s", rec.Code, rec.Body)
		}
		return decode[map[string]string](t, rec)["dir"]
	}

	if got, want := target("Acme/Widgets"), filepath.Join(h.home, "code", "Acme", "Widgets"); got != want {
		t.Errorf("without clones = %s, want %s", got, want)
	}

	// Two clones in ~/work, one in ~/src and one outside home: ~/work wins.
	outside := t.TempDir()
	for _, dir := range []string{filepath.Join(h.home, "work", "a"), filepath.Join(h.home, "work", "b"), filepath.Join(h.home, "src", "c"), filepath.Join(outside, "d"), filepath.Join(outside, "e"), filepath.Join(outside, "f")} {
		if err := fakeClone(gitlocal.CloneOptions{URL: "https://github.com/me/" + filepath.Base(dir) + ".git", Dir: dir}); err != nil {
			t.Fatal(err)
		}
		if _, err := h.registry.Add(ctx, dir, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := target("acme/widgets"), filepath.Join(h.home, "work", "widgets"); got != want {
		t.Errorf("beside clones = %s, want %s", got, want)
	}
	for i, want := range []string{"widgets-2", "widgets-3"} {
		if err := os.MkdirAll(filepath.Join(h.home, "work", []string{"widgets", "widgets-2"}[i]), 0o755); err != nil {
			t.Fatal(err)
		}
		if got := target("acme/widgets"); got != filepath.Join(h.home, "work", want) {
			t.Errorf("taken = %s, want %s", got, want)
		}
	}
	if rec := h.do("GET", "/api/repositories/acme/../clone-target", ""); rec.Code == 200 {
		t.Errorf("a traversal answered %d", rec.Code)
	}
	if rec := h.do("GET", "/api/repositories/ac%20me/w/clone-target", ""); rec.Code != 400 {
		t.Errorf("invalid owner answered %d", rec.Code)
	}
}

func waitFor(t *testing.T, h *harness, repo string, state string) CloneState {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		rec := h.do("GET", "/api/repositories/"+repo+"/clone", "")
		if rec.Code == 200 {
			if s := decode[CloneState](t, rec); s.State == state {
				return s
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("clone of %s never reached %s", repo, state)
	return CloneState{}
}

func TestCloneRejectsBadRequests(t *testing.T) {
	h := newHarness(t)
	existing := filepath.Join(h.home, "full")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existing, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(h.home, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(h.home, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(h.home, "sub", "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	for _, tc := range []struct{ name, body string }{
		{"unknown field", `{"dir":` + quote(filepath.Join(h.home, "x")) + `,"extra":1}`},
		{"trailing data", `{} {}`},
		{"not an object", `[]`},
		{"relative", `{"dir":"code/x"}`},
		{"outside home", `{"dir":` + quote(filepath.Join(outside, "x")) + `}`},
		{"home itself", `{"dir":` + quote(h.home) + `}`},
		{"above home", `{"dir":` + quote(filepath.Dir(h.home)) + `}`},
		{"dotdot out of home", `{"dir":` + quote(h.home+"/../escape") + `}`},
		{"non-empty", `{"dir":` + quote(existing) + `}`},
		{"a file", `{"dir":` + quote(file) + `}`},
		{"through a nested symlink out of home", `{"dir":` + quote(filepath.Join(link, "repo")) + `}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do("POST", "/api/repositories/acme/widgets/clone", tc.body)
			if rec.Code != 400 {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
			}
		})
	}
	if rec := h.do("POST", "/api/repositories/ac%20me/widgets/clone", `{}`); rec.Code != 400 {
		t.Errorf("invalid repository answered %d", rec.Code)
	}
	if list, _ := h.registry.Repositories(context.Background()); len(list) != 0 {
		t.Errorf("a rejected request picked %+v", list)
	}
	if rec := h.do("GET", "/api/repositories/acme/widgets/clone", ""); rec.Code != 404 {
		t.Errorf("status of a clone never started = %d", rec.Code)
	}
}

func TestCloneRunsRegistersAndPublishes(t *testing.T) {
	h := newHarness(t)
	var got gitlocal.CloneOptions
	h.service.Clone = func(_ context.Context, opts gitlocal.CloneOptions) error {
		got = opts
		return fakeClone(opts)
	}
	dir := filepath.Join(h.home, "projects", "widgets")
	rec := h.do("POST", "/api/repositories/Acme/Widgets/clone", `{"dir":`+strconvQuote(dir)+`}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if first := decode[CloneState](t, rec); first.Repository != "acme/widgets" || first.Dir != dir || first.State != "running" {
		t.Errorf("accepted = %+v", first)
	}
	done := waitFor(t, h, "acme/widgets", "done")
	if done.Error != "" || done.Dir != dir {
		t.Errorf("done = %+v", done)
	}
	if got.URL != "https://github.com/Acme/Widgets.git" || got.Dir != dir || got.Token != "tok" {
		t.Errorf("clone options = %+v", got)
	}
	list, err := h.registry.Repositories(context.Background())
	if err != nil || len(list) != 1 || list[0].Repository != "acme/widgets" || len(list[0].Clones) != 1 || list[0].Clones[0].Path != dir || !list[0].Clones[0].Available {
		t.Fatalf("registry = %+v, %v", list, err)
	}
	if h.changed.Load() < 2 {
		t.Errorf("changed published %d times, want after picking and after finishing", h.changed.Load())
	}
	// The default directory now sits beside this clone.
	if next := decode[map[string]string](t, h.do("GET", "/api/repositories/acme/other/clone-target", ""))["dir"]; next != filepath.Join(h.home, "projects", "other") {
		t.Errorf("next default = %s", next)
	}
	// An empty body clones into the default directory; a signed-out person clones anonymously.
	h.service.Token = func(context.Context) (string, error) {
		return "", fmt.Errorf("%w for github.com", config.ErrNoGitHubToken)
	}
	if rec := h.do("POST", "/api/repositories/acme/other/clone", ""); rec.Code != http.StatusAccepted {
		t.Fatalf("empty body: %d %s", rec.Code, rec.Body)
	}
	waitFor(t, h, "acme/other", "done")
	if got.Token != "" || got.Dir != filepath.Join(h.home, "projects", "other") {
		t.Errorf("anonymous clone options = %+v", got)
	}
}

func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestCloneAllowsOneAtATimePerRepository(t *testing.T) {
	h := newHarness(t)
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	h.service.Clone = func(ctx context.Context, opts gitlocal.CloneOptions) error {
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return fakeClone(opts)
	}
	first := filepath.Join(h.home, "one")
	if rec := h.do("POST", "/api/repositories/acme/widgets/clone", `{"dir":`+strconvQuote(first)+`}`); rec.Code != 202 {
		t.Fatalf("first: %d %s", rec.Code, rec.Body)
	}
	<-started
	rec := h.do("POST", "/api/repositories/ACME/Widgets/clone", `{"dir":`+strconvQuote(filepath.Join(h.home, "two"))+`}`)
	if rec.Code != http.StatusConflict || decode[map[string]string](t, rec)["code"] != "clone_running" {
		t.Fatalf("second: %d %s", rec.Code, rec.Body)
	}
	if s := decode[CloneState](t, h.do("GET", "/api/repositories/acme/widgets/clone", "")); s.State != "running" || s.Dir != first {
		t.Errorf("state = %+v", s)
	}
	// Another repository is not blocked.
	if rec := h.do("POST", "/api/repositories/acme/other/clone", `{"dir":`+strconvQuote(filepath.Join(h.home, "three"))+`}`); rec.Code != 202 {
		t.Errorf("other repository: %d %s", rec.Code, rec.Body)
	}
	close(release)
	waitFor(t, h, "acme/widgets", "done")
	waitFor(t, h, "acme/other", "done")
	// After it finishes, a new one may start (into another empty directory).
	if rec := h.do("POST", "/api/repositories/acme/widgets/clone", `{"dir":`+strconvQuote(filepath.Join(h.home, "four"))+`}`); rec.Code != 202 {
		t.Errorf("after done: %d %s", rec.Code, rec.Body)
	}
	waitFor(t, h, "acme/widgets", "done")
}

func TestCloneFailureIsSanitizedAndKeepsThePick(t *testing.T) {
	h := newHarness(t)
	h.service.Token = func(context.Context) (string, error) { return "SECRET-TOKEN", nil }
	h.service.Clone = func(_ context.Context, opts gitlocal.CloneOptions) error {
		return errors.New("git clone: exit 128: fatal: unable to access 'https://x-access-token:SECRET-TOKEN@github.com/': denied")
	}
	dir := filepath.Join(h.home, "failing")
	if rec := h.do("POST", "/api/repositories/acme/widgets/clone", `{"dir":`+strconvQuote(dir)+`}`); rec.Code != 202 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	failed := waitFor(t, h, "acme/widgets", "failed")
	if failed.Error == "" || strings.Contains(failed.Error, "SECRET-TOKEN") {
		t.Errorf("error = %q", failed.Error)
	}
	if list, _ := h.registry.Repositories(context.Background()); len(list) != 1 || len(list[0].Clones) != 0 {
		t.Errorf("registry = %+v, want the repository picked without a clone", list)
	}
	// A failed clone can be retried.
	h.service.Clone = func(_ context.Context, opts gitlocal.CloneOptions) error { return fakeClone(opts) }
	if rec := h.do("POST", "/api/repositories/acme/widgets/clone", `{"dir":`+strconvQuote(dir)+`}`); rec.Code != 202 {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body)
	}
	waitFor(t, h, "acme/widgets", "done")
}

func TestCloneReportsAFailedSignIn(t *testing.T) {
	h := newHarness(t)
	h.service.Token = func(context.Context) (string, error) { return "", fmt.Errorf("keyring: %w", githubauth.ErrStore) }
	called := false
	h.service.Clone = func(context.Context, gitlocal.CloneOptions) error { called = true; return nil }
	h.do("POST", "/api/repositories/acme/widgets/clone", `{"dir":`+strconvQuote(filepath.Join(h.home, "x"))+`}`)
	failed := waitFor(t, h, "acme/widgets", "failed")
	if called || !strings.Contains(failed.Error, "credential store") {
		t.Errorf("called = %v, error = %q", called, failed.Error)
	}
}

func TestCloneStopsWithTheDaemon(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	h.service.Context = ctx
	var wg sync.WaitGroup
	dir := filepath.Join(h.home, "cancelled")
	h.service.Clone = func(ctx context.Context, opts gitlocal.CloneOptions) error {
		_ = os.MkdirAll(opts.Dir, 0o755)
		<-ctx.Done()
		_ = os.RemoveAll(opts.Dir) // gitlocal.Clone's cleanup
		return ctx.Err()
	}
	wg.Go(func() { h.service.Run(ctx) })
	if rec := h.do("POST", "/api/repositories/acme/widgets/clone", `{"dir":`+strconvQuote(dir)+`}`); rec.Code != 202 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	cancel()
	wg.Wait()
	failed := waitFor(t, h, "acme/widgets", "failed")
	if !strings.Contains(failed.Error, "cancelled") {
		t.Errorf("error = %q", failed.Error)
	}
	if _, err := os.Lstat(dir); err == nil {
		t.Error("the partial directory remains")
	}
}

// A response from the previous account must not overwrite a newer cache,
// and a new request must not share the previous account's in-flight request.
type changingLister struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (l *changingLister) ViewerRepositories(context.Context, string) ([]github.ViewerRepository, error) {
	if l.calls.Add(1) == 1 {
		close(l.started)
		<-l.release
		return []github.ViewerRepository{{Repository: "old/private"}}, nil
	}
	return []github.ViewerRepository{{Repository: "new/private"}}, nil
}

func TestAccountChangeDoesNotCacheAnOldInFlightListing(t *testing.T) {
	h := newHarness(t)
	lister := &changingLister{started: make(chan struct{}), release: make(chan struct{})}
	h.service.Lister = lister
	old := make(chan struct{})
	go func() {
		defer close(old)
		_, _, _ = h.service.viewerRepositories(false)
	}()
	<-lister.started
	h.service.ResetIdentity()
	newResult := make(chan []github.ViewerRepository, 1)
	go func() {
		list, _, _ := h.service.viewerRepositories(false)
		newResult <- list
	}()
	select {
	case list := <-newResult:
		if len(list) != 1 || list[0].Repository != "new/private" {
			t.Errorf("new account got %+v", list)
		}
	case <-time.After(5 * time.Second):
		close(lister.release)
		<-old
		t.Fatal("new account joined the old account's request")
	}
	close(lister.release)
	<-old
	list, _, err := h.service.viewerRepositories(false)
	if err != nil || len(list) != 1 || list[0].Repository != "new/private" || lister.calls.Load() != 2 {
		t.Fatalf("cache after old response: %+v, %v, %d calls", list, err, lister.calls.Load())
	}
}

func TestListingIncludesAnAlreadyRegisteredClone(t *testing.T) {
	h := newHarness(t)
	dir := filepath.Join(h.home, "existing")
	if err := fakeClone(gitlocal.CloneOptions{URL: "https://github.com/acme/busy.git", Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.registry.Add(context.Background(), dir, nil); err != nil {
		t.Fatal(err)
	}
	h.service.Found = func() clonefind.Found {
		return clonefind.Found{Repositories: map[string][]clonefind.FoundClone{
			"acme/busy": {{Path: "/another/clone"}},
		}}
	}
	list := decode[Listing](t, h.do("GET", "/api/github/repositories", ""))
	if busy := list.Repositories[0]; !busy.Picked || busy.Clone != dir {
		t.Fatalf("already registered clone = %+v", busy)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	list = decode[Listing](t, h.do("GET", "/api/github/repositories", ""))
	if busy := list.Repositories[0]; busy.Clone != "/another/clone" {
		t.Fatalf("unavailable registered clone hid found clone: %+v", busy)
	}
}

func TestDefaultDirectoryPreservesOwnerLayout(t *testing.T) {
	h := newHarness(t)
	dir := filepath.Join(h.home, "code", "first", "repo")
	if err := fakeClone(gitlocal.CloneOptions{URL: "https://github.com/first/repo.git", Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.registry.Add(context.Background(), dir, nil); err != nil {
		t.Fatal(err)
	}
	got, err := h.service.DefaultDir(context.Background(), "second", "other")
	if want := filepath.Join(h.home, "code", "second", "other"); err != nil || got != want {
		t.Fatalf("default = %s, %v; want %s", got, err, want)
	}
	if _, err := h.service.validateDir(got); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultAndChosenFoldersAgreeWithSymlinks(t *testing.T) {
	t.Run("code folder on another disk", func(t *testing.T) {
		h := newHarness(t)
		outside := t.TempDir()
		link := filepath.Join(h.home, "code")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		got, err := h.service.DefaultDir(context.Background(), "acme", "repo")
		if err != nil || got != filepath.Join(link, "acme", "repo") {
			t.Fatalf("default = %s, %v", got, err)
		}
		if _, err := h.service.validateDir(got); err != nil {
			t.Fatal(err)
		}
		// A deeper symlink cannot escape that code folder.
		if err := os.Symlink(t.TempDir(), filepath.Join(outside, "escape")); err != nil {
			t.Fatal(err)
		}
		if _, err := h.service.validateDir(filepath.Join(link, "escape", "repo")); err == nil {
			t.Error("nested symlink escaped the code folder")
		}
	})
	t.Run("symlinked home", func(t *testing.T) {
		h := newHarness(t)
		real := h.home
		alias := filepath.Join(t.TempDir(), "home")
		if err := os.Symlink(real, alias); err != nil {
			t.Fatal(err)
		}
		h.service.Home = alias
		for _, home := range []string{alias, real} {
			if _, err := h.service.validateDir(filepath.Join(home, "code", "acme", "repo")); err != nil {
				t.Errorf("home %s: %v", home, err)
			}
		}
		got, err := h.service.DefaultDir(context.Background(), "acme", "repo")
		if err != nil || got != filepath.Join(alias, "code", "acme", "repo") {
			t.Fatalf("default = %s, %v", got, err)
		}
	})
}

func TestCloneReservesOverlappingFoldersIncludingAliases(t *testing.T) {
	h := newHarness(t)
	dir := filepath.Join(h.home, "work", "one")
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(h.home, "alias")
	if err := os.Symlink(filepath.Dir(dir), alias); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	h.service.Clone = func(context.Context, gitlocal.CloneOptions) error {
		close(started)
		<-release
		return errors.New("deliberate failure")
	}
	// Registered after running.Wait so a fatal test still releases the clone.
	t.Cleanup(func() { close(release) })
	if rec := h.do("POST", "/api/repositories/acme/one/clone", `{"dir":`+strconvQuote(dir)+`}`); rec.Code != 202 {
		t.Fatalf("first: %d %s", rec.Code, rec.Body)
	}
	<-started
	for _, target := range []string{dir, filepath.Join(dir, "child"), filepath.Dir(dir), filepath.Join(alias, "one"), strings.ToUpper(dir)} {
		// Case variants must remain below the real home for validation.
		if target == strings.ToUpper(dir) {
			target = filepath.Join(h.home, "WORK", "ONE")
		}
		rec := h.do("POST", "/api/repositories/other/two/clone", `{"dir":`+strconvQuote(target)+`}`)
		if rec.Code != 409 || decode[map[string]string](t, rec)["code"] != "dir_in_use" {
			t.Errorf("overlap %s: %d %s", target, rec.Code, rec.Body)
		}
	}
}

func TestCloneRefusesStartsAfterShutdown(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	h.service.Context = ctx
	cancel()
	h.service.Run(ctx)
	h.service.Clone = func(context.Context, gitlocal.CloneOptions) error {
		t.Error("clone started after shutdown")
		return nil
	}
	rec := h.do("POST", "/api/repositories/acme/repo/clone", `{}`)
	if rec.Code != 503 || decode[map[string]string](t, rec)["code"] != "stopping" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rows, _ := h.registry.Repositories(context.Background()); len(rows) != 0 {
		t.Errorf("picked repositories after shutdown: %+v", rows)
	}
}

func TestCloneReportsUnsupportedURLRewrite(t *testing.T) {
	h := newHarness(t)
	h.service.Clone = func(context.Context, gitlocal.CloneOptions) error { return gitlocal.ErrURLRewritten }
	if rec := h.do("POST", "/api/repositories/acme/repo/clone", `{}`); rec.Code != 202 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	state := waitFor(t, h, "acme/repo", "failed")
	if state.Code != "url_rewritten" || !strings.Contains(state.Error, "Git configuration") {
		t.Fatalf("state = %+v", state)
	}
}
