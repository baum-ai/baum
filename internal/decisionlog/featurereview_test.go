package decisionlog

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/features/consolidate"
)

// A resumed version's estimate counts its features behind and their pull
// requests' summaries.
func TestBuildEstimateOfAResumedVersion(t *testing.T) {
	f := newFeatureFixture(t)
	f.clock.now = t0.Add(60 * 24 * time.Hour)
	version, err := featureVersion(context.Background(), f.store.db, repo, VersionBuilding, true, f.clock.now)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 6; i++ {
		f.pull(i, fmt.Sprintf("Change %d", i), f.clock.now.Add(-time.Hour), fmt.Sprintf("h%d", i), []string{"a.go"}, "A description.")
		f.feature(version, fmt.Sprintf("F%d", i), i)
	}
	cost := func() float64 {
		t.Helper()
		e, err := f.worker.EstimateBuild(context.Background(), repo, "", BuildOptions{})
		if err != nil || e.EstimateUSD == nil || e.PullRequests != 0 {
			t.Fatalf("estimate = %+v, %v", e, err)
		}
		return *e.EstimateUSD
	}
	if clean := cost(); clean != 0 {
		t.Fatalf("a version with nothing left is estimated at %f", clean)
	}
	f.exec(`UPDATE feature SET membership_version=1`)
	behind := cost()
	if behind <= 0 {
		t.Fatal("features behind are not estimated")
	}
	f.exec(`INSERT INTO pr_summary(repository, number, content_hash, summary, created_at) SELECT repository, number, 'h', 's', 0 FROM pull_request`)
	if summarized := cost(); summarized >= behind {
		t.Fatalf("pull requests already summarized are estimated again: %f, %f", summarized, behind)
	}
}

// A failed call adds its cost to the version's spend when it reported one,
// and nothing when its usage is unknown.
func TestBuildChargesFailedCalls(t *testing.T) {
	for _, tc := range []struct {
		usage agent.Usage
		paid  bool
	}{{agent.Usage{Input: 1000, Output: 100}, true}, {agent.Usage{Input: 1000, Output: 100, Unreported: true}, false}} {
		f := newFeatureFixture(t)
		f.clock.now = t0.Add(60 * 24 * time.Hour)
		f.history(1)
		f.backend.usage = &tc.usage
		f.backend.batch = func(string) (string, error) { return "", errors.New("refused") }
		report := f.build(BuildOptions{})
		if !report.Current || report.LeftOut != 1 || (report.VersionSpentUSD > 0) != tc.paid {
			t.Fatalf("usage %+v: report = %+v", tc.usage, report)
		}
	}
}

// A build stops when its model cannot be priced.
func TestBuildStopReasons(t *testing.T) {
	f := newFeatureFixture(t)
	f.clock.now = t0.Add(60 * 24 * time.Hour)
	f.history(50)
	f.backend.batch = func(user string) (string, error) { return batchReply(t, user, ""), nil }
	if report := f.build(BuildOptions{Model: "made-up-model"}); report.Stopped != ReasonUnpriced || f.backend.calls["batch"] != 0 {
		t.Fatalf("unpriced = %+v", report)
	}
}

// A live run whose version a build's switch retired stops at once, without
// backing off: a placement's write and a text's write.
func TestLiveRunStopsOnARetiredVersion(t *testing.T) {
	f := newFeatureFixture(t)
	f.clock.now = t0.Add(time.Hour)
	live := f.live()
	f.pull(1, "One", f.clock.now.Add(-30*time.Minute), "h1", []string{"a.go"})
	f.pull(2, "Two", f.clock.now.Add(-20*time.Minute), "h2", []string{"a.go"})
	retire := func() {
		building, err := featureVersion(context.Background(), f.store.db, repo, VersionBuilding, true, f.clock.now)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.store.switchVersion(context.Background(), repo, building, f.clock.now); err != nil {
			t.Fatal(err)
		}
	}
	assign := f.backend.assign
	f.backend.assign = func(user string) (string, error) {
		retire()
		return assign(user)
	}
	report := f.assign(FeatureOptions{Manual: true})
	if report.Stopped != ReasonVersionRetired || f.backend.calls["assign"] != 1 || f.featureOf(1) != "" {
		t.Fatalf("report = %+v", report)
	}
	var rows int
	_ = f.store.db.QueryRow(`SELECT COUNT(*) FROM feature_settle WHERE version_id=?`, live).Scan(&rows)
	if rows != 0 {
		t.Fatal("the item backed off on the retired version")
	}

	// A text written while its version is retired.
	g := newFeatureFixture(t)
	version := g.live()
	g.pull(1, "One", t0, "h1", []string{"a.go"}, "A description.")
	g.feature(version, "A", 1)
	g.exec(`UPDATE feature SET membership_version=1`)
	text := g.backend.text
	g.backend.text = func() (string, error) {
		building, _ := featureVersion(context.Background(), g.store.db, repo, VersionBuilding, true, g.clock.now)
		if err := g.store.switchVersion(context.Background(), repo, building, g.clock.now); err != nil {
			t.Fatal(err)
		}
		return text()
	}
	if got := g.summarize(FeatureOptions{Manual: true}); got.Stopped != ReasonVersionRetired {
		t.Fatalf("text = %+v", got)
	}
}

// A refused split call writes nothing, the merges it follows included.
func TestConsolidationRefusedSplitWritesNothing(t *testing.T) {
	f := newFeatureFixture(t)
	version := f.live()
	var numbers []int
	for i := 1; i <= 31; i++ {
		f.pull(i, fmt.Sprintf("Change %d", i), t0, fmt.Sprintf("h%d", i), []string{"a.go"})
		numbers = append(numbers, i)
	}
	f.pull(40, "Other", t0, "h40", []string{"a.go"})
	f.feature(version, "A", numbers...)
	f.feature(version, "B", 40)
	f.backend.merge = func(user string) (string, error) {
		refs := mergeRefs(t, user)
		f.off = true
		return mergeReply(refs["Feature A"], refs["Feature B"]), nil
	}
	got := f.consolidate(FeatureOptions{})
	if got.Stopped != ReasonDisabled || got.DiscardedMerges != 1 || got.Merged != 0 || f.featureOf(40) != "B" || f.backend.calls["split"] != 0 {
		t.Fatalf("report = %+v", got)
	}
}

// A weekly pass whose version a switch retired between its calls writes
// and records nothing; its calls ask for the local reply bound.
func TestConsolidationRetiredBetweenCalls(t *testing.T) {
	f := newFeatureFixture(t)
	version := f.live()
	f.pull(1, "One", t0, "h1", []string{"a.go"})
	f.pull(2, "Two", t0, "h2", []string{"a.go"})
	f.feature(version, "A", 1)
	f.feature(version, "B", 2)
	f.backend.merge = func(user string) (string, error) {
		refs := mergeRefs(t, user)
		building, _ := featureVersion(context.Background(), f.store.db, repo, VersionBuilding, true, f.clock.now)
		if err := f.store.switchVersion(context.Background(), repo, building, f.clock.now); err != nil {
			t.Fatal(err)
		}
		return mergeReply(refs["Feature A"], refs["Feature B"]), nil
	}
	got := f.consolidate(FeatureOptions{})
	if got.Stopped != ReasonVersionRetired || got.Merged != 0 || f.featureOf(2) != "B" {
		t.Fatalf("report = %+v", got)
	}
	if last, _, _ := f.store.consolidation(context.Background(), repo); !last.IsZero() {
		t.Fatal("a pass over a retired version is recorded")
	}
	if len(f.backend.maxOutput) != 1 || f.backend.maxOutput[0] != FeatureConsolidateOutputTokens {
		t.Fatalf("reply bounds = %v", f.backend.maxOutput)
	}
}

// The live placement checks its version before each item: one retired
// during an item that settles without a write stops before the next.
func TestPlacementChecksItsVersionBeforeEachItem(t *testing.T) {
	f := newFeatureFixture(t)
	f.clock.now = t0.Add(time.Hour)
	f.pull(1, "One", f.clock.now.Add(-30*time.Minute), "h1", []string{"a.go"})
	f.pull(2, "Two", f.clock.now.Add(-20*time.Minute), "h2", []string{"a.go"})
	f.live()
	f.backend.assign = func(string) (string, error) {
		building, _ := featureVersion(context.Background(), f.store.db, repo, VersionBuilding, true, f.clock.now)
		if err := f.store.switchVersion(context.Background(), repo, building, f.clock.now); err != nil {
			t.Fatal(err)
		}
		return choose("skip", "", ""), nil
	}
	if report := f.assign(FeatureOptions{Manual: true}); report.Stopped != ReasonVersionRetired || f.backend.calls["assign"] != 1 || len(report.Items) != 1 {
		t.Fatalf("report = %+v", report)
	}
}

// A build stopped in its last text pass, no pull request left, resumes and
// writes the text left.
func TestBuildResumedInItsLastTextPass(t *testing.T) {
	f := newFeatureFixture(t)
	f.clock.now = t0.Add(60 * 24 * time.Hour)
	version, err := featureVersion(context.Background(), f.store.db, repo, VersionBuilding, true, f.clock.now)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		f.pull(i, fmt.Sprintf("Change %d", i), f.clock.now.Add(-time.Hour), fmt.Sprintf("h%d", i), []string{"a.go"}, "A description.")
		f.feature(version, fmt.Sprintf("F%d", i), i)
	}
	f.exec(`UPDATE feature SET membership_version=1`)
	f.exec(`UPDATE feature_version SET spent_usd=0.5 WHERE id=?`, version)
	estimate, err := f.worker.EstimateBuild(context.Background(), repo, "", BuildOptions{})
	if err != nil || estimate.PullRequests != 0 || estimate.EstimateUSD == nil || *estimate.EstimateUSD <= 0 {
		t.Fatalf("estimate = %+v, %v", estimate, err)
	}
	report := f.build(BuildOptions{})
	if !report.Current || f.backend.calls["text"] != 3 || f.backend.calls["merge"] != 0 {
		t.Fatalf("report = %+v, calls %v", report, f.backend.calls)
	}
}

// Merge, split and the pass's record write nothing to a retired version.
func TestConsolidationWritesNothingToARetiredVersion(t *testing.T) {
	f := newFeatureFixture(t)
	version := f.live()
	for i := 1; i <= 3; i++ {
		f.pull(i, fmt.Sprintf("Change %d", i), t0, fmt.Sprintf("h%d", i), []string{"a.go"})
	}
	f.feature(version, "A", 1, 2)
	f.feature(version, "B", 3)
	f.exec(`UPDATE feature_version SET state=? WHERE id=?`, VersionRetired, version)
	ctx := context.Background()
	expected := map[string]int64{"A": 0, "B": 0}
	if done, err := f.store.mergeFeature(ctx, version, repo, "A", "B", expected, t0); err != nil || done {
		t.Fatalf("merge = %v, %v", done, err)
	}
	pulls := []consolidationPull{{Number: 1}, {Number: 2}}
	partition := consolidate.Partition{Parts: []consolidate.Part{{Title: "One", Description: "One.", PullRequests: []int{0}},
		{Title: "Two", Description: "Two.", PullRequests: []int{1}}}}
	if created, err := f.store.splitFeature(ctx, version, repo, "A", pulls, partition, expected, t0); err != nil || created != -1 {
		t.Fatalf("split = %d, %v", created, err)
	}
	if err := recordConsolidation(ctx, f.store.db, version, repo, "fingerprint", t0); err != nil {
		t.Fatal(err)
	}
	if last, _, _ := f.store.consolidation(ctx, repo); !last.IsZero() || f.featureOf(3) != "B" || f.featureOf(2) != "A" {
		t.Fatal("a retired version was written")
	}
}
