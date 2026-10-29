package decisionlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// feature adds a feature to version holding the pull requests numbers.
func (f *featureFixture) feature(version int64, id string, numbers ...int) {
	f.t.Helper()
	now := f.clock.now.UnixMilli()
	f.exec(`INSERT INTO feature(id, version_id, repository, title, description, changed_at, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?)`,
		id, version, repo, "Feature "+id, "Does "+id+".", now, now, now)
	for _, n := range numbers {
		f.exec(`INSERT INTO feature_pull(version_id, repository, number, feature_id, assigned_by, assigned_at, assigned_paths_json)
VALUES (?,?,?,?,'assigner',?,'["a.go"]')`, version, repo, n, id, now)
	}
}

func (f *featureFixture) live() int64 {
	f.t.Helper()
	v, err := f.store.LiveVersion(context.Background(), repo)
	if err != nil {
		f.t.Fatal(err)
	}
	return v
}

func (f *featureFixture) consolidate(o FeatureOptions) ConsolidateReport {
	f.t.Helper()
	report, err := f.worker.Consolidate(context.Background(), repo, f.t.TempDir(), o)
	if err != nil {
		f.t.Fatal(err)
	}
	return report
}

// mergeRefs names the merge input's features by title.
func mergeRefs(t *testing.T, user string) map[string]string {
	var input struct {
		Features []struct{ ID, Title string } `json:"features"`
	}
	if err := json.Unmarshal([]byte(user), &input); err != nil {
		t.Fatal(err)
	}
	refs := map[string]string{}
	for _, f := range input.Features {
		refs[f.Title] = f.ID
	}
	return refs
}

func mergeReply(survivor, duplicate string) string {
	return fmt.Sprintf(`{"groups":[{"reason":"r","survivor":%q,"duplicates":[%q]}]}`, survivor, duplicate)
}

// A pass is made at most every week, and only when the features changed.
func TestConsolidationCadence(t *testing.T) {
	f := newFeatureFixture(t)
	f.backend.merge = func(string) (string, error) { return `{"groups":[]}`, nil }
	version := f.live()
	f.pull(1, "One", t0, "h1", []string{"a.go"})
	f.pull(2, "Two", t0, "h2", []string{"b.go"})
	f.feature(version, "A", 1)
	f.feature(version, "B", 2)
	if got := f.consolidate(FeatureOptions{}); got.Outcome != ConsolidationDone || f.backend.calls["merge"] != 1 {
		t.Fatalf("first pass = %+v, merge calls %d", got, f.backend.calls["merge"])
	}
	f.clock.now = t0.Add(6 * 24 * time.Hour)
	f.exec(`UPDATE feature SET membership_version=membership_version+1 WHERE id='A'`)
	if got := f.consolidate(FeatureOptions{}); got.Outcome != ConsolidationNotDue {
		t.Fatalf("within the week: %+v", got)
	}
	f.clock.now = t0.Add(8 * 24 * time.Hour)
	if got := f.consolidate(FeatureOptions{}); got.Outcome != ConsolidationDone || f.backend.calls["merge"] != 2 {
		t.Fatalf("changed after the week: %+v", got)
	}
	f.clock.now = t0.Add(16 * 24 * time.Hour)
	if got := f.consolidate(FeatureOptions{}); got.Outcome != ConsolidationUnchanged || f.backend.calls["merge"] != 2 {
		t.Fatalf("unchanged after the week: %+v", got)
	}
}

// A merge moves the duplicate's members to the survivor as consolidation's,
// marks both and points the duplicate at the survivor, which a link to it
// resolves to.
func TestConsolidationMerge(t *testing.T) {
	f := newFeatureFixture(t)
	version := f.live()
	f.pull(1, "One", t0, "h1", []string{"a.go"})
	f.pull(2, "Two", t0, "h2", []string{"a.go"})
	f.feature(version, "A", 1)
	f.feature(version, "B", 2)
	f.exec(`INSERT INTO feature_session(version_id, repository, agent, session_id, feature_id, assigned_at) VALUES (?,?,'claude-code','s1','B',0)`, version, repo)
	f.backend.merge = func(user string) (string, error) {
		refs := mergeRefs(t, user)
		return mergeReply(refs["Feature A"], refs["Feature B"]), nil
	}
	if got := f.consolidate(FeatureOptions{Manual: true}); got.Merged != 1 || got.Changed != 0 {
		t.Fatalf("report = %+v", got)
	}
	var by, into, session string
	if err := f.store.db.QueryRow(`SELECT assigned_by FROM feature_pull WHERE number=2`).Scan(&by); err != nil || by != assignedByConsolidation || f.featureOf(2) != "A" {
		t.Fatalf("pull request 2 is in %s, by %s (%v)", f.featureOf(2), by, err)
	}
	_ = f.store.db.QueryRow(`SELECT merged_into FROM feature WHERE id='B'`).Scan(&into)
	_ = f.store.db.QueryRow(`SELECT feature_id FROM feature_session WHERE session_id='s1'`).Scan(&session)
	if into != "A" || session != "A" || f.membership("A") != 1 || f.membership("B") != 1 {
		t.Fatalf("merged_into %q, session in %q, versions %d %d", into, session, f.membership("A"), f.membership("B"))
	}
	resolved, err := f.store.ResolveFeature(context.Background(), "B")
	if err != nil || resolved.ID != "A" || resolved.Archived {
		t.Fatalf("resolved = %+v, %v", resolved, err)
	}
}

// A merge whose features changed after they were read is skipped; the pass
// still completes.
func TestConsolidationSkipsChangedMerge(t *testing.T) {
	f := newFeatureFixture(t)
	version := f.live()
	f.pull(1, "One", t0, "h1", []string{"a.go"})
	f.pull(2, "Two", t0, "h2", []string{"a.go"})
	f.feature(version, "A", 1)
	f.feature(version, "B", 2)
	f.backend.merge = func(user string) (string, error) {
		refs := mergeRefs(t, user)
		f.exec(`UPDATE feature SET membership_version=membership_version+1 WHERE id='B'`)
		return mergeReply(refs["Feature A"], refs["Feature B"]), nil
	}
	got := f.consolidate(FeatureOptions{Manual: true})
	if got.Outcome != ConsolidationDone || got.Merged != 0 || got.Changed != 1 || f.featureOf(2) != "B" {
		t.Fatalf("report = %+v, pull request 2 in %s", got, f.featureOf(2))
	}
}

// An oversized feature is split: the largest part keeps its id, the other
// becomes a new feature recording its origin, both are checked, and for the
// split hold a merge undoing it is dropped. A feature kept whole is not
// offered again until it changes.
func TestConsolidationSplit(t *testing.T) {
	f := newFeatureFixture(t)
	version := f.live()
	var numbers []int
	for i := 1; i <= 32; i++ {
		f.pull(i, fmt.Sprintf("Change %d", i), t0.Add(time.Duration(i)*time.Hour), fmt.Sprintf("h%d", i), []string{"a.go"})
		numbers = append(numbers, i)
	}
	f.feature(version, "A", numbers...)
	f.backend.merge = func(string) (string, error) { return `{"groups":[]}`, nil }
	f.backend.split = func(string) (string, error) {
		var first, second []string
		for i := 1; i <= 32; i++ {
			if i <= 12 {
				first = append(first, fmt.Sprintf("%q", fmt.Sprintf("P%d", i)))
			} else {
				second = append(second, fmt.Sprintf("%q", fmt.Sprintf("P%d", i)))
			}
		}
		return `{"parts":[{"title":"Small","description":"The small part.","pull_requests":[` + strings.Join(first, ",") +
			`]},{"title":"Large","description":"The large part.","pull_requests":[` + strings.Join(second, ",") + `]}]}`, nil
	}
	got := f.consolidate(FeatureOptions{Manual: true})
	if got.Split != 1 || got.NewFeatures != 1 || f.backend.calls["split"] != 1 {
		t.Fatalf("report = %+v", got)
	}
	part := f.featureOf(1)
	if f.featureOf(32) != "A" || part == "A" {
		t.Fatalf("the largest part does not keep the id: 32 in %s, 1 in %s", f.featureOf(32), part)
	}
	var from string
	var at, checked, membership int64
	if err := f.store.db.QueryRow(`SELECT split_from, split_at, split_checked_version, membership_version FROM feature WHERE id=?`, part).
		Scan(&from, &at, &checked, &membership); err != nil || from != "A" || at != t0.UnixMilli() || checked != membership {
		t.Fatalf("new part: from %q at %d, checked %d of %d (%v)", from, at, checked, membership, err)
	}
	// Within the hold, merging the part back is dropped.
	f.backend.merge = func(user string) (string, error) {
		refs := mergeRefs(t, user)
		return mergeReply(refs["Feature A"], refs["Small"]), nil
	}
	if got := f.consolidate(FeatureOptions{Manual: true}); got.HeldMerges != 1 || got.Merged != 0 {
		t.Fatalf("held = %+v", got)
	}
	// Kept whole: checked, and not offered again until it changes.
	f.exec(`UPDATE feature_pull SET feature_id='A' WHERE version_id=?`, version)
	f.exec(`DELETE FROM feature WHERE id=?`, part)
	f.exec(`UPDATE feature SET membership_version=membership_version+1 WHERE id='A'`)
	f.backend.merge = func(string) (string, error) { return `{"groups":[]}`, nil }
	f.backend.split = func(string) (string, error) {
		var all []string
		for i := 1; i <= 32; i++ {
			all = append(all, fmt.Sprintf("%q", fmt.Sprintf("P%d", i)))
		}
		return `{"parts":[{"title":"Whole","description":"All of it.","pull_requests":[` + strings.Join(all, ",") + `]}]}`, nil
	}
	if got := f.consolidate(FeatureOptions{Manual: true}); got.KeptWhole != 1 || f.backend.calls["split"] != 2 {
		t.Fatalf("kept whole = %+v", got)
	}
	f.consolidate(FeatureOptions{Manual: true})
	if f.backend.calls["split"] != 2 {
		t.Fatal("a feature kept whole is offered again unchanged")
	}
	f.exec(`UPDATE feature SET membership_version=membership_version+1 WHERE id='A'`)
	f.consolidate(FeatureOptions{Manual: true})
	if f.backend.calls["split"] != 3 {
		t.Fatal("a feature kept whole is not offered again once it changed")
	}
}

// A failed split call defers the whole pass: nothing is written.
func TestConsolidationFailedSplitDefersThePass(t *testing.T) {
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
		return mergeReply(refs["Feature A"], refs["Feature B"]), nil
	}
	f.backend.split = func(string) (string, error) { return "", errors.New("refused") }
	got := f.consolidate(FeatureOptions{})
	if got.Outcome != FeatureDeferred || got.Merged != 0 || f.featureOf(40) != "B" {
		t.Fatalf("report = %+v, pull request 40 in %s", got, f.featureOf(40))
	}
	if last, _, _ := f.store.consolidation(context.Background(), repo); !last.IsZero() {
		t.Fatal("a deferred pass is recorded")
	}
}

// At most three split calls a pass, the largest first; a feature too large
// to offer takes no slot, and a call whose reply is invalid takes one.
func TestConsolidationSplitSlots(t *testing.T) {
	f := newFeatureFixture(t)
	version := f.live()
	n := 0
	add := func(id string, count int) {
		var numbers []int
		for range count {
			n++
			f.pull(n, fmt.Sprintf("Change %d", n), t0, fmt.Sprintf("h%d", n), []string{"a.go"})
			numbers = append(numbers, n)
		}
		f.feature(version, id, numbers...)
	}
	add("Huge", 201)
	for _, id := range []string{"A", "B", "C", "D"} {
		add(id, 31)
	}
	f.backend.merge = func(string) (string, error) { return `{"groups":[]}`, nil }
	f.backend.split = func(string) (string, error) { return `{"parts":[]}`, nil }
	got := f.consolidate(FeatureOptions{})
	if f.backend.calls["split"] != 3 || got.InvalidSplits != 3 || got.KeptWhole != 1 {
		t.Fatalf("split calls %d, report %+v", f.backend.calls["split"], got)
	}
}

// Two parts of one split are held apart while either is within the hold.
func TestHeldApartSiblings(t *testing.T) {
	now := t0
	part := func(id string, age time.Duration) *consolidationFeature {
		return &consolidationFeature{candidateFeature: &candidateFeature{ID: id}, splitFrom: "O", splitAt: now.Add(-age)}
	}
	if !heldApart(part("a", 60*24*time.Hour), part("b", time.Hour), now) {
		t.Fatal("a recent part merges into an older sibling")
	}
	if heldApart(part("a", 60*24*time.Hour), part("b", 60*24*time.Hour), now) {
		t.Fatal("parts of an old split are held apart")
	}
	if heldApart(&consolidationFeature{candidateFeature: &candidateFeature{ID: "x"}}, &consolidationFeature{candidateFeature: &candidateFeature{ID: "y"}}, now) {
		t.Fatal("features never split are held apart")
	}
}
