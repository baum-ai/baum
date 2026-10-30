package decisionlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/features/assign"
	"github.com/baum-ai/baum/features/discover"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/locallink/linktest"
)

// discoveryModel is the review model discovery runs with in these tests.
const discoveryModel = "claude-opus-5-5"

// discoveryClone is a clone of acme/widgets whose origin leads to a local
// repository with src/search/a.go and README.md on main.
func discoveryClone(t *testing.T) string {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "remote")
	if err := os.MkdirAll(filepath.Join(remote, "src", "search"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"src/search/a.go": "package search\n", "README.md": "# widgets\n"} {
		if err := os.WriteFile(filepath.Join(remote, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	linktest.Git(t, remote, "init", "-q", "-b", "main")
	linktest.Git(t, remote, "add", ".")
	linktest.Git(t, remote, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "init")
	clone := filepath.Join(t.TempDir(), "clone")
	linktest.Git(t, remote, "clone", "-q", remote, clone)
	linktest.Git(t, clone, "remote", "set-url", "origin", "https://github.com/acme/widgets.git")
	linktest.Git(t, clone, "config", "url."+remote+".insteadOf", "https://github.com/acme/widgets.git")
	return clone
}

const discoveryReply = `{"features":[{"title":"Widget search","description":"Find widgets.","paths":["src/search/"],"examples":[1]},` +
	`{"title":"Billing","description":"Pay for widgets.","paths":["billing/"],"examples":[]}]}`

// discoveryFixture is a fixture whose review model is priced, with three
// merged pull requests (two under src/search, one in docs) and a clone, and
// a batch reply that joins a pull request under src/search to the seed
// "Widget search" when it is offered, and starts a feature otherwise.
func discoveryFixture(t *testing.T) (*featureFixture, string) {
	f := newFeatureFixture(t)
	f.clock.now = t0.Add(400 * 24 * time.Hour)
	f.worker.Config.Model = discoveryModel
	f.pull(1, "Search box", f.clock.now.Add(-300*24*time.Hour), "h1", []string{"src/search/a.go"}, "a private description")
	f.pull(2, "Search ranking", f.clock.now.Add(-2*24*time.Hour), "h2", []string{"src/search/b.go"})
	f.pull(3, "Docs", f.clock.now.Add(-24*time.Hour), "h3", []string{"docs/x.md"})
	f.exec(`UPDATE pull_request SET state='closed', merged_at=created_at`)
	f.backend.task = func(agent.Request) (agent.Result, error) {
		return agent.Result{Document: []byte(discoveryReply), Model: discoveryModel, Calls: 1, Tokens: 100,
			Usage: agent.Usage{Input: 1000, Output: 100, Speed: new("standard")}}, nil
	}
	f.backend.batch = func(user string) (string, error) {
		var input struct {
			Items []struct {
				ID    string   `json:"id"`
				Paths []string `json:"paths"`
			} `json:"items"`
			Candidates []struct{ ID, Title string } `json:"candidates"`
		}
		if err := json.Unmarshal([]byte(user), &input); err != nil {
			t.Fatal(err)
		}
		seed := ""
		for _, c := range input.Candidates {
			if c.Title == "Widget search" {
				seed = c.ID
			}
		}
		var out []string
		started := 0
		for _, item := range input.Items {
			if seed != "" && strings.HasPrefix(item.Paths[0], "src/search/") {
				out = append(out, fmt.Sprintf(`{"item":%q,"reason":"r","choice":"existing","feature_id":%q,"new_id":"","title":"","description":""}`, item.ID, seed))
			} else {
				started++
				out = append(out, fmt.Sprintf(`{"item":%q,"reason":"r","choice":"new","feature_id":"","new_id":"N%d","title":"Own %s","description":"Own."}`,
					item.ID, started, item.ID))
			}
		}
		return `{"assignments":[` + strings.Join(out, ",") + `]}`, nil
	}
	return f, discoveryClone(t)
}

func (f *featureFixture) buildIn(dir string, o BuildOptions) BuildReport {
	f.t.Helper()
	report, err := f.worker.Build(context.Background(), repo, dir, o)
	if err != nil {
		f.t.Fatal(err)
	}
	return report
}

func (f *featureFixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.store.db.QueryRow(query, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// A first build runs discovery through the review harness over a run
// directory of the default branch's head and the history digest, within the
// discovery budgets, stores the taxonomy and seeds the version from it. The
// seeds stay out of the ranking and are offered by path in the seeded batch
// version; a joined seed is an ordinary feature, and those none joined are
// deleted before the switch.
func TestBuildDiscoversAndSeeds(t *testing.T) {
	f, dir := discoveryFixture(t)
	var task agent.Request
	reply := f.backend.task
	f.backend.task = func(req agent.Request) (agent.Result, error) {
		task = req
		if _, err := os.Stat(filepath.Join(req.Directory, "head", "src", "search", "a.go")); err != nil {
			t.Errorf("the run directory lacks the head checkout: %v", err)
		}
		info, err := os.Stat(filepath.Join(req.Directory, "history.jsonl"))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o222 != 0 {
			t.Fatalf("history.jsonl: %v, %v", info, err)
		}
		history, _ := os.ReadFile(filepath.Join(req.Directory, "history.jsonl"))
		if !strings.Contains(string(history), `"title":"Search box"`) || strings.Contains(string(history), "private description") {
			t.Errorf("history.jsonl = %s", history)
		}
		if !strings.Contains(req.Prompt, discover.PublicPrompt) {
			t.Error("the task's prompt is not the published discovery prompt")
		}
		return reply(req)
	}
	estimate, err := f.worker.EstimateBuild(context.Background(), repo, dir, BuildOptions{})
	if err != nil || !estimate.Discovery || estimate.PullRequests != 3 {
		t.Fatalf("estimate = %+v, %v", estimate, err)
	}
	report := f.buildIn(dir, BuildOptions{})
	if _, err := os.Stat(task.Directory); !os.IsNotExist(err) {
		t.Fatalf("the run directory %s is left behind: %v", task.Directory, err)
	}
	// A review run's budgets, the defaults where the configuration sets none,
	// whatever the model's price.
	if want := config.Default().Budgets; task.MaxTokens != want.MaxTokens || task.MaxCalls != want.MaxCalls {
		t.Fatalf("the task ran with %d tokens and %d calls, want a review run's %+v", task.MaxTokens, task.MaxCalls, want)
	}
	if !report.Current || report.Discovery == nil || report.Discovery.TaxonomyID == 0 || report.Taxonomy != report.Discovery.TaxonomyID ||
		report.Seeds != 2 || report.SeedsJoined != 1 || report.SeedsDeleted != 1 || report.Features != 2 || report.Unseeded != "" {
		t.Fatalf("report = %+v, discovery %+v", report, report.Discovery)
	}
	if f.count(`SELECT COUNT(*) FROM capture_attempt WHERE stage=? AND state=? AND cost_usd > 0`, StageDiscovery, AttemptSucceeded) != 1 ||
		report.SpentUSD < *report.Discovery.SpentUSD || report.VersionSpentUSD != report.SpentUSD {
		t.Fatalf("discovery's spend: %+v", report)
	}
	seeded := assign.Public.SeededBatch(mustBatch(t))
	request, err := assign.Public.Request(seeded, "", 0)
	if err != nil || seeded == mustBatch(t) || len(f.backend.batchSystems) != 1 || f.backend.batchSystems[0] != request.System {
		t.Fatalf("the batch did not use the seeded version %s: %v", seeded, err)
	}
	search := f.featureOf(1)
	if search == "" || f.featureOf(2) != search || f.featureOf(3) == search {
		t.Fatal("the pull requests under the seed's path did not join it")
	}
	var title string
	_ = f.store.db.QueryRow(`SELECT title FROM feature WHERE id=?`, search).Scan(&title)
	if f.count(`SELECT COUNT(*) FROM feature WHERE version_id=? AND title='Billing'`, report.Version) != 0 || title == "" {
		t.Fatalf("the empty seed was kept, or the joined one has no text (%q)", title)
	}
}

func mustBatch(t *testing.T) string {
	batch, ok := assign.Public.BatchVersion(AssignVersion)
	if !ok {
		t.Fatal("no batch version")
	}
	return batch
}

// A resumed build never seeds again nor discovers again.
func TestBuildResumeDoesNotReseed(t *testing.T) {
	f, dir := discoveryFixture(t)
	batch := f.backend.batch
	f.backend.batch = func(user string) (string, error) {
		f.off = true
		return batch(user)
	}
	first := f.buildIn(dir, BuildOptions{Gated: true})
	if first.Current || first.Stopped != ReasonDisabled || first.Seeds != 2 {
		t.Fatalf("first run = %+v", first)
	}
	f.off = false
	f.backend.batch = batch
	second := f.buildIn(dir, BuildOptions{Gated: true})
	if !second.Current || second.Seeds != 0 || second.Discovery != nil || second.Taxonomy != first.Taxonomy || f.backend.runs != 1 ||
		f.count(`SELECT COUNT(*) FROM feature_seed WHERE version_id=?`, first.Version) != 2 {
		t.Fatalf("second run = %+v after %d discoveries", second, f.backend.runs)
	}
}

// A taxonomy made after the version is reused without a discovery; a build
// told none seeds nothing, and one told a taxonomy's id seeds from it.
func TestBuildTaxonomyChoices(t *testing.T) {
	t.Run("newer", func(t *testing.T) {
		f, dir := discoveryFixture(t)
		f.exec(`INSERT INTO feature_version(repository, state, created_at) VALUES (?, ?, ?)`, repo, VersionBuilding, f.clock.now.Add(-time.Hour).UnixMilli())
		report, err := f.worker.Discover(context.Background(), repo, dir)
		if err != nil || report.TaxonomyID == 0 || report.Features != 2 {
			t.Fatalf("discover = %+v, %v", report, err)
		}
		estimate, err := f.worker.EstimateBuild(context.Background(), repo, dir, BuildOptions{})
		if err != nil || estimate.Discovery {
			t.Fatalf("estimate = %+v, %v", estimate, err)
		}
		built := f.buildIn(dir, BuildOptions{})
		if built.Taxonomy != report.TaxonomyID || built.Discovery != nil || f.backend.runs != 1 || built.SeedsJoined != 1 {
			t.Fatalf("build = %+v", built)
		}
	})
	t.Run("none", func(t *testing.T) {
		f, dir := discoveryFixture(t)
		built := f.buildIn(dir, BuildOptions{Taxonomy: TaxonomyNone})
		if built.Seeds != 0 || built.Taxonomy != 0 || f.backend.runs != 0 || built.Features != 3 {
			t.Fatalf("build = %+v", built)
		}
	})
	t.Run("id", func(t *testing.T) {
		f, dir := discoveryFixture(t)
		report, err := f.worker.Discover(context.Background(), repo, dir)
		if err != nil {
			t.Fatal(err)
		}
		built := f.buildIn(dir, BuildOptions{Taxonomy: fmt.Sprint(report.TaxonomyID)})
		if built.Taxonomy != report.TaxonomyID || f.backend.runs != 1 || built.Seeds != 2 {
			t.Fatalf("build = %+v", built)
		}
	})
}

// A discovery that fails leaves the build unseeded with the reason, and the
// failed task's ledger row; a resume does not run it again.
func TestBuildFailedDiscoveryIsUnseeded(t *testing.T) {
	f, dir := discoveryFixture(t)
	f.backend.task = func(agent.Request) (agent.Result, error) {
		return agent.Result{Document: []byte(`{"features":[]}`), Model: discoveryModel, Calls: 1, Tokens: 10}, nil
	}
	batch := f.backend.batch
	f.backend.batch = func(string) (string, error) {
		f.off = true
		return "", errors.New("refused")
	}
	if first := f.buildIn(dir, BuildOptions{Gated: true}); first.Current || !strings.HasPrefix(first.Unseeded, ReasonDiscoveryFailed) {
		t.Fatalf("first run = %+v", first)
	}
	f.off, f.backend.batch = false, batch
	report := f.buildIn(dir, BuildOptions{Gated: true})
	if !report.Current || report.Seeds != 0 || report.Discovery != nil || !strings.HasPrefix(report.Unseeded, ReasonDiscoveryFailed) || report.Features != 3 {
		t.Fatalf("report = %+v", report)
	}
	if f.count(`SELECT COUNT(*) FROM capture_attempt WHERE stage=? AND state=?`, StageDiscovery, AttemptFailed) != 1 ||
		f.count(`SELECT COUNT(*) FROM feature_taxonomy`) != 0 {
		t.Fatal("the failed discovery's ledger row or taxonomy")
	}
}

// Seeds go before ranked candidates not linked to the item with a lower
// path score, best first; one holding none of the item's paths is not
// offered.
func TestWithSeedsOffersSeedsByPath(t *testing.T) {
	seeds := []buildSeed{
		{assign.Candidate{ID: "s1", Paths: []string{"a/"}}},
		{assign.Candidate{ID: "s2", Paths: []string{"a/", "b/"}}},
		{assign.Candidate{ID: "s3", Paths: []string{"c/"}}},
	}
	ranked := []assign.Candidate{
		{ID: "linked", Links: assign.Links{Stacked: []int32{4}}},
		{ID: "strong"},
		{ID: "weak"},
	}
	got := withSeeds(ranked, []float64{0, 2, 0.1}, seeds, []string{"a/x.go", "b/y.go"})
	var ids []string
	for _, c := range got {
		ids = append(ids, c.ID)
	}
	// s2 scores 1.5, s1 0.5.
	if want := "linked strong s2 s1 weak"; strings.Join(ids, " ") != want {
		t.Fatalf("candidates %v, want %s", ids, want)
	}
}

// A build call whose spend the version cannot record only logs: the build
// goes on and the call's ledger row keeps its cost. A call whose cost is
// unknown is counted on the version.
func TestBuildSpendNotRecordedOrUnpriced(t *testing.T) {
	f := newFeatureFixture(t)
	f.clock.now = t0.Add(30 * 24 * time.Hour)
	f.history(3)
	f.backend.batch = func(user string) (string, error) { return batchReply(t, user, ""), nil }
	f.exec(`CREATE TRIGGER no_spend BEFORE UPDATE OF spent_usd ON feature_version BEGIN SELECT RAISE(ABORT, 'refused'); END`)
	report := f.build(BuildOptions{})
	if !report.Current || report.SpentUSD <= 0 || report.UnpricedCalls != 0 {
		t.Fatalf("report = %+v", report)
	}
	if f.count(`SELECT COUNT(*) FROM capture_attempt WHERE stage=? AND cost_usd > 0`, StageAssign) == 0 ||
		f.count(`SELECT COUNT(*) FROM feature_version WHERE id=? AND spent_usd=0`, report.Version) != 1 {
		t.Fatal("the ledger lost the call's cost, or the version recorded it")
	}

	g := newFeatureFixture(t)
	g.clock.now = t0.Add(30 * 24 * time.Hour)
	g.history(3)
	g.backend.batch = f.backend.batch
	g.backend.usage = &agent.Usage{Unreported: true}
	unpriced := g.build(BuildOptions{})
	if !unpriced.Current || unpriced.UnpricedCalls != unpriced.Calls || unpriced.Calls == 0 {
		t.Fatalf("report = %+v", unpriced)
	}
}
