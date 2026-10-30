package repos

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/config"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("git %v: %s %v", args, out, e)
	}
}
func clone(t *testing.T, remote string) string {
	t.Helper()
	p := t.TempDir()
	git(t, p, "init")
	git(t, p, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "init")
	git(t, p, "remote", "add", "origin", remote)
	return p
}
func registry(t *testing.T) *Registry {
	t.Helper()
	r, e := Open(filepath.Join(t.TempDir(), "registry.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}
func TestClonesAndSettings(t *testing.T) {
	ctx := context.Background()
	r := registry(t)
	p := clone(t, "https://github.com/Owner/Repo.git")
	b := config.BackendCodex
	m := "model"
	s := Settings{Automatic: new(true), Backend: &b, Model: &m}
	c, e := r.Add(ctx, p, &s)
	if e != nil {
		t.Fatal(e)
	}
	wt := filepath.Join(t.TempDir(), "linked")
	git(t, p, "worktree", "add", "-b", "linked", wt)
	w, e := r.Add(ctx, wt, nil)
	if e != nil {
		t.Fatal(e)
	}
	other, e := r.Add(ctx, clone(t, "https://github.com/owner/repo"), nil)
	if e != nil {
		t.Fatal(e)
	}
	if w.ID == c.ID || other.ID == c.ID || w.CommonDir != c.CommonDir || other.CommonDir == c.CommonDir || w.Repository != "owner/repo" || !explicitlyOn(w.Settings.Automatic) {
		t.Fatalf("clones %#v %#v %#v", c, w, other)
	}
	again, e := r.Add(ctx, p, nil)
	if e != nil || !explicitlyOn(again.Settings.Automatic) {
		t.Fatalf("idempotent %#v %v", again, e)
	}
	global := config.Default()
	global.Model = "global"
	if got := s.Resolve(global); got.Backend != b || got.Model != m {
		t.Fatal(got)
	}
	if got := (Settings{}).Resolve(global); got.Model != "global" {
		t.Fatal(got)
	}
	git(t, p, "remote", "set-url", "origin", "https://github.com/owner/replaced")
	list, e := r.List(ctx)
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range list {
		if (v.ID == c.ID || v.ID == w.ID) && v.Available {
			t.Fatal("changed remote available")
		}
	}
	if e = os.RemoveAll(other.Path); e != nil {
		t.Fatal(e)
	}
	list, e = r.List(ctx)
	if e != nil || len(list) != 3 {
		t.Fatalf("missing retained %v %v", list, e)
	}
	for _, v := range list {
		if v.ID == other.ID && v.Available {
			t.Fatal("missing available")
		}
	}
	if e = r.Remove(ctx, other.Path); e != nil {
		t.Fatal(e)
	}
	if e = r.Update(ctx, c.ID, Settings{}); e != nil {
		t.Fatal(e)
	}
	if e = r.Remove(ctx, w.ID); e != nil {
		t.Fatal(e)
	}
}
func TestRejectAndFallback(t *testing.T) {
	r := registry(t)
	ctx := context.Background()
	for _, p := range []string{filepath.Join(t.TempDir(), "gone"), t.TempDir(), clone(t, "https://gitlab.com/owner/repo")} {
		if _, e := r.Add(ctx, p, nil); e == nil {
			t.Fatal("accepted", p)
		}
	}
	p := clone(t, "https://gitlab.com/owner/repo")
	git(t, p, "remote", "add", "upstream", "https://token:secret@github.com/owner/repo")
	c, e := r.Add(ctx, p, nil)
	if e != nil || c.Settings.Automatic != nil || c.Repository != "owner/repo" {
		t.Fatalf("%#v %v", c, e)
	}
	t.Setenv("GH_HOST", "git.example.com")
	enterprise, e := r.Add(ctx, clone(t, "https://git.example.com/owner/repo"), nil)
	if e != nil || enterprise.Repository == c.Repository {
		t.Fatalf("enterprise %#v %v", enterprise, e)
	}
	empty := ""
	if _, e = r.Add(ctx, p, &Settings{Model: &empty}); e == nil {
		t.Fatal("empty model accepted")
	}
}
func TestPermissionsAndConcurrentProcesses(t *testing.T) {
	if os.Getenv("BAUM_REGISTRY_CHILD") != "" {
		r, e := Open(os.Getenv("BAUM_REGISTRY_DB"))
		if e != nil {
			t.Fatal(e)
		}
		defer func() { _ = r.Close() }()
		if _, e = r.Add(context.Background(), os.Getenv("BAUM_REGISTRY_CLONE"), nil); e != nil {
			t.Fatal(e)
		}
		return
	}
	path := filepath.Join(t.TempDir(), "registry.sqlite")
	if e := os.WriteFile(path, nil, 0644); e != nil {
		t.Fatal(e)
	}
	r, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = r.Close() }()
	info, e := os.Stat(path)
	if e != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("mode %v %v", info, e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		p := clone(t, fmt.Sprintf("https://github.com/owner/repo%d", i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestPermissionsAndConcurrentProcesses$")
			cmd.Env = append(os.Environ(), "BAUM_REGISTRY_CHILD=1", "BAUM_REGISTRY_DB="+path, "BAUM_REGISTRY_CLONE="+p)
			if out, e := cmd.CombinedOutput(); e != nil {
				t.Errorf("child: %s %v", out, e)
			}
		}()
	}
	wg.Wait()
	list, e := r.List(context.Background())
	if e != nil || len(list) != 4 {
		t.Fatalf("lost writes %d %v", len(list), e)
	}
}
func TestHTTP(t *testing.T) {
	r := registry(t)
	h := r.Handler()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	p := clone(t, "https://github.com/owner/repo")
	check := func(path string) string {
		t.Helper()
		w := request(http.MethodGet, "/api/repos/check?path="+url.QueryEscape(path), "")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
		return strings.TrimSpace(w.Body.String())
	}
	if got := check(p); got != `{"valid":true,"repository":"owner/repo"}` {
		t.Fatal(got)
	}
	if got := check(t.TempDir()); !strings.Contains(got, `"valid":false`) || !strings.Contains(got, "not a git working tree") {
		t.Fatal(got)
	}
	body, _ := json.Marshal(map[string]string{"path": p})
	w := request("POST", "/api/repos", string(body))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var c Clone
	if e := json.Unmarshal(w.Body.Bytes(), &c); e != nil {
		t.Fatal(e)
	}
	if got := check(p); got != `{"valid":true,"repository":"owner/repo","registered":true}` {
		t.Fatal(got)
	}
	for _, body := range []string{`{"unknown":1}`, `{"daily_cap":-1}`, `{"backend":"no"}`, `{} {}`, `{"model":""}`, `{"automatic":null,"model":""}`} {
		if w = request("PUT", "/api/repos/"+c.ID+"/settings", body); w.Code != 400 {
			t.Fatal(body, w.Code)
		}
	}
	// An older app still sends the daily cap baum no longer has: it is
	// ignored, not refused.
	if w = request("PUT", "/api/repos/"+c.ID+"/settings", `{"automatic":true,"backend":null,"model":null,"daily_cap":0}`); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w = request(http.MethodGet, "/api/repos", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"automatic":true`) || strings.Contains(w.Body.String(), "daily_cap") {
		t.Fatal(w.Code, w.Body)
	}
	if w = request("DELETE", "/api/repos/"+c.ID, ""); w.Code != 204 {
		t.Fatal(w.Code)
	}
	if w = request("DELETE", "/api/repos/"+c.ID, ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
}

func TestSpecialDatabasePath(t *testing.T) {
	name := "with?#space "
	if runtime.GOOS == "windows" {
		name = "with# space" // Windows forbids '?' and trailing spaces in directory names.
	}
	p := filepath.Join(t.TempDir(), name, "repos.sqlite")
	r, e := Open(p)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = r.Close() }()
	if _, e = r.Add(context.Background(), clone(t, "https://github.com/owner/repo"), nil); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(p); e != nil {
		t.Fatal(e)
	}
}

func TestConcurrentSettingsPatches(t *testing.T) {
	p := clone(t, "https://github.com/owner/repo")
	path := filepath.Join(t.TempDir(), "repos.sqlite")
	r, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = r.Close() }()
	other, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = other.Close() }()
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, db := range []*Registry{r, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, e := db.AddPatch(context.Background(), p, func(s Settings) (Settings, error) {
				if i == 0 {
					s.Automatic = new(true)
				} else {
					v := "model-b"
					s.Model = &v
				}
				return s, nil
			})
			if e != nil {
				t.Error(e)
			}
		}()
	}
	close(start)
	wg.Wait()
	list, e := r.List(context.Background())
	if e != nil || len(list) != 1 || !explicitlyOn(list[0].Settings.Automatic) || list[0].Settings.Model == nil || *list[0].Settings.Model != "model-b" {
		t.Fatalf("lost settings %#v %v", list, e)
	}
}

// Each pull requests choice covers what it names; all covers pull requests
// the viewer neither authored nor was asked to review.
func TestPreferencesCoverTheChosenPullRequests(t *testing.T) {
	for _, tc := range []struct {
		choice                       string
		authored, requested, neither bool
	}{
		{"", true, true, false},
		{PullRequestsAuthored, true, false, false},
		{PullRequestsRequested, false, true, false},
		{PullRequestsAll, true, true, true},
	} {
		p := Preferences{PullRequests: tc.choice}
		if p.Validate() != nil {
			t.Fatalf("%q rejected", tc.choice)
		}
		if got := [3]bool{p.Covers(true, false), p.Covers(false, true), p.Covers(false, false)}; got != [3]bool{tc.authored, tc.requested, tc.neither} {
			t.Fatalf("%q covers %v", tc.choice, got)
		}
	}
	if (Preferences{PullRequests: "everyone"}).Validate() == nil {
		t.Fatal("unknown choice accepted")
	}
}

func TestPreferencesConcurrencyFallsBackToTheMaximum(t *testing.T) {
	for stored, want := range map[int]int{0: MaxJobConcurrency, 2: 2, MaxJobConcurrency + 1: MaxJobConcurrency, -1: MaxJobConcurrency} {
		p := Preferences{JobConcurrency: stored}
		if got := p.Concurrency(); got != want {
			t.Errorf("stored %d: %d, want %d", stored, got, want)
		}
		if err := p.Validate(); err != nil {
			t.Errorf("stored %d blocks other settings: %v", stored, err)
		}
	}
}
func TestPreferencesTimeLimitsReplaceTheFiles(t *testing.T) {
	global := config.Default()
	got := (Preferences{ReviewTimeLimit: 20, RunTimeLimit: 90}).Resolve(global).Budgets
	if time.Duration(got.ReviewTimeout) != 20*time.Minute || time.Duration(got.RunTimeout) != 90*time.Minute {
		t.Fatalf("set limits: %+v", got)
	}
	for _, p := range []Preferences{{}, {ReviewTimeLimit: -1, RunTimeLimit: MaxTimeLimit + 1}} {
		if got := p.Resolve(global).Budgets; got != global.Budgets {
			t.Errorf("%+v changed the file's budgets: %+v", p, got)
		}
		if err := p.Validate(); err != nil {
			t.Errorf("%+v blocks other settings: %v", p, err)
		}
	}
	if ValidateTimeLimit(0) != nil || ValidateTimeLimit(MaxTimeLimit) != nil || ValidateTimeLimit(MaxTimeLimit+1) == nil || ValidateTimeLimit(-1) == nil {
		t.Error("ValidateTimeLimit bounds")
	}
}

func TestPreferencesFixAgent(t *testing.T) {
	claude, codex, cursor := config.BackendClaude, config.BackendCodex, config.BackendCursorAgent
	for _, b := range []*config.Backend{nil, &claude, &codex} {
		if err := (Preferences{FixBackend: b}).Validate(); err != nil {
			t.Fatalf("%v rejected: %v", b, err)
		}
	}
	if err := (Preferences{FixBackend: &cursor}).Validate(); err == nil || err.Error() != "fixes run with Claude Code or Codex only" {
		t.Fatalf("cursor-agent: %v", err)
	}
	long := strings.Repeat("m", 257)
	if (Preferences{FixBackend: &claude, FixModel: &long}).Validate() == nil {
		t.Fatal("long fix model accepted")
	}
	short, empty := "sonnet", ""
	if (Preferences{FixModel: &short}).Validate() == nil {
		t.Fatal("a fix model without a fix agent accepted")
	}
	if err := (Preferences{FixModel: &empty}).Validate(); err != nil {
		t.Fatalf("an empty fix model without a fix agent: %v", err)
	}
	review := config.Config{Backend: config.BackendCursorAgent, Model: "review-model"}
	if got := (Preferences{}).ResolveFix(review); got.Backend != review.Backend || got.Model != review.Model {
		t.Fatalf("same as reviews: %+v", got)
	}
	if got := (Preferences{FixModel: &long}).ResolveFix(review); got.Backend != review.Backend || got.Model != review.Model {
		t.Fatalf("a model without a backend: %+v", got)
	}
	if got := (Preferences{FixBackend: &codex}).ResolveFix(review); got.Backend != config.BackendCodex || got.Model != "" {
		t.Fatalf("default model: %+v", got)
	}
	model := " gpt-fix "
	if got := (Preferences{FixBackend: &codex, FixModel: &model}).ResolveFix(review); got.Backend != config.BackendCodex || got.Model != "gpt-fix" {
		t.Fatalf("fix agent: %+v", got)
	}
}
