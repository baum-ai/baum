package decisionlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
)

// batchReply starts one feature with the batch's first item and joins the
// rest to it, unless join names a candidate ref every item joins.
func batchReply(t *testing.T, user, join string) string {
	var input struct {
		Items []struct{ ID string } `json:"items"`
	}
	if err := json.Unmarshal([]byte(user), &input); err != nil {
		t.Fatal(err)
	}
	var out []string
	for i, item := range input.Items {
		switch {
		case join != "":
			out = append(out, fmt.Sprintf(`{"item":%q,"reason":"r","choice":"existing","feature_id":%q,"new_id":"","title":"","description":""}`, item.ID, join))
		case i == 0:
			out = append(out, fmt.Sprintf(`{"item":%q,"reason":"r","choice":"new","feature_id":"","new_id":"N1","title":"Batch %s","description":"A batch."}`,
				item.ID, item.ID))
		default:
			out = append(out, fmt.Sprintf(`{"item":%q,"reason":"r","choice":"existing","feature_id":"N1","new_id":"","title":"","description":""}`, item.ID))
		}
	}
	return `{"assignments":[` + strings.Join(out, ",") + `]}`
}

// history records n merged pull requests opened a day apart, ending now.
func (f *featureFixture) history(n int) {
	f.t.Helper()
	for i := 1; i <= n; i++ {
		f.pull(i, fmt.Sprintf("Change %d", i), f.clock.now.Add(-time.Duration(n-i+1)*24*time.Hour), fmt.Sprintf("h%d", i), []string{fmt.Sprintf("p%d.go", i)})
	}
	f.exec(`UPDATE pull_request SET state='closed', merged_at=created_at`)
}

func (f *featureFixture) build(o BuildOptions) BuildReport {
	f.t.Helper()
	report, err := f.worker.Build(context.Background(), repo, f.t.TempDir(), o)
	if err != nil {
		f.t.Fatal(err)
	}
	return report
}

func (f *featureFixture) versionState(id int64) (state, stopped string) {
	f.t.Helper()
	if err := f.store.db.QueryRow(`SELECT state, stopped FROM feature_version WHERE id=?`, id).Scan(&state, &stopped); err != nil {
		f.t.Fatal(err)
	}
	return state, stopped
}

// A build places the whole history's merged and open pull requests by no bot in
// batches, oldest first, writes the text every three batches and after the
// last, and switches versions in one step; the live placement keeps writing
// the current version meanwhile, and the weekly pass consolidates the new
// version on its next run though the old one had a recent pass and a
// backoff.
func TestBuildPlacesTextsAndSwitches(t *testing.T) {
	f := newFeatureFixture(t)
	f.clock.now = t0.Add(200 * 24 * time.Hour)
	f.history(110)
	f.pull(200, "By a bot", f.clock.now.Add(-time.Hour), "hb", []string{"a.go"})
	f.pull(201, "Closed unmerged", f.clock.now.Add(-time.Hour), "hc", []string{"a.go"})
	f.pull(202, "Older than the first sync's window", f.clock.now.Add(-400*24*time.Hour), "ho", []string{"a.go"})
	f.exec(`UPDATE pull_request SET bot=1 WHERE number=200`)
	f.exec(`UPDATE pull_request SET state='closed', merged_at=NULL WHERE number=201`)
	f.exec(`UPDATE pull_request SET merged_at=created_at, state='closed' WHERE number=202`)
	live := f.live()
	// The old version's recent pass and a backoff.
	f.exec(`INSERT INTO feature_consolidation(repository, last_pass_at, fingerprint) VALUES (?, ?, 'old')`, repo, f.clock.now.Add(-24*time.Hour).UnixMilli())
	f.exec(`INSERT INTO feature_settle(version_id, repository, kind, item, trigger_key, failures, next_at) VALUES (?, ?, ?, ?, 'old', 1, ?)`,
		live, repo, kindConsolidation, consolidationItem, f.clock.now.Add(time.Hour).UnixMilli())
	var order []string
	livePlaced := false
	f.backend.batch = func(user string) (string, error) {
		order = append(order, "batch")
		if !livePlaced {
			// The live placement runs during the build, on the current
			// version.
			livePlaced = true
			f.pull(300, "Live work", f.clock.now.Add(-time.Hour), "hl", []string{"l.go"})
			if _, err := f.worker.Assign(context.Background(), repo, f.t.TempDir(), FeatureOptions{Manual: true}); err != nil {
				t.Fatal(err)
			}
		}
		return batchReply(t, user, ""), nil
	}
	text := f.backend.text
	f.backend.text = func() (string, error) {
		if len(order) == 0 || order[len(order)-1] != "text" {
			order = append(order, "text")
		}
		return text()
	}
	f.backend.merge = func(string) (string, error) { order = append(order, "merge"); return `{"groups":[]}`, nil }
	// The first batch's feature spans the old pull request: an invalid split
	// leaves it whole.
	f.backend.split = func(string) (string, error) { return `{"parts":[]}`, nil }
	estimate, err := f.worker.EstimateBuild(context.Background(), repo, "", BuildOptions{})
	if err != nil || estimate.PullRequests != 111 || estimate.Batches != 5 || estimate.EstimateUSD == nil || *estimate.EstimateUSD <= 0 {
		t.Fatalf("estimate = %+v, %v", estimate, err)
	}
	report := f.build(BuildOptions{})
	if !report.Current || report.Placed != 111 || report.Created != 5 || report.PullRequests != 111 || report.LeftOut != 0 {
		t.Fatalf("report = %+v", report)
	}
	if want := []string{"batch", "batch", "batch", "text", "batch", "batch", "text"}; !slices.Equal(order, want) {
		t.Fatalf("calls = %v, want %v", order, want)
	}
	if state, _ := f.versionState(live); state != VersionRetired {
		t.Fatalf("the old version is %s", state)
	}
	if state, _ := f.versionState(report.Version); state != VersionCurrent {
		t.Fatalf("the built version is %s", state)
	}
	var liveFeature string
	if err := f.store.db.QueryRow(`SELECT feature_id FROM feature_pull WHERE version_id=? AND number=300`, live).Scan(&liveFeature); err != nil {
		t.Fatalf("the live placement did not write the current version: %v", err)
	}
	resolved, err := f.store.ResolveFeature(context.Background(), liveFeature)
	if err != nil || !resolved.Archived {
		t.Fatalf("a feature of the retired version resolves to %+v, %v", resolved, err)
	}
	// The weekly pass, due though one ran this week, consolidates the new
	// version on its next run.
	if pass := f.consolidate(FeatureOptions{}); pass.Outcome != ConsolidationDone || pass.Features != report.Features || f.backend.calls["merge"] != 1 {
		t.Fatalf("the next weekly pass = %+v", pass)
	}
	if f.featureOf(202) == "" {
		t.Fatal("a pull request older than the first sync's window was left out")
	}
	var excluded int
	_ = f.store.db.QueryRow(`SELECT COUNT(*) FROM feature_pull WHERE version_id=? AND number IN (200, 201, 300)`, report.Version).Scan(&excluded)
	if excluded != 0 {
		t.Fatal("the build placed a pull request by a bot, closed unmerged or opened after it listed")
	}
}

// A failed batch is made once more, then split in halves down to one pull
// request, which is tried once more after the last batch and then left out;
// the build still finishes, and the one left out is no longer settled.
func TestBuildRetriesSplitsAndLeavesOut(t *testing.T) {
	f := newFeatureFixture(t)
	f.clock.now = t0.Add(30 * 24 * time.Hour)
	f.history(10)
	f.backend.merge = func(string) (string, error) { return `{"groups":[]}`, nil }
	f.backend.batch = func(user string) (string, error) {
		if strings.Contains(user, `"number":7,`) {
			return "", errors.New("refused")
		}
		return batchReply(t, user, ""), nil
	}
	report := f.build(BuildOptions{})
	if !report.Current || report.Failed != 1 || report.LeftOut != 1 || report.Placed != 9 || f.featureOf(7) != "" {
		t.Fatalf("report = %+v", report)
	}
	// 10, 5, 2, 1 twice each and the last retry.
	if f.backend.calls["batch"] < 9 {
		t.Fatalf("batch calls = %d", f.backend.calls["batch"])
	}
	var rows int
	_ = f.store.db.QueryRow(`SELECT COUNT(*) FROM feature_settle WHERE version_id=? AND outcome=?`, report.Version, outcomeLeftOut).Scan(&rows)
	if rows != 0 {
		t.Fatal("the pull request left out is still settled once the version is current")
	}
}

// A gated build, the job's, reads the gate before every call: a repository
// turned off during the build stops it, its version still building. Built
// again once on, it resumes the version and adds to its spend.
func TestGatedBuildStopsWhenTurnedOffAndResumes(t *testing.T) {
	f := newFeatureFixture(t)
	f.clock.now = t0.Add(60 * 24 * time.Hour)
	f.history(50)
	f.backend.usage = &agent.Usage{Input: 1000, Output: 1000}
	f.backend.merge = func(string) (string, error) { return `{"groups":[]}`, nil }
	f.backend.batch = func(user string) (string, error) {
		f.off = true
		return batchReply(t, user, ""), nil
	}
	report := f.build(BuildOptions{Gated: true})
	if report.Current || report.Stopped != ReasonDisabled || report.Placed != 25 || f.backend.calls["batch"] != 1 || report.SpentUSD <= 0 {
		t.Fatalf("report = %+v", report)
	}
	if state, stopped := f.versionState(report.Version); state != VersionBuilding || stopped != ReasonDisabled {
		t.Fatalf("version %s, stopped %q", state, stopped)
	}
	if again := f.build(BuildOptions{Gated: true}); again.Stopped != ReasonDisabled || f.backend.calls["batch"] != 1 {
		t.Fatalf("a build of a repository turned off = %+v", again)
	}
	f.off = false
	f.backend.batch = func(user string) (string, error) { return batchReply(t, user, ""), nil }
	second := f.build(BuildOptions{Gated: true})
	if !second.Current || second.Version != report.Version || second.Placed != 25 || second.PullRequests != 25 {
		t.Fatalf("second = %+v", second)
	}
	if want := report.SpentUSD + second.SpentUSD; second.VersionSpentUSD < want-1e-9 || second.VersionSpentUSD > want+1e-9 {
		t.Fatalf("version spend %f, want %f", second.VersionSpentUSD, want)
	}
	if state, stopped := f.versionState(report.Version); state != VersionCurrent || stopped != "" {
		t.Fatalf("version %s, stopped %q", state, stopped)
	}
}
