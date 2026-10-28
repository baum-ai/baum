package decisionlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/decisions"
	"github.com/baum-ai/baum/features/assign"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/repos"
)

const repo = "acme/widgets"

// scriptBackend answers the feature log's calls by kind: placement,
// feature text and pull request summary.
type scriptBackend struct {
	assign func(user string) (string, error)
	text   func() (string, error)
	pull   func() (string, error)
	// batch, merge and split answer a build's and consolidation's calls.
	batch, merge, split func(user string) (string, error)
	calls               map[string]int
	// usage, when set, is reported instead of a small call's.
	usage  *agent.Usage
	onCall func()
	// rejections is reported with every reply.
	rejections int
	// textInput is the last feature call's input.
	textInput string
}

func (b *scriptBackend) Name() string                          { return agent.BackendClaude }
func (b *scriptBackend) Check(context.Context) (string, error) { return "1.0.0", nil }
func (b *scriptBackend) Run(context.Context, agent.Request) (agent.Result, error) {
	return agent.Result{}, errors.New("not used")
}

func (b *scriptBackend) Complete(_ context.Context, req agent.CompleteRequest) (agent.Completion, error) {
	if b.onCall != nil {
		b.onCall()
	}
	out := agent.Completion{Model: "claude-haiku-5-5", Usage: agent.Usage{Input: 1000, Output: 100}, Rejections: b.rejections}
	if b.usage != nil {
		out.Usage = *b.usage
	}
	schema := string(req.Schema)
	var reply string
	var err error
	switch {
	case strings.Contains(schema, `"assignments"`):
		b.calls["batch"]++
		reply, err = b.batch(req.User)
	case strings.Contains(schema, `"groups"`):
		b.calls["merge"]++
		reply, err = b.merge(req.User)
	case strings.Contains(schema, `"parts"`):
		b.calls["split"]++
		reply, err = b.split(req.User)
	case strings.Contains(schema, `"choice"`):
		b.calls["assign"]++
		reply, err = b.assign(req.User)
	case strings.Contains(schema, `"digest"`):
		b.calls["text"]++
		b.textInput = req.User
		reply, err = b.text()
	default:
		b.calls["pull"]++
		reply, err = b.pull()
	}
	out.Document = []byte(reply)
	return out, err
}

func choose(choice, ref, title string) string {
	return fmt.Sprintf(`{"reason":"r","choice":%q,"feature_id":%q,"title":%q,"description":"%s does it."}`, choice, ref, title, title)
}

type featureFixture struct {
	t       *testing.T
	clock   *clock
	store   *Store
	backend *scriptBackend
	worker  *FeatureWorker
	prefs   repos.Preferences
	// off turns the repository's decisions off; prefsErr fails reading the
	// preferences.
	off      bool
	prefsErr error
}

func newFeatureFixture(t *testing.T) *featureFixture {
	t.Helper()
	f := &featureFixture{t: t, clock: &clock{t0}, prefs: repos.Preferences{Automatic: true}}
	f.store = openStore(t, f.clock)
	f.backend = &scriptBackend{calls: map[string]int{},
		assign: func(string) (string, error) { return choose("new", "", "Widgets"), nil },
		text: func() (string, error) {
			return `{"title":"Widget export","description":"Lets people export widgets.","digest":"Exports widgets.","key_decisions":["D1"]}`, nil
		},
		pull: func() (string, error) { return `{"summary":"Adds an export."}`, nil },
	}
	build := func(string) generate.CheckedBackend { return f.backend }
	f.worker = &FeatureWorker{Store: f.store, Now: f.clock.Now, Config: config.Config{Backend: config.BackendClaude},
		Backends: generate.Backends{Claude: build, Codex: build, Anthropic: build, CursorAgent: build}}
	f.worker.Registry = registryOf(f)
	if _, err := f.store.db.Exec(`INSERT INTO pull_sync(repository, activated_at) VALUES (?, ?)`, repo, t0.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	return f
}

type fixtureRegistry struct{ f *featureFixture }

func registryOf(f *featureFixture) FeatureRegistry { return fixtureRegistry{f} }

func (r fixtureRegistry) Preferences(context.Context) (repos.Preferences, error) {
	return r.f.prefs, r.f.prefsErr
}
func (r fixtureRegistry) Repositories(context.Context) ([]repos.Repository, error) {
	return nil, nil
}
func (r fixtureRegistry) RepositorySettings(context.Context, string) (repos.Settings, bool, error) {
	on := !r.f.off
	return repos.Settings{Decisions: &on}, true, nil
}

// pull records a pull request opened at, with paths at head (nil: not read).
func (f *featureFixture) pull(number int, title string, opened time.Time, head string, paths []string, extra ...string) {
	f.t.Helper()
	var encoded any
	pathsHead := ""
	if paths != nil {
		raw, _ := json.Marshal(paths)
		encoded, pathsHead = string(raw), head
	}
	if _, err := f.store.db.Exec(`INSERT INTO pull_request(repository, number, title, body, state, head_ref, head_sha, base_ref, base_sha,
created_at, updated_at, paths_head_sha, paths_json, default_branch) VALUES (?,?,?,?,'open',?,?,'main','base',?,?,?,?,'main')
ON CONFLICT(repository, number) DO UPDATE SET head_sha=excluded.head_sha, paths_head_sha=excluded.paths_head_sha, paths_json=excluded.paths_json,
updated_at=excluded.updated_at`,
		repo, number, title, strings.Join(extra, ""), fmt.Sprintf("branch-%d", number), head, opened.UnixMilli(), f.clock.now.UnixMilli(), pathsHead, encoded); err != nil {
		f.t.Fatal(err)
	}
}

func (f *featureFixture) exec(query string, args ...any) {
	f.t.Helper()
	if _, err := f.store.db.Exec(query, args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f *featureFixture) assign(o FeatureOptions) AssignReport {
	f.t.Helper()
	report, err := f.worker.Assign(context.Background(), repo, f.t.TempDir(), o)
	if err != nil {
		f.t.Fatal(err)
	}
	return report
}

func (f *featureFixture) pending() []string {
	f.t.Helper()
	r, err := f.worker.newRun(context.Background(), repo, "", FeatureOptions{})
	if err != nil {
		f.t.Fatal(err)
	}
	state, _ := f.store.Activate(context.Background(), repo)
	items, _, err := r.pendingPlacements(context.Background(), state.ActivatedAt)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for _, item := range items {
		out = append(out, item.Trigger+":"+item.name())
	}
	return out
}

func (f *featureFixture) featureOf(number int) string {
	var id string
	_ = f.store.db.QueryRow(`SELECT feature_id FROM feature_pull WHERE number=?`, number).Scan(&id)
	return id
}

func (f *featureFixture) membership(id string) int {
	var v int
	if err := f.store.db.QueryRow(`SELECT membership_version FROM feature WHERE id=?`, id).Scan(&v); err != nil {
		f.t.Fatal(err)
	}
	return v
}

// A pull request is placed once it is opened after the activation, within
// the lookback, by no bot, and its paths are known or it waited for them.
func TestPlacementOpenedTrigger(t *testing.T) {
	f := newFeatureFixture(t)
	f.clock.now = t0.Add(9 * 24 * time.Hour)
	now := f.clock.now
	f.pull(1, "Before the activation", t0.Add(-time.Hour), "h1", []string{"a"})
	f.pull(2, "Outside the lookback", now.Add(-8*24*time.Hour), "h2", []string{"a"})
	f.pull(3, "Paths known", now.Add(-time.Hour), "h3", []string{"a"})
	f.pull(4, "Paths not yet known", now.Add(-5*time.Minute), "h4", nil)
	f.pull(5, "Waited for its paths", now.Add(-20*time.Minute), "h5", nil)
	f.pull(6, "By a bot", now.Add(-time.Hour), "h6", []string{"a"})
	f.exec(`UPDATE pull_request SET bot=1 WHERE number=6`)
	if got, want := f.pending(), []string{"opened:3", "opened:5"}; !slices.Equal(got, want) {
		t.Fatalf("pending = %v, want %v", got, want)
	}
	f.clock.now = t0.Add(time.Hour)
	if got := f.pending(); slices.Contains(got, "opened:1") {
		t.Fatalf("a pull request opened before the activation is placed: %v", got)
	}
	// Manually, the activation does not hold one back.
	report := f.assign(FeatureOptions{Manual: true, DryRun: true})
	if len(report.Items) == 0 || report.Items[0].Number != 1 || report.Items[0].Outcome != FeatureDryRun || report.Items[0].InputBytes == 0 {
		t.Fatalf("manual dry run = %+v", report)
	}
	if f.backend.calls["assign"] != 0 {
		t.Fatal("a dry run called")
	}
}

// A placed pull request is placed again once when it merged, and when a new
// head changed its paths enough; a smaller change is checked once.
func TestPlacementMergedAndUpdated(t *testing.T) {
	f := newFeatureFixture(t)
	f.clock.now = t0.Add(time.Hour)
	f.pull(1, "Export widgets", t0.Add(time.Minute), "h1", []string{"a", "b", "c", "d"})
	if report := f.assign(FeatureOptions{}); len(report.Items) != 1 || report.Items[0].Outcome != FeatureCreated || report.Spent <= 0 {
		t.Fatalf("report = %+v", report)
	}
	feature := f.featureOf(1)
	if feature == "" || len(f.pending()) != 0 {
		t.Fatalf("placed in %q, pending %v", feature, f.pending())
	}

	f.pull(1, "Export widgets", t0.Add(time.Minute), "h2", []string{"a", "b", "c", "d", "e"})
	if got := f.pending(); len(got) != 0 {
		t.Fatalf("a small update is pending: %v", got)
	}
	var checked string
	_ = f.store.db.QueryRow(`SELECT checked_head_sha FROM feature_pull WHERE number=1`).Scan(&checked)
	if checked != "h2" {
		t.Fatalf("checked head = %q", checked)
	}
	f.pull(1, "Export widgets", t0.Add(time.Minute), "h3", []string{"a", "x", "y", "z"})
	if got := f.pending(); !slices.Equal(got, []string{"updated:1"}) {
		t.Fatalf("pending after a large update = %v", got)
	}
	f.backend.assign = func(string) (string, error) { return choose("existing", "F1", ""), nil }
	f.assign(FeatureOptions{})
	if len(f.pending()) != 0 || f.featureOf(1) != feature {
		t.Fatalf("pending %v, feature %s", f.pending(), f.featureOf(1))
	}

	// Merged: placed again once; a skip keeps it where it is.
	f.exec(`UPDATE pull_request SET state='closed', merged_at=? WHERE number=1`, f.clock.now.UnixMilli())
	if got := f.pending(); !slices.Equal(got, []string{"merged:1"}) {
		t.Fatalf("pending after the merge = %v", got)
	}
	f.backend.assign = func(string) (string, error) { return choose("skip", "", ""), nil }
	report := f.assign(FeatureOptions{})
	if report.Items[0].Outcome != FeatureAssigned || f.featureOf(1) != feature || len(f.pending()) != 0 {
		t.Fatalf("report = %+v, feature %s, pending %v", report, f.featureOf(1), f.pending())
	}
}

// A small change in no feature stays in none until its head changes.
func TestPlacementSkipSettlesForTheHead(t *testing.T) {
	f := newFeatureFixture(t)
	f.clock.now = t0.Add(time.Hour)
	f.pull(1, "Fix a typo", t0.Add(time.Minute), "h1", []string{"README.md"})
	f.backend.assign = func(string) (string, error) { return choose("skip", "", ""), nil }
	if report := f.assign(FeatureOptions{}); report.Items[0].Outcome != FeatureSkipped || report.Items[0].Reason != "small_change" {
		t.Fatalf("report = %+v", report)
	}
	if f.featureOf(1) != "" || len(f.pending()) != 0 {
		t.Fatalf("feature %q, pending %v", f.featureOf(1), f.pending())
	}
	f.pull(1, "Fix a typo", t0.Add(time.Minute), "h2", []string{"README.md"})
	if got := f.pending(); !slices.Equal(got, []string{"opened:1"}) {
		t.Fatalf("pending after a push = %v", got)
	}
}

// A failed call backs off for ten minutes; after three counted failures
// the item waits for its next trigger.
func TestPlacementFailuresBackOff(t *testing.T) {
	f := newFeatureFixture(t)
	f.clock.now = t0.Add(time.Hour)
	f.pull(1, "Export widgets", t0.Add(time.Minute), "h1", []string{"a"})
	f.backend.assign = func(string) (string, error) { return "", errors.New("boom") }
	for i := range 3 {
		report := f.assign(FeatureOptions{})
		want := FeatureDeferred
		if i == 2 {
			want = FeatureGivenUp
		}
		if len(report.Items) != 1 || report.Items[0].Outcome != want {
			t.Fatalf("attempt %d: %+v", i, report)
		}
		if got := f.pending(); len(got) != 0 {
			t.Fatalf("pending while backing off: %v", got)
		}
		f.clock.now = f.clock.now.Add(FeatureBackoff + time.Second)
	}
	if got := f.pending(); len(got) != 0 {
		t.Fatalf("given up, yet pending: %v", got)
	}
	f.pull(1, "Export widgets", t0.Add(time.Minute), "h2", []string{"a"})
	if got := f.pending(); !slices.Equal(got, []string{"opened:1"}) {
		t.Fatalf("a new head is not pending: %v", got)
	}
	// An invalid reply counts too.
	f.backend.assign = func(string) (string, error) { return `{"choice":"maybe"}`, nil }
	if report := f.assign(FeatureOptions{}); report.Items[0].Outcome != FeatureDeferred {
		t.Fatalf("report = %+v", report)
	}
}

// saveSession records a captured session's extraction citing files, made
// at the fixture's time.
func (f *featureFixture) saveSession(id string, files ...string) {
	f.t.Helper()
	d := decisions.Decision{ID: "d-" + id, Summary: "Export as CSV.", Category: decisions.CategoryImplementation, Who: decisions.WhoHuman,
		Turns: []int{1}, Files: files}
	if err := f.store.Save(context.Background(), Extraction{Meta: Meta{Agent: "claude-code", ID: id, Repository: repo},
		ContentHash: "c-" + id, Decisions: []decisions.Decision{d}}); err != nil {
		f.t.Fatal(err)
	}
}

// A quiet session linked to no pull request only joins an existing
// feature: with no candidates, or a reply starting one, it is settled.
func TestPlacementSessionsOnlyJoin(t *testing.T) {
	f := newFeatureFixture(t)
	f.clock.now = t0.Add(time.Hour)
	f.saveSession("s1", "export.go")
	f.clock.now = t0.Add(time.Hour + 10*time.Minute)
	if got := f.pending(); len(got) != 0 {
		t.Fatalf("a session not yet quiet is pending: %v", got)
	}
	f.clock.now = t0.Add(time.Hour + 20*time.Minute)
	report := f.assign(FeatureOptions{})
	if len(report.Items) != 1 || report.Items[0].Reason != "no_candidates" || f.backend.calls["assign"] != 0 {
		t.Fatalf("report = %+v, calls %v", report, f.backend.calls)
	}

	// With a feature, the call is made; a new feature is refused.
	f.pull(1, "Export widgets", t0.Add(time.Hour), "h1", []string{"export.go"})
	f.assign(FeatureOptions{})
	feature := f.featureOf(1)
	f.saveSession("s2", "export.go")
	f.clock.now = f.clock.now.Add(20 * time.Minute)
	report = f.assign(FeatureOptions{})
	if len(report.Items) != 1 || report.Items[0].Reason != "no_fit" {
		t.Fatalf("report = %+v", report)
	}
	f.saveSession("s3", "export.go")
	f.clock.now = f.clock.now.Add(20 * time.Minute)
	f.backend.assign = func(string) (string, error) { return choose("existing", "F1", ""), nil }
	before := f.membership(feature)
	report = f.assign(FeatureOptions{})
	var joined string
	_ = f.store.db.QueryRow(`SELECT feature_id FROM feature_session WHERE session_id='s3'`).Scan(&joined)
	if len(report.Items) != 1 || joined != feature || f.membership(feature) != before+1 {
		t.Fatalf("report = %+v, joined %q", report, joined)
	}

	// A session linked to a pull request's commit is never placed on its own.
	f.saveSession("s4", "export.go")
	f.exec(`INSERT INTO pull_commit(repository, number, sha, committed_at) VALUES (?, 1, 'c1', 0)`, repo)
	f.exec(`INSERT INTO session_commit(agent, session_id, repository, sha, method, confidence) VALUES ('claude-code','s4',?,'c1','entire',1)`, repo)
	f.clock.now = f.clock.now.Add(20 * time.Minute)
	if got := f.pending(); len(got) != 0 {
		t.Fatalf("a linked session is pending: %v", got)
	}
}

// Moving a pull request marks the feature it left, the one it joined and
// the features of its linked sessions; new decisions mark the session's
// features.
func TestPlacementMarksTouchedFeatures(t *testing.T) {
	f := newFeatureFixture(t)
	f.clock.now = t0.Add(time.Hour)
	f.pull(1, "Export widgets", t0.Add(time.Minute), "h1", []string{"a", "b", "c"})
	f.pull(2, "Import widgets", t0.Add(2*time.Minute), "h2", []string{"x", "y", "z"})
	f.backend.assign = func(user string) (string, error) {
		return choose("new", "", strings.Split(strings.Split(user, `"title":"`)[1], `"`)[0]), nil
	}
	f.assign(FeatureOptions{})
	export, imports := f.featureOf(1), f.featureOf(2)
	if export == imports || f.membership(export) != 1 || f.membership(imports) != 1 {
		t.Fatalf("features %s %s", export, imports)
	}
	// A session linked to both pull requests' commits.
	f.saveSession("s1", "a")
	f.exec(`INSERT INTO pull_commit(repository, number, sha, committed_at) VALUES (?, 1, 'c1', 0), (?, 2, 'c2', 0)`, repo, repo)
	f.exec(`INSERT INTO session_commit(agent, session_id, repository, sha, method, confidence) VALUES
('claude-code','s1',?,'c1','entire',1), ('claude-code','s1',?,'c2','entire',1)`, repo, repo)
	f.saveSession("s1", "a", "b")
	if f.membership(export) != 2 || f.membership(imports) != 2 {
		t.Fatalf("new decisions did not mark the session's features: %d %d", f.membership(export), f.membership(imports))
	}

	// Pull request 2 moves to a third feature, pull request 1's linked
	// session's feature is marked too.
	f.pull(3, "Widget sync", t0.Add(3*time.Minute), "h3", []string{"q"})
	f.backend.assign = func(string) (string, error) { return choose("new", "", "Widget sync"), nil }
	f.assign(FeatureOptions{})
	third := f.featureOf(3)
	f.pull(2, "Import widgets", t0.Add(2*time.Minute), "h4", []string{"q", "r", "s"})
	f.backend.assign = func(user string) (string, error) {
		var input struct {
			Candidates []struct{ ID, Title string } `json:"candidates"`
		}
		_ = json.Unmarshal([]byte(user), &input)
		for _, c := range input.Candidates {
			if c.Title == "Widget sync" {
				return choose("existing", c.ID, ""), nil
			}
		}
		return "", errors.New("no candidate")
	}
	f.assign(FeatureOptions{})
	if f.featureOf(2) != third || f.membership(imports) != 3 || f.membership(third) != 2 || f.membership(export) != 3 {
		t.Fatalf("after the move: feature %s, memberships %d %d %d", f.featureOf(2), f.membership(imports), f.membership(third), f.membership(export))
	}
}

// Candidates rank as the hosted weighted ranking does.
func TestRankCandidates(t *testing.T) {
	feature := func(id string, last int64, paths ...string) *candidateFeature {
		return &candidateFeature{ID: id, last: last, Paths: paths}
	}
	ids := func(ranked []*candidateFeature) []string {
		var out []string
		for _, f := range ranked {
			out = append(out, f.ID)
		}
		return out
	}
	// A path only one feature holds outweighs two shared with many.
	found := []*candidateFeature{
		feature("shared1", 9, "common", "lib"), feature("shared2", 8, "common", "lib"), feature("shared3", 7, "common", "lib"),
		feature("rare", 1, "export.go"), feature("recent", 10, "other"), feature("current", 0, "nothing"),
	}
	got := ids(rankCandidates(found, []string{"common", "lib", "export.go"}, "current", 16, 10))
	if want := []string{"current", "rare", "shared1", "shared2", "shared3", "recent"}; !slices.Equal(got, want) {
		t.Fatalf("ranked %v, want %v", got, want)
	}
	// A linked feature comes before the path slots, which are bounded;
	// what is left goes by activity, within k.
	found = []*candidateFeature{
		feature("p1", 1, "a"), feature("p2", 2, "a", "b"), feature("p3", 3, "c"), feature("new", 99), feature("linked", 0),
	}
	found[4].Links = assign.Links{Mentioned: []int32{7}}
	got = ids(rankCandidates(found, []string{"a", "b"}, "", 4, 1))
	if want := []string{"linked", "p2", "new", "p3"}; !slices.Equal(got, want) {
		t.Fatalf("ranked %v, want %v", got, want)
	}
}

// Candidates carry the links of the item pull request: stacked on it,
// sharing a session, mentioned.
func TestPlacementCandidateLinks(t *testing.T) {
	f := newFeatureFixture(t)
	f.clock.now = t0.Add(time.Hour)
	f.pull(1, "Base of the stack", t0.Add(time.Minute), "h1", []string{"a"})
	f.pull(2, "Mentioned", t0.Add(2*time.Minute), "h2", []string{"m"})
	f.pull(3, "Shares a session", t0.Add(3*time.Minute), "h3", []string{"s"})
	n := 0
	f.backend.assign = func(string) (string, error) { n++; return choose("new", "", fmt.Sprintf("Feature %d", n)), nil }
	f.assign(FeatureOptions{})
	f.pull(4, "On top, after #2", t0.Add(4*time.Minute), "h4", []string{"z"})
	f.exec(`UPDATE pull_request SET base_ref='branch-1' WHERE number=4`)
	f.exec(`INSERT INTO pull_commit(repository, number, sha, committed_at) VALUES (?, 3, 'c3', 0), (?, 4, 'c4', 0)`, repo, repo)
	f.exec(`INSERT INTO session_commit(agent, session_id, repository, sha, method, confidence) VALUES
('claude-code','s1',?,'c3','entire',1), ('claude-code','s1',?,'c4','entire',1)`, repo, repo)
	var seen string
	f.backend.assign = func(user string) (string, error) { seen = user; return choose("skip", "", ""), nil }
	f.assign(FeatureOptions{})
	for _, want := range []string{`"stacked":[1]`, `"mentioned":[2]`, `"shared_sessions":[3]`} {
		if !strings.Contains(seen, want) {
			t.Fatalf("the input lacks %s: %s", want, seen)
		}
	}
}

// One daily cap holds capture's and the feature log's spend together.
func TestFeatureCallsShareTheDailyCap(t *testing.T) {
	f := newFeatureFixture(t)
	f.clock.now = t0.Add(time.Hour)
	f.pull(1, "Export widgets", t0.Add(time.Minute), "h1", []string{"a"})
	limit := 1.0
	f.prefs.Decisions.DailyCap = &limit
	id, err := f.store.Begin(context.Background(), Attempt{Agent: "claude-code", ID: "s1", Backend: "claude", Model: "m", Mode: "full", Reserved: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.Settle(context.Background(), id, AttemptSucceeded, "", new(1.0), decisions.Usage{}); err != nil {
		t.Fatal(err)
	}
	report := f.assign(FeatureOptions{})
	if report.Stopped != ReasonDailyCap || len(report.Items) != 0 || f.backend.calls["assign"] != 0 {
		t.Fatalf("report = %+v", report)
	}
	limit = 5
	if report = f.assign(FeatureOptions{}); len(report.Items) != 1 {
		t.Fatalf("report = %+v", report)
	}
	var stage, item string
	if err := f.store.db.QueryRow(`SELECT stage, item FROM capture_attempt ORDER BY id DESC LIMIT 1`).Scan(&stage, &item); err != nil ||
		stage != StageAssign || item != "pull:1" {
		t.Fatalf("ledger %s %s %v", stage, item, err)
	}
	// Capture counts the feature log's spend.
	st, err := f.store.Stats(context.Background(), "", "", startOfDay(f.clock.now))
	if err != nil || st.AllSpent <= 1 {
		t.Fatalf("all spent %v, %v", st.AllSpent, err)
	}
}

// A fork's pull request, or a branch shared with the default branch, links
// nothing as a stack.
func TestPlacementStackSkipsForksAndTheDefaultBranch(t *testing.T) {
	f := newFeatureFixture(t)
	f.clock.now = t0.Add(time.Hour)
	f.pull(1, "Fork from main", t0.Add(time.Minute), "h1", []string{"a"})
	f.exec(`UPDATE pull_request SET head_ref='main', cross_repository=1, default_branch='main'`)
	f.assign(FeatureOptions{})
	f.pull(2, "Targets the fork's branch", t0.Add(2*time.Minute), "h2", []string{"z"})
	f.pull(3, "On develop", t0.Add(3*time.Minute), "h3", []string{"y"})
	f.exec(`UPDATE pull_request SET default_branch='main' WHERE number IN (2, 3)`)
	f.exec(`UPDATE pull_request SET base_ref='main' WHERE number=2`)
	links, err := f.store.pullLinks(context.Background(), repo, 2, nil)
	if err != nil || len(links["stacked"]) != 0 {
		t.Fatalf("links of a pull request on main = %v, %v", links, err)
	}
	// The same stack within the repository links.
	f.exec(`UPDATE pull_request SET cross_repository=0, head_ref='branch-1' WHERE number=1`)
	f.exec(`UPDATE pull_request SET base_ref='branch-1' WHERE number=3`)
	if links, err = f.store.pullLinks(context.Background(), repo, 3, nil); err != nil || !links["stacked"][1] {
		t.Fatalf("links of a stacked pull request = %v, %v", links, err)
	}
	f.exec(`UPDATE pull_request SET cross_repository=1 WHERE number=1`)
	if links, err = f.store.pullLinks(context.Background(), repo, 3, nil); err != nil || len(links["stacked"]) != 0 {
		t.Fatalf("a fork's branch links: %v, %v", links, err)
	}
	// An unknown default branch links no stack either.
	f.exec(`UPDATE pull_request SET cross_repository=0, default_branch=''`)
	if links, err = f.store.pullLinks(context.Background(), repo, 3, nil); err != nil || len(links["stacked"]) != 0 {
		t.Fatalf("a stack with no default branch links: %v, %v", links, err)
	}
}

// A pull request placed before its paths were known gets them, with no
// call, and its feature is marked; new commits or links mark the features
// they reach, an unchanged re-link none.
func TestFeatureMarksOnPathsAndLinks(t *testing.T) {
	f := newFeatureFixture(t)
	ctx := context.Background()
	f.clock.now = t0.Add(time.Hour)
	f.pull(1, "Export widgets", t0.Add(time.Minute), "h1", nil)
	f.assign(FeatureOptions{})
	feature := f.featureOf(1)
	if feature == "" || f.membership(feature) != 1 {
		t.Fatalf("placed without paths in %q", feature)
	}
	at := time.Unix(0, 0)
	if err := f.store.SetPullPaths(ctx, repo, 1, "h1", []string{"export.go"}, []PullCommit{{SHA: "c1", CommittedAt: at}}); err != nil {
		t.Fatal(err)
	}
	var placed string
	_ = f.store.db.QueryRow(`SELECT assigned_paths_json FROM feature_pull WHERE number=1`).Scan(&placed)
	if placed != `["export.go"]` || f.membership(feature) != 2 || f.backend.calls["assign"] != 1 {
		t.Fatalf("placed paths %s, membership %d, calls %v", placed, f.membership(feature), f.backend.calls)
	}
	// Recording the same again marks nothing.
	if err := f.store.SetPullPaths(ctx, repo, 1, "h1", []string{"export.go"}, []PullCommit{{SHA: "c1", CommittedAt: at}}); err != nil {
		t.Fatal(err)
	}
	if f.membership(feature) != 2 {
		t.Fatalf("an unchanged read marked the feature: %d", f.membership(feature))
	}
	// A session placed on its own elsewhere, then linked to the commit:
	// both features are marked; the same links again mark nothing.
	f.pull(2, "Import widgets", t0.Add(2*time.Minute), "h2", []string{"import.go"})
	f.backend.assign = func(string) (string, error) { return choose("new", "", "Import"), nil }
	f.assign(FeatureOptions{})
	other := f.featureOf(2)
	f.saveSession("s1", "import.go")
	f.exec(`INSERT INTO feature_session(version_id, repository, agent, session_id, feature_id, assigned_at)
SELECT version_id, repository, 'claude-code', 's1', ?, 0 FROM feature WHERE id=?`, other, other)
	before, otherBefore := f.membership(feature), f.membership(other)
	link := []SessionCommit{{Agent: "claude-code", SessionID: "s1", SHA: "c1", Method: "entire", Confidence: 1}}
	if err := f.store.ReplaceSessionCommits(ctx, repo, []string{"c1"}, link, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if f.membership(feature) != before+1 || f.membership(other) != otherBefore+1 {
		t.Fatalf("a new link marked %d→%d, %d→%d", before, f.membership(feature), otherBefore, f.membership(other))
	}
	if err := f.store.ReplaceSessionCommits(ctx, repo, []string{"c1"}, link, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if f.membership(feature) != before+1 {
		t.Fatal("an unchanged re-link marked the feature")
	}
	// A new head's commits mark the feature and the linked session's.
	if err := f.store.SetPullPaths(ctx, repo, 1, "h1b", []string{"export.go"}, []PullCommit{{SHA: "c1", CommittedAt: at}, {SHA: "c2", CommittedAt: at}}); err != nil {
		t.Fatal(err)
	}
	if f.membership(feature) != before+2 || f.membership(other) != otherBefore+2 {
		t.Fatalf("new commits marked %d, %d", f.membership(feature), f.membership(other))
	}
}

// A repository turned off, or a daily cap reached, during a run stops it
// before the next item; a run cut short counts no failure.
func TestPlacementRereadsTheGateAndCap(t *testing.T) {
	f := newFeatureFixture(t)
	f.clock.now = t0.Add(time.Hour)
	f.pull(1, "One", t0.Add(time.Minute), "h1", []string{"a"})
	f.pull(2, "Two", t0.Add(2*time.Minute), "h2", []string{"b"})
	f.pull(3, "Three", t0.Add(3*time.Minute), "h3", []string{"c"})
	f.backend.onCall = func() { f.off = true }
	if report := f.assign(FeatureOptions{}); len(report.Items) != 1 || report.Stopped != "disabled" {
		t.Fatalf("report = %+v", report)
	}
	f.off = false
	limit := 100.0
	f.prefs.Decisions.DailyCap = &limit
	f.backend.onCall = func() { limit = 0 }
	if report := f.assign(FeatureOptions{}); len(report.Items) != 1 || report.Stopped != ReasonDailyCap {
		t.Fatalf("report = %+v", report)
	}
	limit = 100
	ctx, cancel := context.WithCancel(context.Background())
	f.backend.onCall = cancel
	f.backend.assign = func(string) (string, error) { return "", context.Canceled }
	if _, err := f.worker.Assign(ctx, repo, t.TempDir(), FeatureOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	var failures int
	_ = f.store.db.QueryRow(`SELECT COALESCE(SUM(failures), 0) FROM feature_settle`).Scan(&failures)
	if failures != 0 || !slices.Equal(f.pending(), []string{"opened:3"}) {
		t.Fatalf("failures %d, pending %v", failures, f.pending())
	}
}

// A dry run on a repository never placed records nothing.
func TestPlacementDryRunWritesNothing(t *testing.T) {
	f := newFeatureFixture(t)
	f.exec(`DELETE FROM pull_sync`)
	f.clock.now = t0.Add(time.Hour)
	f.pull(1, "One", t0.Add(time.Minute), "h1", []string{"a"})
	f.assign(FeatureOptions{DryRun: true})
	var rows int
	_ = f.store.db.QueryRow(`SELECT (SELECT COUNT(*) FROM pull_sync) + (SELECT COUNT(*) FROM feature_version) + (SELECT COUNT(*) FROM feature_settle)`).Scan(&rows)
	if rows != 0 {
		t.Fatalf("a dry run wrote %d rows", rows)
	}
}

// Sync keeps a pull request's fork flag and its base's default branch, and a
// reply the harness rejected first is recorded with the attempt.
func TestSavePullsKeepsForksAndLedgerKeepsRejections(t *testing.T) {
	f := newFeatureFixture(t)
	f.clock.now = t0.Add(time.Hour)
	pull := github.ListedPull{Number: 9, Title: "From a fork", State: "open", HeadRef: "main", HeadSHA: "h9", BaseRef: "main", BaseSHA: "b",
		CrossRepository: true, DefaultBranch: "main", CreatedAt: t0, UpdatedAt: t0}
	if err := f.store.SavePulls(context.Background(), repo, []github.ListedPull{pull}, "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	var cross int
	var branch string
	if err := f.store.db.QueryRow(`SELECT cross_repository, default_branch FROM pull_request WHERE number=9`).Scan(&cross, &branch); err != nil ||
		cross != 1 || branch != "main" {
		t.Fatalf("stored %d %q, %v", cross, branch, err)
	}
	f.pull(1, "Export widgets", t0.Add(time.Minute), "h1", []string{"a"})
	f.backend.rejections = 1
	f.assign(FeatureOptions{})
	var reason string
	var rejections int
	if err := f.store.db.QueryRow(`SELECT reason, rejections FROM capture_attempt WHERE stage=?`, StageAssign).Scan(&reason, &rejections); err != nil ||
		reason != "" || rejections != 1 {
		t.Fatalf("reason %q, rejections %d, %v", reason, rejections, err)
	}
}

// Preferences that cannot be read stop a run; a ledger that cannot be
// written after a good reply keeps the reply.
func TestPlacementUnreadablePreferencesAndLedger(t *testing.T) {
	f := newFeatureFixture(t)
	f.clock.now = t0.Add(time.Hour)
	f.pull(1, "One", t0.Add(time.Minute), "h1", []string{"a"})
	f.pull(2, "Two", t0.Add(2*time.Minute), "h2", []string{"b"})
	f.backend.onCall = func() { f.prefsErr = errors.New("registry gone") }
	report, err := f.worker.Assign(context.Background(), repo, t.TempDir(), FeatureOptions{})
	if err == nil || report.Stopped != "preferences_unreadable" || len(report.Items) != 1 {
		t.Fatalf("report = %+v, %v", report, err)
	}
	f.prefsErr, f.backend.onCall = nil, nil
	f.exec(`CREATE TRIGGER no_settle BEFORE UPDATE ON capture_attempt BEGIN SELECT RAISE(ABORT, 'read-only ledger'); END`)
	if report := f.assign(FeatureOptions{}); len(report.Items) != 1 || report.Items[0].Outcome != FeatureCreated || f.featureOf(2) == "" {
		t.Fatalf("report = %+v", report)
	}
}

// A re-link of many commits, some named twice, reads and marks them in
// chunks.
func TestRelinkManyCommitsMarksTheirFeatures(t *testing.T) {
	f := newFeatureFixture(t)
	f.clock.now = t0.Add(time.Hour)
	f.pull(1, "One", t0.Add(time.Minute), "h1", []string{"a"})
	f.assign(FeatureOptions{})
	feature := f.featureOf(1)
	f.exec(`INSERT INTO pull_commit(repository, number, sha, committed_at) VALUES (?, 1, 'c600', 0)`, repo)
	var shas []string
	var links []SessionCommit
	for i := range 601 {
		sha := fmt.Sprintf("c%d", i)
		shas = append(shas, sha)
		links = append(links, SessionCommit{Agent: "claude-code", SessionID: fmt.Sprintf("s%d", i), SHA: sha, Method: "entire", Confidence: 1})
	}
	shas = append(shas, "c600", "c1")
	before := f.membership(feature)
	if err := f.store.ReplaceSessionCommits(context.Background(), repo, shas, links, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if f.membership(feature) != before+1 {
		t.Fatalf("membership %d, want %d", f.membership(feature), before+1)
	}
}

// The daily cap check and the reservation are one: of two reservations at
// once that together pass the cap, one is made.
func TestReservationsWithinTheCapAreAtomic(t *testing.T) {
	s := openStore(t, &clock{t0})
	limit := 1.0
	var wg sync.WaitGroup
	made := make(chan bool, 2)
	for range 2 {
		wg.Go(func() {
			_, ok, err := s.beginWithin(context.Background(), Attempt{Backend: "claude", Model: "m", Reserved: 0.6, Stage: StageAssign}, &limit, nil, t0)
			if err != nil {
				t.Error(err)
			}
			made <- ok
		})
	}
	wg.Wait()
	close(made)
	n := 0
	for ok := range made {
		if ok {
			n++
		}
	}
	var rows int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM capture_attempt`).Scan(&rows)
	if n != 1 || rows != 1 {
		t.Fatalf("%d reservations made, %d rows", n, rows)
	}
	// A session's own bound holds the same way, whatever the daily cap.
	session := 0.25
	for range 2 {
		wg.Go(func() {
			if _, _, err := s.beginWithin(context.Background(), Attempt{Agent: "claude-code", ID: "s1", Backend: "claude", Model: "m", Reserved: 0.2},
				nil, &session, t0); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM capture_attempt WHERE session_id='s1'`).Scan(&rows)
	if rows != 1 {
		t.Fatalf("%d session reservations made", rows)
	}
}
