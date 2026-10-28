package decisionlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/livesessions"
	"github.com/baum-ai/baum/internal/locallink/linktest"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/sessions"
	"github.com/baum-ai/baum/sessions/vendors"
)

// A store an earlier release made gets the ledger's stage and item columns;
// its attempts are capture's.
func TestOpenMigratesTheLedger(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "decisions")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "decisions.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE capture_attempt(
  id INTEGER PRIMARY KEY AUTOINCREMENT, agent TEXT NOT NULL, session_id TEXT NOT NULL, repository TEXT NOT NULL DEFAULT '',
  backend TEXT NOT NULL, model TEXT NOT NULL, mode TEXT NOT NULL, started_at INTEGER NOT NULL, settled_at INTEGER,
  state TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '', reserved_usd REAL NOT NULL, cost_usd REAL,
  input_tokens INTEGER NOT NULL DEFAULT 0, cached_input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0);
INSERT INTO capture_attempt(agent, session_id, backend, model, mode, started_at, state, reserved_usd) VALUES ('claude-code','s1','claude','m','full',1,'succeeded',0.1)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	for range 2 { // and again, once migrated
		s, err := Open(path, OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Begin(context.Background(), Attempt{Backend: "claude", Model: "m", Mode: "full", Stage: "assign", Item: "pr:7"}); err != nil {
			t.Fatal(err)
		}
		_ = s.Close()
	}
	s, err := Open(path, OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var stages []string
	rows, err := s.db.Query(`SELECT stage || ':' || COALESCE(item, '') FROM capture_attempt ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v string
		_ = rows.Scan(&v)
		stages = append(stages, v)
	}
	_ = rows.Close()
	if want := []string{"capture:", "assign:pr:7", "assign:pr:7"}; !slices.Equal(stages, want) {
		t.Fatalf("stages = %v, want %v", stages, want)
	}
}

// A repository gets one current version, made when it is first asked for.
func TestLiveVersion(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, &clock{t0})
	first, err := s.LiveVersion(ctx, "o/r")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.LiveVersion(ctx, "o/r")
	other, _ := s.LiveVersion(ctx, "o/s")
	if err != nil || first == 0 || again != first || other == first {
		t.Fatalf("versions %d %d %d, %v", first, again, other, err)
	}
	if _, err := s.db.Exec(`INSERT INTO feature_version(repository, state, created_at) VALUES ('o/r','current',0)`); err == nil {
		t.Fatal("a second current version must be refused")
	}
	if _, err := s.db.Exec(`INSERT INTO feature_version(repository, state, created_at) VALUES ('o/r','building',0)`); err != nil {
		t.Fatal(err)
	}
}

// fakeReader serves the pull request list of a repository, answering 304 to
// its ETag.
type fakeReader struct {
	pulls []map[string]any
	// version is the listing's ETag; changing the pulls changes it.
	version int
}

func (r *fakeReader) set(pulls ...map[string]any) {
	r.pulls = pulls
	r.version++
}

func (r *fakeReader) Read(_ context.Context, path, etag string) (int, http.Header, []byte, error) {
	current := fmt.Sprintf(`"e%d"`, r.version)
	if etag == current {
		return http.StatusNotModified, http.Header{}, nil, nil
	}
	if !strings.HasSuffix(path, "page=1") {
		return http.StatusOK, http.Header{}, []byte("[]"), nil
	}
	body, _ := json.Marshal(r.pulls)
	return http.StatusOK, http.Header{"Etag": []string{current}}, body, nil
}

func (s *Store) pathsAt(t *testing.T, number int) (head string, paths sql.NullString) {
	t.Helper()
	if err := s.db.QueryRow(`SELECT paths_head_sha, paths_json FROM pull_request WHERE number=?`, number).Scan(&head, &paths); err != nil {
		t.Fatal(err)
	}
	return head, paths
}

// An empty first sync still moves the cursor to the start of its window, so
// the next one does not list the repository's whole history.
func TestFeatureSyncEmptyFirstListing(t *testing.T) {
	c := &clock{t0}
	s := openStore(t, c)
	reader := &fakeReader{}
	reader.set()
	w := &FeatureWorker{Store: s, Now: c.Now, Window: 24 * time.Hour,
		GitHub: func(context.Context, github.Repository) (github.Reader, error) { return reader, nil }}
	if _, err := w.Sync(context.Background(), "acme/widgets", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	state, err := s.Activate(context.Background(), "acme/widgets")
	if err != nil || !state.Cursor.Equal(t0.Add(-24*time.Hour)) || state.ETag != `"e1"` {
		t.Fatalf("state = %+v, %v", state, err)
	}
}

func pullJSON(number int, head, base string) map[string]any {
	return map[string]any{
		"number": number, "title": fmt.Sprintf("PR %d", number), "state": "open",
		"created_at": "2026-07-01T08:00:00Z", "updated_at": "2026-07-01T11:00:00Z",
		"user": map[string]any{"login": "grace", "type": "User"},
		"head": map[string]any{"ref": "feature", "sha": head}, "base": map[string]any{"ref": "main", "sha": base},
	}
}

// Sync records the pull requests, fetches a missing head and reads paths and
// commits; Index links the commits to the session that made them, and the
// links attribute the captured session to its pull request's feature.
func TestFeatureSyncAndIndex(t *testing.T) {
	ctx := context.Background()
	f := linktest.New(t)
	// The clone's origin names the repository and leads to a local bare
	// copy holding pull request 6's head, which the clone lacks.
	remote := filepath.Join(t.TempDir(), "remote.git")
	linktest.Git(t, f.Repo, "clone", "-q", "--bare", f.Repo, remote)
	linktest.Git(t, remote, "update-ref", "refs/pull/5/head", f.Head)
	// Pull request 7 has no ref on the remote: its fetches fail although the
	// remote answers, so it is counted as failing rather than blocking.
	work := filepath.Join(t.TempDir(), "work")
	linktest.Git(t, f.Repo, "clone", "-q", remote, work)
	linktest.Git(t, work, "checkout", "-q", "-b", "six", f.Base)
	if err := os.WriteFile(filepath.Join(work, "d.txt"), []byte("d\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	linktest.Git(t, work, "add", "d.txt")
	linktest.Git(t, work, "commit", "-q", "-m", "Six")
	six := linktest.Git(t, work, "rev-parse", "HEAD")
	linktest.Git(t, work, "push", "-q", "origin", "HEAD:refs/pull/6/head")
	linktest.Git(t, f.Repo, "remote", "set-url", "origin", "https://github.com/acme/widgets.git")
	linktest.Git(t, f.Repo, "config", "url."+remote+".insteadOf", "https://github.com/acme/widgets.git")

	c := &clock{time.Date(2026, 7, 2, 9, 0, 0, 0, time.UTC)}
	s := openStore(t, c)
	// Pull request 7's listed head is nowhere; 9 is a bot's and 10 was closed
	// unmerged, so neither is read.
	missing := strings.Repeat("e", 40)
	bot, closed := pullJSON(9, f.Head, f.Base), pullJSON(10, f.Head, f.Base)
	bot["user"] = map[string]any{"login": "dependabot[bot]", "type": "Bot"}
	closed["state"] = "closed"
	reader := &fakeReader{}
	reader.set(pullJSON(5, f.Head, f.Base), pullJSON(6, six, f.Base), pullJSON(7, missing, f.Base), bot, closed)
	on := featureRegistry{prefs: repos.Preferences{Automatic: true}, repositories: []repos.Repository{
		{Repository: linktest.Repository, Clones: []repos.Clone{{Path: f.Repo, Available: true}}}}}
	w := &FeatureWorker{Store: s, Registry: on, Now: c.Now, Sessions: f.Store,
		GitHub: func(context.Context, github.Repository) (github.Reader, error) { return reader, nil }}

	// One pass reads the others and links their commits although pull
	// request 7 cannot be read.
	w.RunOnce(ctx)
	if head, paths := s.pathsAt(t, 7); head != "" || paths.Valid {
		t.Fatalf("pull request 7 = %s %v after one failure", head, paths)
	}
	for _, n := range []int{9, 10} {
		if head, _ := s.pathsAt(t, n); head != "" {
			t.Fatalf("pull request %d was read", n)
		}
	}
	if got := linktest.Git(t, f.Repo, "rev-parse", "refs/baum/pull/6/head"); got != six {
		t.Fatalf("pull request 6's head was not fetched: %s", got)
	}
	if head, paths := s.pathsAt(t, 5); head != f.Head || paths.String != `["b.txt","c.txt"]` {
		t.Fatalf("pull request 5 = %s %v", head, paths)
	}
	var linked int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM session_commit`).Scan(&linked); err != nil || linked != 1 {
		t.Fatalf("%d links after the pass, %v", linked, err)
	}

	// The pass's full re-link had pull request 7 read again at once; then it
	// waits FeatureQuiet, and its paths are unknown after its third failure.
	report, err := w.Sync(ctx, linktest.Repository, f.Repo)
	if err != nil || !report.NotModified || report.Calls != 1 || report.PathsFailed != 1 || report.PathsUnknown != 0 {
		t.Fatalf("second sync = %+v, %v", report, err)
	}
	if report, err = w.Sync(ctx, linktest.Repository, f.Repo); err != nil || report.PathsFailed != 0 {
		t.Fatalf("retried too soon: %+v, %v", report, err)
	}
	c.now = c.now.Add(FeatureQuiet)
	if report, err = w.Sync(ctx, linktest.Repository, f.Repo); err != nil || report.PathsFailed != 1 || report.PathsUnknown != 1 {
		t.Fatalf("third try = %+v, %v", report, err)
	}
	if head, paths := s.pathsAt(t, 7); head != missing || paths.Valid {
		t.Fatalf("pull request 7 = %s %v, want its head with unknown paths", head, paths)
	}

	// With the remote out of reach, the pass fails without counting a
	// failure against any pull request.
	linktest.Git(t, f.Repo, "config", "--unset", "url."+remote+".insteadOf")
	linktest.Git(t, f.Repo, "config", "url."+filepath.Join(t.TempDir(), "gone")+".insteadOf", "https://github.com/acme/widgets.git")
	other := strings.Repeat("d", 40)
	reader.set(pullJSON(5, f.Head, f.Base), pullJSON(6, six, f.Base), pullJSON(8, other, f.Base))
	if report, err = w.Sync(ctx, linktest.Repository, f.Repo); err == nil || report.PathsFailed != 0 {
		t.Fatalf("sync without the remote = %+v, %v", report, err)
	}
	var failures int
	if err := s.db.QueryRow(`SELECT paths_failures FROM pull_request WHERE number=8`).Scan(&failures); err != nil || failures != 0 {
		t.Fatalf("failures = %d, %v", failures, err)
	}
	if head, _ := s.pathsAt(t, 8); head != "" {
		t.Fatalf("a failed fetch recorded head %s", head)
	}

	// Turned off during a pass, the repository's pull requests are left
	// and its commits not linked; nothing is fetched (the remote is still
	// gone, so a fetch would fail the pass).
	reader.set(pullJSON(5, f.Head, f.Base), pullJSON(6, six, f.Base), pullJSON(8, other, f.Base), pullJSON(12, f.Head, f.Base))
	c.now = c.now.Add(FeatureQuiet)
	linkedAt := w.windowLinked[linktest.Repository]
	w.Registry = turnedOff{on}
	w.RunOnce(ctx)
	if head, _ := s.pathsAt(t, 12); head != "" || !w.windowLinked[linktest.Repository].Equal(linkedAt) {
		t.Fatalf("a repository turned off was read (head %q) or linked", head)
	}
	w.Registry = nil

	indexed, err := w.Index(ctx, linktest.Repository, f.Repo, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if !indexed.Full || indexed.Commits != 3 || indexed.Links != 1 || indexed.Sessions != 1 {
		t.Fatalf("index = %+v", indexed)
	}
	// The daily re-link has unknown paths read again.
	if head, _ := s.pathsAt(t, 7); head != "" {
		t.Fatal("unknown paths are not retried")
	}

	// The session's decisions go to pull request 5's feature once the
	// session was captured.
	version, err := s.LiveVersion(ctx, linktest.Repository)
	if err != nil {
		t.Fatal(err)
	}
	for _, insert := range []string{
		`INSERT INTO feature(id, version_id, repository, changed_at, created_at, updated_at) VALUES ('F', ?, ?, 0, 0, 0)`,
		`INSERT INTO feature_pull(version_id, repository, number, feature_id, assigned_by, assigned_at) VALUES (?, ?, 5, 'F', 'assigner', 0)`,
	} {
		if _, err := s.db.Exec(insert, version, linktest.Repository); err != nil {
			t.Fatal(err)
		}
	}
	links, err := s.SessionLinks(ctx, linktest.Repository, "claude-code", linktest.SessionID)
	if err != nil || len(links.PullRequests) != 0 {
		t.Fatalf("an uncaptured session has no links: %+v, %v", links, err)
	}
	if err := s.Skip(ctx, Meta{Agent: "claude-code", ID: linktest.SessionID, Repository: linktest.Repository}, "test"); err != nil {
		t.Fatal(err)
	}
	links, err = s.SessionLinks(ctx, linktest.Repository, "claude-code", linktest.SessionID)
	if err != nil || len(links.PullRequests) != 1 || links.Features()[0] != "F" {
		t.Fatalf("links = %+v, %v", links, err)
	}
	if pull := links.PullRequests[0]; pull.Number != 5 || !slices.Equal(pull.Commits, f.Commits[:1]) || !slices.Equal(pull.Paths, []string{"b.txt", "c.txt"}) {
		t.Fatalf("pull = %+v", pull)
	}

	// Linking a commit again replaces its links.
	if err := s.ReplaceSessionCommits(ctx, linktest.Repository, f.Commits[:1], nil, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if links, err = s.SessionLinks(ctx, linktest.Repository, "claude-code", linktest.SessionID); err != nil || len(links.PullRequests) != 0 {
		t.Fatalf("links after replace = %+v, %v", links, err)
	}
	// Within the day, a tick with nothing new links nothing.
	if indexed, err = w.Index(ctx, linktest.Repository, f.Repo, nil, false); err != nil || indexed.Full || indexed.Commits != 0 {
		t.Fatalf("index = %+v, %v", indexed, err)
	}
}

type featureRegistry struct {
	prefs        repos.Preferences
	repositories []repos.Repository
	// listed is told each time the repositories are read.
	listed chan struct{}
}

func (r featureRegistry) Preferences(context.Context) (repos.Preferences, error) { return r.prefs, nil }
func (r featureRegistry) Repositories(context.Context) ([]repos.Repository, error) {
	if r.listed != nil {
		r.listed <- struct{}{}
	}
	return r.repositories, nil
}

// turnedOff lists the repository but reads it as off: it was turned off
// after the pass began.
type turnedOff struct{ featureRegistry }

func (turnedOff) RepositorySettings(context.Context, string) (repos.Settings, bool, error) {
	off := false
	return repos.Settings{Decisions: &off}, true, nil
}

func (r featureRegistry) RepositorySettings(_ context.Context, repository string) (repos.Settings, bool, error) {
	for _, each := range r.repositories {
		if each.Repository == repository {
			return each.Settings, true, nil
		}
	}
	return repos.Settings{}, false, nil
}

// A repository whose decisions are not captured is never synced, and Run
// returns once its context ends. A failed repository waits before it is
// tried again.
func TestFeatureWorkerGateAndShutdown(t *testing.T) {
	off := false
	for name, registry := range map[string]featureRegistry{
		"on":             {prefs: repos.Preferences{Automatic: true}},
		"paused":         {prefs: repos.Preferences{Automatic: false}},
		"decisions off":  {prefs: repos.Preferences{Automatic: true, Decisions: repos.DecisionPreferences{Enabled: &off}}},
		"repository off": {prefs: repos.Preferences{Automatic: true}, repositories: []repos.Repository{{Settings: repos.Settings{Decisions: &off}}}},
	} {
		t.Run(name, func(t *testing.T) {
			if len(registry.repositories) == 0 {
				registry.repositories = []repos.Repository{{}}
			}
			registry.repositories[0].Repository = "acme/widgets"
			registry.repositories[0].Clones = []repos.Clone{{Path: t.TempDir(), Available: true}}
			registry.listed = make(chan struct{}, 16)
			calls := 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			// A repository turned on is reached, and the daemon stops in the
			// middle of its pass.
			w := &FeatureWorker{Store: openStore(t, &clock{t0}), Registry: registry, Tick: time.Hour,
				GitHub: func(ctx context.Context, _ github.Repository) (github.Reader, error) {
					calls++
					cancel()
					return nil, ctx.Err()
				}}
			done := make(chan struct{})
			go func() {
				w.Run(ctx)
				close(done)
			}()
			if name != "on" {
				// The first pass reads the repositories; a wake starts a
				// second, so the first has finished when it reads them again.
				<-registry.listed
				w.Wake()
				<-registry.listed
				cancel()
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("Run did not return")
			}
			if want := map[bool]int{true: 1}[name == "on"]; calls != want {
				t.Fatalf("GitHub was called %d times, want %d", calls, want)
			}
		})
	}
}

// A commit in two pull requests links the session to both; another
// repository's rows never count, and a pull request's paths include those
// it was assigned with.
func TestSessionLinksAcrossPullRequests(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, &clock{t0})
	if err := s.Skip(ctx, Meta{Agent: "codex", ID: "s1", Repository: "o/r"}, "test"); err != nil {
		t.Fatal(err)
	}
	for _, repository := range []string{"o/r", "o/other"} {
		version, err := s.LiveVersion(ctx, repository)
		if err != nil {
			t.Fatal(err)
		}
		for i, number := range []int{10, 11} {
			feature := fmt.Sprintf("%s-F%d", repository, i)
			for _, stmt := range []struct {
				query string
				args  []any
			}{
				{`INSERT INTO pull_request(repository, number, title, state, head_sha, base_sha, created_at, updated_at, paths_head_sha, paths_json)
VALUES (?,?,'t','open','h','b',?,0,'h',?)`, []any{repository, number, number, fmt.Sprintf(`["p%d"]`, number)}},
				{`INSERT INTO pull_commit(repository, number, sha, committed_at) VALUES (?,?,'X',0)`, []any{repository, number}},
				{`INSERT INTO feature(id, version_id, repository, changed_at, created_at, updated_at) VALUES (?,?,?,0,0,0)`, []any{feature, version, repository}},
				{`INSERT INTO feature_pull(version_id, repository, number, feature_id, assigned_by, assigned_at, assigned_paths_json)
VALUES (?,?,?,?,'assigner',0,'["old"]')`, []any{version, repository, number, feature}},
			} {
				if _, err := s.db.Exec(stmt.query, stmt.args...); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := s.ReplaceSessionCommits(ctx, repository, []string{"X"}, []SessionCommit{
			{Agent: "codex", SessionID: "s1", SHA: "X", Method: "files_overlap", Confidence: 0.5},
			{Agent: "codex", SessionID: "s1", SHA: "X", Method: "commit_ref", Confidence: 0.9},
			{Agent: "codex", SessionID: "s1", SHA: "X", Method: "start_commit", Confidence: 0.7},
		}, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	var method string
	if err := s.db.QueryRow(`SELECT method FROM session_commit WHERE repository='o/r'`).Scan(&method); err != nil || method != "commit_ref" {
		t.Fatalf("kept %q, %v; want the strongest link", method, err)
	}
	links, err := s.SessionLinks(ctx, "o/r", "codex", "s1")
	if err != nil || len(links.PullRequests) != 2 {
		t.Fatalf("links = %+v, %v", links, err)
	}
	for i, pull := range links.PullRequests {
		if pull.FeatureID != fmt.Sprintf("o/r-F%d", i) || !slices.Equal(pull.Commits, []string{"X"}) ||
			!slices.Equal(pull.Paths, []string{"old", fmt.Sprintf("p%d", pull.Number)}) {
			t.Fatalf("pull = %+v", pull)
		}
	}
}

// A linked session is keyed as capture keys it: a Codex session's agent and
// id are the ones its live transcript is located by.
func TestLinkedSessionsKeyAsCapture(t *testing.T) {
	if agents["claude_code"] != livesessions.AgentClaudeCode || agents["codex"] != livesessions.AgentCodex || agents["cursor"] != livesessions.AgentCursor {
		t.Fatalf("agents = %v", agents)
	}
	home, repo := t.TempDir(), t.TempDir()
	linktest.Git(t, repo, "init", "-q")
	const id = "019a0000-0000-7000-8000-000000000001"
	rollout := filepath.Join(home, ".codex", "sessions", "2026", "07", "01", "rollout-2026-07-01T10-00-00-"+id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rollout, []byte(fmt.Sprintf(`{"timestamp":"2026-07-01T10:00:00.000Z","type":"session_meta","payload":{"id":%q,"cwd":%q,"cli_version":"0.69.0","model_provider":"openai"}}
{"timestamp":"2026-07-01T10:00:05.000Z","type":"event_msg","payload":{"type":"user_message","message":"hello"}}
`, id, repo)), 0o644); err != nil {
		t.Fatal(err)
	}
	found, _, err := sessions.Load(context.Background(), repo, sessions.Options{
		Sources: []vendors.Slug{vendors.Codex}, CodexSessionsDir: filepath.Join(home, ".codex", "sessions"),
	})
	if err != nil || len(found) != 1 {
		t.Fatalf("found = %+v, %v", found, err)
	}
	agent := agents[found[0].Vendor]
	if agent != livesessions.AgentCodex || found[0].ID != id {
		t.Fatalf("session %s %s", found[0].Vendor, found[0].ID)
	}
	if _, err := livesessions.Locate(home, "", agent, found[0].ID, ""); err != nil {
		t.Fatalf("capture cannot locate the linked session: %v", err)
	}
}

// manyReader lists count pull requests by bots, newest first, a minute apart
// from updated down.
type manyReader struct {
	count   int
	updated time.Time
}

func (r manyReader) Read(_ context.Context, path, _ string) (int, http.Header, []byte, error) {
	parsed, err := url.Parse(path)
	if err != nil {
		return 0, nil, nil, err
	}
	perPage, _ := strconv.Atoi(parsed.Query().Get("per_page"))
	page, _ := strconv.Atoi(parsed.Query().Get("page"))
	rows := []map[string]any{}
	for n := (page-1)*perPage + 1; n <= min(page*perPage, r.count); n++ {
		row := pullJSON(n, strings.Repeat("a", 40), strings.Repeat("b", 40))
		row["updated_at"] = r.updated.Add(-time.Duration(n) * time.Minute).Format(time.RFC3339)
		row["user"] = map[string]any{"login": "bot", "type": "Bot"}
		rows = append(rows, row)
	}
	body, _ := json.Marshal(rows)
	return http.StatusOK, http.Header{"Etag": []string{`"many"`}}, body, nil
}

// A listing cut at its bound skips the older tail: the cursor moves to the
// newest update, so the next sync does not list them all again.
func TestFeatureSyncTruncatedListing(t *testing.T) {
	defer github.SetMaxPulls(128)()
	c := &clock{t0}
	s := openStore(t, c)
	reader := manyReader{count: 300, updated: t0}
	w := &FeatureWorker{Store: s, Now: c.Now, Window: 30 * 24 * time.Hour,
		GitHub: func(context.Context, github.Repository) (github.Reader, error) { return reader, nil }}
	report, err := w.Sync(context.Background(), "acme/widgets", t.TempDir())
	if err != nil || !report.Truncated || report.Listed != 128 {
		t.Fatalf("report = truncated %t, %d listed, %v", report.Truncated, report.Listed, err)
	}
	state, err := s.Activate(context.Background(), "acme/widgets")
	if err != nil || !state.Cursor.Equal(t0.Add(-time.Minute)) {
		t.Fatalf("cursor = %v, %v", state.Cursor, err)
	}
}

// A new head starts a pull request's failure count again; the third failure
// at a head leaves its paths unknown, and the daily re-link has it, and every
// other failed one, read again at once.
func TestPathFailuresFollowTheHead(t *testing.T) {
	ctx := context.Background()
	c := &clock{t0}
	s := openStore(t, c)
	list := func(number int, head string) {
		t.Helper()
		pull := github.ListedPull{Number: number, State: "open", HeadSHA: head, BaseSHA: "b", UpdatedAt: t0}
		if err := s.SavePulls(ctx, "o/r", []github.ListedPull{pull}, "", t0); err != nil {
			t.Fatal(err)
		}
	}
	state := func(number int) (head string, failures int, retry sql.NullInt64) {
		t.Helper()
		if err := s.db.QueryRow(`SELECT paths_head_sha, paths_failures, paths_retry_at FROM pull_request WHERE number=?`, number).
			Scan(&head, &failures, &retry); err != nil {
			t.Fatal(err)
		}
		return head, failures, retry
	}
	fail := func(number int, head string, wantUnknown bool) {
		t.Helper()
		if unknown, err := s.PathsFailed(ctx, "o/r", number, head); err != nil || unknown != wantUnknown {
			t.Fatalf("unknown %t, %v", unknown, err)
		}
	}
	if _, err := s.Activate(ctx, "o/r"); err != nil {
		t.Fatal(err)
	}
	list(1, "h1")
	list(2, "x1")
	fail(1, "h1", false)
	fail(1, "h1", false)
	list(1, "h1")
	if _, failures, retry := state(1); failures != 2 || !retry.Valid {
		t.Fatalf("the same head kept %d failures, retry %v", failures, retry)
	}
	list(1, "h2")
	if _, failures, retry := state(1); failures != 0 || retry.Valid {
		t.Fatalf("a new head kept %d failures, retry %v", failures, retry)
	}
	for try := 1; try <= FeaturePathTries; try++ {
		fail(1, "h2", try == FeaturePathTries)
	}
	if head, _, _ := state(1); head != "h2" {
		t.Fatalf("unknown at head %q", head)
	}
	fail(2, "x1", false)
	if err := s.RetryUnknownPaths(ctx, "o/r"); err != nil {
		t.Fatal(err)
	}
	if head, failures, retry := state(1); head != "" || failures != 0 || retry.Valid {
		t.Fatalf("unknown pull request after the re-link: %q %d %v", head, failures, retry)
	}
	if _, failures, retry := state(2); failures != 1 || retry.Valid {
		t.Fatalf("failed pull request after the re-link: %d %v", failures, retry)
	}
}

// A pull request's recorded commits are linked again at the head they were
// recorded at while a newer head cannot be read.
func TestRelinkReadsTheRecordedHead(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, &clock{t0})
	if _, err := s.Activate(ctx, "o/r"); err != nil {
		t.Fatal(err)
	}
	save := func(head string) {
		t.Helper()
		pull := github.ListedPull{Number: 1, State: "open", HeadSHA: head, BaseSHA: "b", UpdatedAt: t0}
		if err := s.SavePulls(ctx, "o/r", []github.ListedPull{pull}, "", t0); err != nil {
			t.Fatal(err)
		}
	}
	save("h1")
	if err := s.SetPullPaths(ctx, "o/r", 1, "h1", []string{"a"}, []PullCommit{{SHA: "c1", CommittedAt: t0}}); err != nil {
		t.Fatal(err)
	}
	save("h2")
	pulls, err := s.PullsWithCommitsSince(ctx, "o/r", t0.Add(-time.Hour))
	if err != nil || len(pulls) != 1 || pulls[0].HeadSHA != "h1" {
		t.Fatalf("pulls = %+v, %v", pulls, err)
	}
}

// A pull request retargeted to another base, its head unchanged, has its
// commits read again.
func TestRetargetedPullIsReadAgain(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, &clock{t0})
	if _, err := s.Activate(ctx, "o/r"); err != nil {
		t.Fatal(err)
	}
	save := func(base string) {
		t.Helper()
		pull := github.ListedPull{Number: 1, State: "open", HeadSHA: "h", BaseSHA: base, UpdatedAt: t0}
		if err := s.SavePulls(ctx, "o/r", []github.ListedPull{pull}, "", t0); err != nil {
			t.Fatal(err)
		}
	}
	due := func() int {
		t.Helper()
		pulls, err := s.PullsWithoutPaths(ctx, "o/r")
		if err != nil {
			t.Fatal(err)
		}
		return len(pulls)
	}
	save("b1")
	if err := s.SetPullPaths(ctx, "o/r", 1, "h", []string{"a"}, nil); err != nil {
		t.Fatal(err)
	}
	save("b1")
	if due() != 0 {
		t.Fatal("an unchanged pull request is read again")
	}
	save("b2")
	if due() != 1 {
		t.Fatal("a retargeted pull request is not read again")
	}
}

// A repository turned off after its sync has no sessions read, even when
// the sync had nothing to do.
func TestFeatureGateBeforeLinking(t *testing.T) {
	c := &clock{t0}
	s := openStore(t, c)
	reader := &fakeReader{}
	reader.set()
	on := featureRegistry{prefs: repos.Preferences{Automatic: true}, repositories: []repos.Repository{
		{Repository: "acme/widgets", Clones: []repos.Clone{{Path: t.TempDir(), Available: true}}}}}
	w := &FeatureWorker{Store: s, Registry: turnedOff{on}, Now: c.Now,
		GitHub: func(context.Context, github.Repository) (github.Reader, error) { return reader, nil }}
	w.RunOnce(context.Background())
	state, err := s.Activate(context.Background(), "acme/widgets")
	if err != nil || state.Cursor.IsZero() || !state.RelinkedAt.IsZero() {
		t.Fatalf("state = %+v, %v; want synced and not linked", state, err)
	}
	// Turned on, the same pass links.
	w.Registry = on
	w.RunOnce(context.Background())
	if state, err = s.Activate(context.Background(), "acme/widgets"); err != nil || state.RelinkedAt.IsZero() {
		t.Fatalf("state = %+v, %v; want linked", state, err)
	}
}

// A store whose pull requests predate the fork and default branch columns
// gets them, and lists its pull requests again.
func TestOpenMigratesPullRequests(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "decisions")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "decisions.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE pull_request(repository TEXT NOT NULL, number INTEGER NOT NULL, title TEXT NOT NULL,
body TEXT NOT NULL DEFAULT '', state TEXT NOT NULL, draft INTEGER NOT NULL DEFAULT 0, author TEXT NOT NULL DEFAULT '', bot INTEGER NOT NULL DEFAULT 0,
head_ref TEXT NOT NULL DEFAULT '', head_sha TEXT NOT NULL, base_ref TEXT NOT NULL DEFAULT '', base_sha TEXT NOT NULL,
merge_commit_sha TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, merged_at INTEGER, closed_at INTEGER,
paths_head_sha TEXT NOT NULL DEFAULT '', paths_json TEXT, paths_failures INTEGER NOT NULL DEFAULT 0, paths_retry_at INTEGER,
PRIMARY KEY(repository, number));
CREATE TABLE pull_sync(repository TEXT PRIMARY KEY, etag TEXT NOT NULL DEFAULT '', cursor_updated_at INTEGER,
activated_at INTEGER NOT NULL, relinked_at INTEGER);
INSERT INTO pull_sync(repository, etag, cursor_updated_at, activated_at) VALUES ('acme/widgets', '"e1"', 5, 1)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	for i, want := range []string{"", `"e2"`} { // and again, once migrated: no second reset
		s, err := Open(path, OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		state, err := s.Activate(context.Background(), "acme/widgets")
		if err != nil || state.ETag != want || (i == 0 && !state.Cursor.IsZero()) || state.ActivatedAt.UnixMilli() != 1 {
			t.Fatalf("open %d: state %+v, %v", i, state, err)
		}
		if _, err := s.db.Exec(`UPDATE pull_request SET cross_repository=1, default_branch='main'; UPDATE pull_sync SET etag='"e2"'`); err != nil {
			t.Fatal(err)
		}
		_ = s.Close()
	}
}

// A store made with today's schema is not migrated, and keeps its sync state.
func TestOpenKeepsACurrentStoresSyncState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions", "decisions.sqlite")
	s, err := Open(path, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Activate(context.Background(), "acme/widgets"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE pull_sync SET etag='"e1"', cursor_updated_at=5`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if s, err = Open(path, OpenOptions{}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if state, err := s.Activate(context.Background(), "acme/widgets"); err != nil || state.ETag != `"e1"` || state.Cursor.IsZero() {
		t.Fatalf("state %+v, %v", state, err)
	}
}
