package decisionlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/locallink/linktest"
	"github.com/baum-ai/baum/internal/repos"
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
	calls int
}

func (r *fakeReader) Read(_ context.Context, path, etag string) (int, http.Header, []byte, error) {
	r.calls++
	if etag == `"e1"` {
		return http.StatusNotModified, http.Header{}, nil, nil
	}
	if !strings.HasSuffix(path, "page=1") {
		return http.StatusOK, http.Header{}, []byte("[]"), nil
	}
	body, _ := json.Marshal(r.pulls)
	return http.StatusOK, http.Header{"Etag": []string{`"e1"`}}, body, nil
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
	reader := &fakeReader{pulls: []map[string]any{pullJSON(5, f.Head, f.Base), pullJSON(6, six, f.Base)}}
	w := &FeatureWorker{Store: s, Now: c.Now, Sessions: f.Store,
		GitHub: func(context.Context, github.Repository) (github.Reader, error) { return reader, nil }}
	report, err := w.Sync(ctx, linktest.Repository, f.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if report.Listed != 2 || report.PathsRead != 2 || report.PathsUnknown != 0 || report.Commits != 3 {
		t.Fatalf("report = %+v", report)
	}
	if got := linktest.Git(t, f.Repo, "rev-parse", "refs/baum/pull/6/head"); got != six {
		t.Fatalf("pull request 6's head was not fetched: %s", got)
	}
	var paths string
	if err := s.db.QueryRow(`SELECT paths_json FROM pull_request WHERE repository=? AND number=5`, linktest.Repository).Scan(&paths); err != nil || paths != `["b.txt","c.txt"]` {
		t.Fatalf("paths = %s, %v", paths, err)
	}
	if report, err = w.Sync(ctx, linktest.Repository, f.Repo); err != nil || !report.NotModified || report.Calls != 1 || report.PathsRead != 0 {
		t.Fatalf("second sync = %+v, %v", report, err)
	}

	indexed, err := w.Index(ctx, linktest.Repository, f.Repo, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !indexed.Full || indexed.Commits != 3 || indexed.Links != 1 || indexed.Sessions != 1 {
		t.Fatalf("index = %+v", indexed)
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
}

func (r featureRegistry) Preferences(context.Context) (repos.Preferences, error) { return r.prefs, nil }
func (r featureRegistry) Repositories(context.Context) ([]repos.Repository, error) {
	return r.repositories, nil
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
			calls := 0
			w := &FeatureWorker{Store: openStore(t, &clock{t0}), Registry: registry, Tick: time.Millisecond,
				GitHub: func(context.Context, github.Repository) (github.Reader, error) {
					calls++
					return nil, fmt.Errorf("unexpected")
				}}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				w.Run(ctx)
				close(done)
			}()
			time.Sleep(20 * time.Millisecond)
			w.Wake()
			cancel()
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
