package clonefind

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/hookevents"
)

// home returns a symlink-free temporary home directory.
func home(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// repo creates a clone at path whose remotes are name=URL pairs.
func repo(t *testing.T, path string, remotes ...string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, path, "init", "-q")
	for i := 0; i+1 < len(remotes); i += 2 {
		git(t, path, "remote", "add", remotes[i], remotes[i+1])
	}
	return path
}

func gh(name string) string { return "https://github.com/" + name + ".git" }

type fakeEvents []hookevents.Record

func (f fakeEvents) Recent(int) ([]hookevents.Record, error) { return f, nil }

func agentEvent(dir string, at time.Time) hookevents.Record {
	e := hookevents.New(hookevents.KindAgent, "claude-code", "stop", dir, "test")
	e.Time = at
	return hookevents.Record{Received: at, Event: e}
}

// options isolates a run in a temporary home: no real session store is read.
func options(h string, events ...hookevents.Record) Options {
	o := Options{Home: h, GOOS: "linux"}
	o.Stores.CursorStorePaths = []string{filepath.Join(h, "no-cursor")}
	if events != nil {
		o.Events = fakeEvents(events)
	}
	return o
}

func find(t *testing.T, o Options) Result {
	t.Helper()
	result, err := Find(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func paths(candidates []Candidate) []string {
	var out []string
	for _, c := range candidates {
		out = append(out, c.Path)
	}
	return out
}

func TestAgentEventInSubdirectoryFindsTheClone(t *testing.T) {
	h := home(t)
	clone := repo(t, filepath.Join(h, "elsewhere", "app"), "origin", "git@github.com:Acme/App.git")
	sub := filepath.Join(clone, "pkg", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	event := agentEvent(h, at) // the hook's directory is not the agent's
	event.Event.Session = &hookevents.Session{Cwd: sub}
	got := find(t, options(h, event)).Repositories["acme/app"]
	if len(got) != 1 || got[0].Path != clone || got[0].Source != SourceAgent || !got[0].LastActivity.Equal(at) {
		t.Fatalf("candidates = %+v", got)
	}
	if got[0].CommonDir != filepath.Join(clone, ".git") {
		t.Fatalf("common dir = %s", got[0].CommonDir)
	}
}

func TestForkCloneMatchesBothNames(t *testing.T) {
	h := home(t)
	clone := repo(t, filepath.Join(h, "elsewhere", "fork"), "origin", gh("me/app"), "upstream", gh("acme/app"))
	result := find(t, options(h, agentEvent(clone, time.Now())))
	for _, name := range []string{"me/app", "acme/app"} {
		if got := paths(result.Repositories[name]); len(got) != 1 || got[0] != clone {
			t.Fatalf("%s = %v", name, got)
		}
	}
}

func TestOtherHostsAreIgnored(t *testing.T) {
	h := home(t)
	clone := repo(t, filepath.Join(h, "code", "lab"), "origin", "https://gitlab.com/acme/app.git", "mirror", "/srv/git/acme/app.git")
	if result := find(t, options(h, agentEvent(clone, time.Now()))); len(result.Repositories) != 0 {
		t.Fatalf("repositories = %+v", result.Repositories)
	}
}

func TestWorktreesCollapseToTheMainClone(t *testing.T) {
	h := home(t)
	main := repo(t, filepath.Join(h, "code", "app"), "origin", gh("acme/app"))
	git(t, main, "-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init")
	linked := filepath.Join(h, "elsewhere", "app-feature")
	git(t, main, "worktree", "add", "-q", "-b", "feature", linked)
	at := time.Date(2026, 7, 2, 9, 0, 0, 0, time.UTC)
	got := find(t, options(h, agentEvent(linked, at))).Repositories["acme/app"]
	if len(got) != 1 || got[0].Path != main || got[0].Source != SourceAgent || !got[0].LastActivity.Equal(at) {
		t.Fatalf("candidates = %+v", got)
	}
}

func TestDeletedHomeAndRootAreNeverCandidates(t *testing.T) {
	h := home(t)
	// A home directory that is itself a clone (dotfiles) is not a candidate.
	repo(t, h, "origin", gh("me/dotfiles"))
	gone := repo(t, filepath.Join(h, "code", "gone"), "origin", gh("acme/gone"))
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	result := find(t, options(h, agentEvent(h, now), agentEvent("/", now), agentEvent(filepath.Dir(h), now), agentEvent(gone, now), agentEvent("relative/dir", now)))
	if len(result.Repositories) != 0 {
		t.Fatalf("repositories = %+v", result.Repositories)
	}
}

func TestWalkFindsClonesToDepthFour(t *testing.T) {
	h := home(t)
	shallow := repo(t, filepath.Join(h, "code", "a", "b", "c", "d"), "origin", gh("acme/depth4"))
	repo(t, filepath.Join(h, "code", "a", "b", "c", "d", "e"), "origin", gh("acme/depth5"))
	repo(t, filepath.Join(h, "code", "x", "y", "z", "w", "v"), "origin", gh("acme/deep"))
	repo(t, filepath.Join(h, "code", "node_modules", "dep"), "origin", gh("acme/dep"))
	repo(t, filepath.Join(h, "code", ".hidden", "app"), "origin", gh("acme/hidden"))
	repo(t, filepath.Join(h, "code", "vendor", "lib"), "origin", gh("acme/vendored"))
	nested := repo(t, filepath.Join(h, "code", "outer"), "origin", gh("acme/outer"))
	repo(t, filepath.Join(nested, "inner"), "origin", gh("acme/inner"))
	result := find(t, options(h))
	if got := result.Repositories["acme/depth4"]; len(got) != 1 || got[0].Path != shallow || got[0].Source != SourceScan || !got[0].LastActivity.IsZero() {
		t.Fatalf("depth4 = %+v", got)
	}
	for _, name := range []string{"acme/depth5", "acme/deep", "acme/dep", "acme/hidden", "acme/vendored", "acme/inner"} {
		if got := result.Repositories[name]; got != nil {
			t.Errorf("%s found: %+v", name, got)
		}
	}
	if _, ok := result.Repositories["acme/outer"]; !ok {
		t.Error("a clone that holds another was not found")
	}
}

func TestWalkNeverReadsPersonalFolders(t *testing.T) {
	h := home(t)
	for _, name := range []string{"Documents", "Desktop", "Downloads", "Dropbox", "Google Drive"} {
		repo(t, filepath.Join(h, name, "app"), "origin", gh("acme/"+name))
	}
	// Asked to walk them, or reached through a symlink, they stay unread.
	o := options(h)
	o.GOOS = "darwin"
	o.ExtraRoots = []string{filepath.Join(h, "Documents"), filepath.Join(h, "Dropbox", "app"), h, "/", filepath.Dir(h)}
	if err := os.MkdirAll(filepath.Join(h, "code"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(h, "Documents"), filepath.Join(h, "code", "docs")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(h, "Downloads"), filepath.Join(h, "src")); err != nil {
		t.Fatal(err)
	}
	if result := find(t, o); len(result.Repositories) != 0 {
		t.Fatalf("repositories = %+v", result.Repositories)
	}
}

func TestSiblingsOfAgentClonesAreWalked(t *testing.T) {
	h := home(t)
	worked := repo(t, filepath.Join(h, "work", "a"), "origin", gh("acme/a"))
	sibling := repo(t, filepath.Join(h, "work", "b"), "origin", gh("acme/b"))
	// An agent worked in Documents: on macOS it is protected, so nothing there is read.
	repo(t, filepath.Join(h, "Documents", "x"), "origin", gh("acme/x"))
	repo(t, filepath.Join(h, "Documents", "y"), "origin", gh("acme/y"))
	// Nor are the siblings of a clone directly in the home directory.
	repo(t, filepath.Join(h, "home-clone"), "origin", gh("acme/home-clone"))
	repo(t, filepath.Join(h, "home-sibling"), "origin", gh("acme/home-sibling"))
	now := time.Now()
	o := options(h, agentEvent(worked, now), agentEvent(filepath.Join(h, "Documents", "x"), now), agentEvent(filepath.Join(h, "home-clone"), now))
	o.GOOS = "darwin"
	result := find(t, o)
	if got := result.Repositories["acme/b"]; len(got) != 1 || got[0].Path != sibling || got[0].Source != SourceScan {
		t.Fatalf("sibling = %+v", got)
	}
	if got := result.Repositories["acme/x"]; got != nil {
		t.Fatalf("documents clone on macOS = %+v", got)
	}
	for _, name := range []string{"acme/y", "acme/home-sibling"} {
		if result.Repositories[name] != nil {
			t.Errorf("%s found", name)
		}
	}
}

func TestBestCandidateIsTheMostRecentlyUsed(t *testing.T) {
	h := home(t)
	old := repo(t, filepath.Join(h, "one", "app"), "origin", gh("acme/app"))
	recent := repo(t, filepath.Join(h, "two", "app"), "origin", gh("acme/app"))
	scanZ := repo(t, filepath.Join(h, "code", "zzz"), "origin", gh("acme/app"))
	scanA := repo(t, filepath.Join(h, "code", "aaa"), "origin", gh("acme/app"))
	now := time.Now()
	got := find(t, options(h, agentEvent(old, now.Add(-time.Hour)), agentEvent(recent, now))).Repositories["acme/app"]
	want := []string{recent, old, scanA, scanZ}
	if fmt.Sprint(paths(got)) != fmt.Sprint(want) {
		t.Fatalf("order = %v, want %v", paths(got), want)
	}
	// Without agent activity the order is by path, every time.
	for range 3 {
		got = find(t, options(h)).Repositories["acme/app"]
		if fmt.Sprint(paths(got)) != fmt.Sprint([]string{scanA, scanZ}) {
			t.Fatalf("scan-only order = %v", paths(got))
		}
	}
}

func TestSessionStoresPointToClones(t *testing.T) {
	h := home(t)
	claude := repo(t, filepath.Join(h, "elsewhere", "claude-app"), "origin", gh("acme/claude-app"))
	codex := repo(t, filepath.Join(h, "elsewhere", "codex-app"), "origin", gh("acme/codex-app"))
	child := repo(t, filepath.Join(h, "unscanned", "child-app"), "origin", gh("acme/child-app"))

	project := filepath.Join(h, ".claude", "projects", "p")
	rollouts := filepath.Join(h, ".codex", "sessions", "2026", "07", "01")
	for _, dir := range []string{project, rollouts} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(project, "s1.jsonl"), fmt.Sprintf(`{"cwd":%q,"type":"user","sessionId":"s1"}`+"\n", filepath.Join(claude, "src")))
	write(filepath.Join(rollouts, "rollout-2026-07-01T10-00-00-c1.jsonl"),
		fmt.Sprintf(`{"timestamp":"2026-07-01T10:00:00.000Z","type":"session_meta","payload":{"id":"c1","cwd":%q,"cli_version":"0.69.0","model_provider":"openai"}}`+"\n", codex))
	write(filepath.Join(rollouts, "rollout-2026-07-01T11-00-00-c2.jsonl"),
		fmt.Sprintf(`{"timestamp":"2026-07-01T11:00:00.000Z","type":"session_meta","payload":{"id":"c2","session_id":"c1","cwd":%q,"parent_thread_id":"c1","source":{"subagent":{"thread_spawn":{"parent_thread_id":"c1","depth":1,"agent_path":"worker"}}}}}`+"\n", child))
	if err := os.MkdirAll(filepath.Join(claude, "src"), 0o755); err != nil {
		t.Fatal(err)
	}

	result := find(t, options(h))
	if got := result.Repositories["acme/claude-app"]; len(got) != 1 || got[0].Path != claude || got[0].Source != SourceAgent || got[0].LastActivity.IsZero() {
		t.Fatalf("claude = %+v", got)
	}
	if got := result.Repositories["acme/codex-app"]; len(got) != 1 || got[0].Path != codex || got[0].Source != SourceAgent {
		t.Fatalf("codex = %+v", got)
	}
	if got := result.Repositories["acme/child-app"]; got != nil {
		t.Fatalf("a sub-agent session named %+v", got)
	}
}

func TestProbeBudgetAndCancellation(t *testing.T) {
	h := home(t)
	for i := range 5 {
		repo(t, filepath.Join(h, "code", fmt.Sprintf("r%d", i)), "origin", gh(fmt.Sprintf("acme/r%d", i)))
	}
	calls := 0
	o := options(h)
	o.MaxProbes = 3
	o.Probe = func(ctx context.Context, dir string) (gitlocal.Light, error) {
		calls++
		return gitlocal.Probe(ctx, dir, time.Second)
	}
	if result := find(t, o); calls != 3 || len(result.Repositories) != 3 {
		t.Fatalf("probes = %d, repositories = %d", calls, len(result.Repositories))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Find(ctx, options(h)); err == nil {
		t.Fatal("a cancelled run reported success")
	}
}

func TestDocumentsAreWalkedOnLinuxButNeedOptInOnMacOS(t *testing.T) {
	h := home(t)
	docs := repo(t, filepath.Join(h, "Documents", "proj", "app"), "origin", gh("acme/app"))
	repo(t, filepath.Join(h, "Desktop", "x"), "origin", gh("acme/desktop"))
	repo(t, filepath.Join(h, "Downloads", "x"), "origin", gh("acme/downloads"))
	for _, tc := range []struct {
		goos    string
		include bool
		want    bool
	}{{"linux", false, true}, {"windows", false, true}, {"darwin", false, false}, {"darwin", true, true}} {
		o := options(h)
		o.GOOS, o.IncludeDocuments = tc.goos, tc.include
		result := find(t, o)
		got := result.Repositories["acme/app"]
		if tc.want != (len(got) == 1 && got[0].Path == docs) {
			t.Errorf("%s include=%v: %+v", tc.goos, tc.include, got)
		}
		if result.Repositories["acme/desktop"] != nil || result.Repositories["acme/downloads"] != nil {
			t.Errorf("%s include=%v walked Desktop or Downloads", tc.goos, tc.include)
		}
	}
}
