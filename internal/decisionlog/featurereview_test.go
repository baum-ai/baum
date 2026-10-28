package decisionlog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
)

// A build's pass that a cap refuses, here for want of room for the merge
// and the splits after it, is tried once: the build switches without
// recording it, and a resume of such a version makes no merge call. A pass
// the gate refuses waits for the next run.
func TestBuildPassTriedOnce(t *testing.T) {
	f := newFeatureFixture(t)
	f.clock.now = t0.Add(80 * 24 * time.Hour)
	f.history(70)
	// The second batch joins the first's feature, which then needs a split
	// call after the merge.
	batches := 0
	f.backend.batch = func(user string) (string, error) {
		if batches++; batches == 2 {
			return batchReply(t, user, "F1"), nil
		}
		return batchReply(t, user, ""), nil
	}
	f.backend.merge = func(string) (string, error) { return `{"groups":[]}`, nil }
	report := f.build(BuildOptions{MaxCostUSD: 0.03})
	if !report.Current || f.backend.calls["merge"] != 0 || report.Consolidation == nil || report.Consolidation.Stopped != ReasonBuildCap {
		t.Fatalf("report = %+v, merge calls %d", report, f.backend.calls["merge"])
	}
	var at int64
	if err := f.store.db.QueryRow(`SELECT consolidated_at FROM feature_version WHERE id=?`, report.Version).Scan(&at); err != nil || at != 0 {
		t.Fatalf("consolidated_at = %d, %v", at, err)
	}
	if last, _, _ := f.store.consolidation(context.Background(), repo); !last.IsZero() {
		t.Fatal("the pass is recorded")
	}

	// A version resumed after its pass was tried switches without a merge.
	g := newFeatureFixture(t)
	g.clock.now = t0.Add(60 * 24 * time.Hour)
	version, err := featureVersion(context.Background(), g.store.db, repo, VersionBuilding, true, g.clock.now)
	if err != nil {
		t.Fatal(err)
	}
	g.pull(1, "One", g.clock.now.Add(-time.Hour), "h1", []string{"a.go"})
	g.pull(2, "Two", g.clock.now.Add(-time.Hour), "h2", []string{"b.go"})
	g.feature(version, "A", 1)
	g.feature(version, "B", 2)
	g.exec(`UPDATE feature_version SET consolidated_at=0 WHERE id=?`, version)
	g.backend.merge = func(string) (string, error) { return `{"groups":[]}`, nil }
	if resumed := g.build(BuildOptions{MaxCostUSD: 1}); !resumed.Current || g.backend.calls["merge"] != 0 {
		t.Fatalf("resumed = %+v, merge calls %d", resumed, g.backend.calls["merge"])
	}
	if last, _, _ := g.store.consolidation(context.Background(), repo); !last.IsZero() {
		t.Fatal("the resumed version's untried pass is recorded")
	}

	// Turned off at the pass: the build stops and its pass waits.
	h := newFeatureFixture(t)
	h.clock.now = t0.Add(60 * 24 * time.Hour)
	h.history(50)
	h.backend.batch = func(user string) (string, error) { return batchReply(t, user, ""), nil }
	h.backend.merge = func(string) (string, error) { return `{"groups":[]}`, nil }
	calls := 0
	h.backend.onCall = func() {
		// Two batches and two texts, then the repository is turned off.
		if calls++; calls == 4 {
			h.off = true
		}
	}
	stopped := h.build(BuildOptions{Gated: true, MaxCostUSD: 1})
	var pending sql.NullInt64
	_ = h.store.db.QueryRow(`SELECT consolidated_at FROM feature_version WHERE id=?`, stopped.Version).Scan(&pending)
	if stopped.Current || stopped.Stopped != ReasonDisabled || pending.Valid {
		t.Fatalf("stopped = %+v, consolidated_at %v", stopped, pending)
	}
	h.off = false
	if again := h.build(BuildOptions{Gated: true, MaxCostUSD: 1}); !again.Current || h.backend.calls["merge"] != 1 {
		t.Fatalf("again = %+v", again)
	}
}

// A resumed version's estimate counts its features behind and their pull
// requests' summaries, and the pass only until it was tried.
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
	clean := cost()
	if clean <= 0 {
		t.Fatal("the pass is not estimated")
	}
	f.exec(`UPDATE feature SET membership_version=1`)
	behind := cost()
	if behind <= clean {
		t.Fatalf("features behind %f, clean %f", behind, clean)
	}
	f.exec(`INSERT INTO pr_summary(repository, number, content_hash, summary, created_at) SELECT repository, number, 'h', 's', 0 FROM pull_request`)
	if summarized := cost(); summarized >= behind {
		t.Fatalf("pull requests already summarized are estimated again: %f, %f", summarized, behind)
	}
	f.exec(`UPDATE feature SET membership_version=0`)
	f.exec(`UPDATE feature_version SET consolidated_at=0 WHERE id=?`, version)
	if tried := cost(); tried != 0 {
		t.Fatalf("a tried pass is estimated again: %f", tried)
	}
}

// A failed call is charged the estimate it was held to when its cost is
// unknown, and nothing when it reported no usage.
func TestBuildChargesFailedCalls(t *testing.T) {
	for _, tc := range []struct {
		usage agent.Usage
		paid  bool
	}{{agent.Usage{Input: 1000, Output: 100, Unreported: true}, true}, {agent.Usage{}, false}} {
		f := newFeatureFixture(t)
		f.clock.now = t0.Add(60 * 24 * time.Hour)
		f.history(1)
		f.backend.usage = &tc.usage
		f.backend.batch = func(string) (string, error) { return "", errors.New("refused") }
		report := f.build(BuildOptions{MaxCostUSD: 1})
		if !report.Current || report.LeftOut != 1 || (report.VersionSpentUSD > 0) != tc.paid {
			t.Fatalf("usage %+v: report = %+v", tc.usage, report)
		}
	}
}

// A build stops when its model cannot be priced, or when a call's spend
// could not be recorded.
func TestBuildStopReasons(t *testing.T) {
	f := newFeatureFixture(t)
	f.clock.now = t0.Add(60 * 24 * time.Hour)
	f.history(50)
	f.backend.batch = func(user string) (string, error) { return batchReply(t, user, ""), nil }
	if report := f.build(BuildOptions{Model: "made-up-model"}); report.Stopped != ReasonUnpriced || f.backend.calls["batch"] != 0 {
		t.Fatalf("unpriced = %+v", report)
	}
	f.exec(`CREATE TRIGGER no_spend BEFORE UPDATE OF spent_usd ON feature_version BEGIN SELECT RAISE(ABORT, 'no'); END`)
	if report := f.build(BuildOptions{MaxCostUSD: 1}); report.Stopped != ReasonSpendUnrecorded || f.backend.calls["batch"] != 1 {
		t.Fatalf("unrecorded = %+v", report)
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
		if err := f.store.switchVersion(context.Background(), repo, building, false, f.clock.now); err != nil {
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
		if err := g.store.switchVersion(context.Background(), repo, building, false, g.clock.now); err != nil {
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
		if err := f.store.switchVersion(context.Background(), repo, building, false, f.clock.now); err != nil {
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
		if err := f.store.switchVersion(context.Background(), repo, building, false, f.clock.now); err != nil {
			t.Fatal(err)
		}
		return choose("skip", "", ""), nil
	}
	if report := f.assign(FeatureOptions{Manual: true}); report.Stopped != ReasonVersionRetired || f.backend.calls["assign"] != 1 || len(report.Items) != 1 {
		t.Fatalf("report = %+v", report)
	}
}
