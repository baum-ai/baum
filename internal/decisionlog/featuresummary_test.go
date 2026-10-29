package decisionlog

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/decisions"
)

func (f *featureFixture) summarize(o FeatureOptions) SummaryReport {
	f.t.Helper()
	report, err := f.worker.Summarize(context.Background(), repo, f.t.TempDir(), o)
	if err != nil {
		f.t.Fatal(err)
	}
	return report
}

// placed places pull request 1, described, with a linked session's
// decision, and returns its feature.
func (f *featureFixture) placed() string {
	f.t.Helper()
	f.clock.now = t0.Add(time.Hour)
	f.pull(1, "Export widgets", t0.Add(time.Minute), "h1", []string{"export.go"}, "Adds a CSV export.")
	f.saveSession("s1", "export.go")
	f.exec(`INSERT INTO pull_commit(repository, number, sha, committed_at) VALUES (?, 1, 'c1', 0)`, repo)
	f.exec(`INSERT INTO session_commit(agent, session_id, repository, sha, method, confidence) VALUES ('claude-code','s1',?,'c1','entire',1)`, repo)
	f.assign(FeatureOptions{})
	return f.featureOf(1)
}

func (f *featureFixture) text(id string) (title string, keys string, clean bool) {
	f.t.Helper()
	var summarized, membership int
	if err := f.store.db.QueryRow(`SELECT title, key_decisions_json, summarized_version, membership_version FROM feature WHERE id=?`, id).
		Scan(&title, &keys, &summarized, &membership); err != nil {
		f.t.Fatal(err)
	}
	return title, keys, summarized == membership
}

// A feature's text waits for its members to be quiet, is written from its
// pull requests (a generated summary standing in for a missing review
// summary) and attributed decisions, and kept without a call while its
// input is unchanged; a generated pull request summary is reused too.
func TestFeatureTextDebounceReuseAndPullSummaries(t *testing.T) {
	f := newFeatureFixture(t)
	feature := f.placed()
	if report := f.summarize(FeatureOptions{}); len(report.Features) != 0 {
		t.Fatalf("summarized before the debounce: %+v", report)
	}
	f.clock.now = f.clock.now.Add(FeatureQuiet)
	report := f.summarize(FeatureOptions{})
	if len(report.Features) != 1 || report.Features[0].Outcome != FeatureSummarized || report.Features[0].PullSummaries != 1 ||
		report.Features[0].Decisions != 1 || report.Spent <= 0 {
		t.Fatalf("report = %+v", report)
	}
	title, keys, clean := f.text(feature)
	if title != "Widget export" || keys != `[{"id":"d-s1","agent":"claude-code","session":"s1","turn":1,"ordinal":0}]` || !clean {
		t.Fatalf("text %q %s clean %v", title, keys, clean)
	}

	// Marked with nothing changed: no call at all.
	f.markAll(feature)
	report = f.summarize(FeatureOptions{Manual: true})
	if report.Features[0].Outcome != FeatureReused || report.Features[0].PullSummariesReused != 1 || f.backend.calls["text"] != 1 || f.backend.calls["pull"] != 1 {
		t.Fatalf("report = %+v, calls %v", report, f.backend.calls)
	}
	if _, _, clean := f.text(feature); !clean {
		t.Fatal("a reused text left the feature behind")
	}

	// A changed description: a new pull request summary and a new text,
	// which reads the current one.
	f.exec(`UPDATE pull_request SET body='Adds a CSV and JSON export.' WHERE number=1`)
	f.markAll(feature)
	f.backend.text = func() (string, error) {
		return `{"title":"Widget export","description":"Exports widgets.","digest":"D.","key_decisions":[]}`, nil
	}
	report = f.summarize(FeatureOptions{Manual: true})
	if report.Features[0].Outcome != FeatureSummarized || f.backend.calls["pull"] != 2 || f.backend.calls["text"] != 2 {
		t.Fatalf("report = %+v, calls %v", report, f.backend.calls)
	}
}

// Features' text is written concurrently: a second feature's calls do not
// wait for the first's.
func TestFeatureTextConcurrent(t *testing.T) {
	f := newFeatureFixture(t)
	f.clock.now = t0.Add(time.Hour)
	f.pull(1, "Export widgets", t0.Add(time.Minute), "h1", []string{"export.go"}, "Adds a CSV export.")
	f.pull(2, "Import widgets", t0.Add(2*time.Minute), "h2", []string{"import.go"}, "Adds a CSV import.")
	f.assign(FeatureOptions{})
	if f.featureOf(1) == f.featureOf(2) {
		t.Fatal("one feature for both pull requests")
	}
	// The first call returns only once a second one started, which one
	// feature at a time never does before the deadline.
	var arrived atomic.Int32
	f.backend.wait = func() {
		arrived.Add(1)
		deadline := time.Now().Add(5 * time.Second)
		for arrived.Load() < 2 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	}
	started := time.Now()
	report := f.summarize(FeatureOptions{Manual: true})
	if time.Since(started) > 4*time.Second {
		t.Fatal("the features' calls ran one after another")
	}
	if len(report.Features) != 2 || report.Features[0].Outcome != FeatureSummarized || report.Features[1].Outcome != FeatureSummarized ||
		report.Features[0].PullRequests[0] != 1 || f.backend.calls["text"] != 2 || f.backend.calls["pull"] != 2 {
		t.Fatalf("report = %+v, calls %v", report, f.backend.calls)
	}
}

// markAll marks a feature changed now and lets the debounce pass.
func (f *featureFixture) markAll(id string) {
	f.t.Helper()
	tx, err := f.store.db.Begin()
	if err != nil {
		f.t.Fatal(err)
	}
	if err := markFeatures(context.Background(), tx, []string{id}, f.clock.now); err != nil {
		f.t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		f.t.Fatal(err)
	}
}

// Text is stored only when the feature's members did not change during the
// call; otherwise it stays behind for the next run.
func TestFeatureTextStoredOnlyIfUnchanged(t *testing.T) {
	f := newFeatureFixture(t)
	feature := f.placed()
	f.backend.onCall = func() { f.markAll(feature) }
	report := f.summarize(FeatureOptions{Manual: true})
	if report.Features[0].Outcome != FeatureStale {
		t.Fatalf("report = %+v", report)
	}
	if title, _, clean := f.text(feature); clean || title != "Widgets" {
		t.Fatalf("text %q clean %v", title, clean)
	}
	f.backend.onCall = nil
	if report = f.summarize(FeatureOptions{Manual: true}); report.Features[0].Outcome != FeatureSummarized {
		t.Fatalf("report = %+v", report)
	}
}

// The feature call reads the repository and the current text, never a
// session's prompts; a failed call backs off.
func TestFeatureTextInputAndFailures(t *testing.T) {
	f := newFeatureFixture(t)
	f.placed()
	f.backend.text = func() (string, error) { return "", context.DeadlineExceeded }
	report := f.summarize(FeatureOptions{Manual: true})
	if report.Features[0].Outcome != FeatureDeferred {
		t.Fatalf("report = %+v", report)
	}
	for _, want := range []string{`"repository":"acme/widgets"`, `"current":{"title":"Widgets"`, `Export as CSV.`, `Adds an export.`} {
		if !strings.Contains(f.backend.textInput, want) {
			t.Fatalf("the input lacks %s: %s", want, f.backend.textInput)
		}
	}
	if report = f.summarize(FeatureOptions{Manual: true}); len(report.Features) != 0 {
		t.Fatalf("taken while backing off: %+v", report)
	}
	f.clock.now = f.clock.now.Add(FeatureBackoff + time.Second)
	f.backend.text = func() (string, error) {
		return `{"title":"Widget export","description":"Exports widgets.","digest":"D.","key_decisions":[]}`, nil
	}
	if report = f.summarize(FeatureOptions{Manual: true}); report.Features[0].Outcome != FeatureSummarized {
		t.Fatalf("report = %+v", report)
	}
}

// Decisions extracted again under new IDs keep the text without a call, as
// they are hashed by session and first cited turn, and a key decision takes
// its new ID; a feature gone since is skipped.
func TestFeatureTextHashesDecisionsByAddress(t *testing.T) {
	f := newFeatureFixture(t)
	feature := f.placed()
	f.summarize(FeatureOptions{Manual: true})
	if _, keys, _ := f.text(feature); !strings.Contains(keys, `"id":"d-s1"`) {
		t.Fatalf("key decisions %s", keys)
	}
	d := decisions.Decision{ID: "renamed", Summary: "Export as CSV.", Category: decisions.CategoryImplementation, Who: decisions.WhoHuman,
		Turns: []int{1}, Files: []string{"export.go"}}
	if err := f.store.Save(context.Background(), Extraction{Meta: Meta{Agent: "claude-code", ID: "s1", Repository: repo},
		ContentHash: "c-s1b", Decisions: []decisions.Decision{d}}); err != nil {
		t.Fatal(err)
	}
	if _, _, clean := f.text(feature); clean {
		t.Fatal("new decisions did not mark the feature")
	}
	report := f.summarize(FeatureOptions{Manual: true})
	if report.Features[0].Outcome != FeatureReused || f.backend.calls["text"] != 1 || report.Features[0].KeyDecisions != 1 {
		t.Fatalf("report = %+v, calls %v", report, f.backend.calls)
	}
	if _, keys, clean := f.text(feature); keys != `[{"id":"renamed","agent":"claude-code","session":"s1","turn":1,"ordinal":0}]` || !clean {
		t.Fatalf("key decisions %s, clean %v", keys, clean)
	}
	r, err := f.worker.newRun(context.Background(), repo, "", FeatureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := r.summarize(context.Background(), "gone"); err != nil || out.Outcome != FeatureSkipped {
		t.Fatalf("a missing feature: %+v, %v", out, err)
	}
}

// A key decision keeps its ID while it is current; extracted again under
// new IDs, it takes the one at its address and place, not the first there.
func TestFeatureTextKeepsTheNamedKeyDecision(t *testing.T) {
	f := newFeatureFixture(t)
	feature := f.placed()
	save := func(first, second, hash string) {
		t.Helper()
		var list []decisions.Decision
		for _, id := range []string{first, second} {
			list = append(list, decisions.Decision{ID: id, Summary: "Choice " + id[1:], Category: decisions.CategoryImplementation,
				Who: decisions.WhoHuman, Turns: []int{1}, Files: []string{"export.go"}})
		}
		if err := f.store.Save(context.Background(), Extraction{Meta: Meta{Agent: "claude-code", ID: "s1", Repository: repo},
			ContentHash: hash, Decisions: list}); err != nil {
			t.Fatal(err)
		}
	}
	save("a1", "a2", "h-a")
	f.backend.text = func() (string, error) {
		return `{"title":"Widget export","description":"Exports widgets.","digest":"D.","key_decisions":["D2"]}`, nil
	}
	f.summarize(FeatureOptions{Manual: true})
	if _, keys, _ := f.text(feature); keys != `[{"id":"a2","agent":"claude-code","session":"s1","turn":1,"ordinal":1}]` {
		t.Fatalf("keys %s", keys)
	}
	f.markAll(feature)
	if report := f.summarize(FeatureOptions{Manual: true}); report.Features[0].Outcome != FeatureReused {
		t.Fatalf("report = %+v", report)
	}
	save("b1", "b2", "h-b")
	if report := f.summarize(FeatureOptions{Manual: true}); report.Features[0].Outcome != FeatureReused || f.backend.calls["text"] != 1 {
		t.Fatalf("report = %+v, calls %v", report, f.backend.calls)
	}
	if _, keys, _ := f.text(feature); !strings.Contains(keys, `"id":"b2"`) {
		t.Fatalf("keys %s", keys)
	}
	// The earlier form, bare IDs, still reads.
	f.exec(`UPDATE feature SET key_decisions_json='["b2"]' WHERE id=?`, feature)
	f.markAll(feature)
	if report := f.summarize(FeatureOptions{Manual: true}); len(report.Features) != 1 || report.Features[0].Error != "" {
		t.Fatalf("report = %+v", report)
	}
}

// The gate is read before every call: turned off after a feature's first
// pull request summary, it makes no further call.
func TestFeatureTextRereadsTheGateBeforeEachCall(t *testing.T) {
	f := newFeatureFixture(t)
	f.clock.now = t0.Add(time.Hour)
	f.pull(1, "Export widgets", t0.Add(time.Minute), "h1", []string{"export.go"}, "Adds a CSV export.")
	f.pull(2, "Export follow-up", t0.Add(2*time.Minute), "h2", []string{"export.go"}, "Fixes the export.")
	n := 0
	f.backend.assign = func(user string) (string, error) {
		if n++; n == 1 {
			return choose("new", "", "Widgets"), nil
		}
		return choose("existing", "F1", ""), nil
	}
	f.assign(FeatureOptions{})
	f.backend.onCall = func() { f.off = true }
	f.clock.now = f.clock.now.Add(FeatureQuiet)
	report := f.summarize(FeatureOptions{})
	if report.Stopped != ReasonDisabled || f.backend.calls["pull"] != 1 || f.backend.calls["text"] != 0 {
		t.Fatalf("report = %+v, calls %v", report, f.backend.calls)
	}
}
