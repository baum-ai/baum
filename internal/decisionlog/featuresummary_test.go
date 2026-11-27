package decisionlog

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/decisions"
	"github.com/baum-ai/baum/features/summary"
	"github.com/baum-ai/baum/internal/config"
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
// input is unchanged; no per-PR model calls are generated.
func TestFeatureTextDebounceReuseWithoutPullSummaryFanout(t *testing.T) {
	f := newFeatureFixture(t)
	feature := f.placed()
	if report := f.summarize(FeatureOptions{}); len(report.Features) != 0 {
		t.Fatalf("summarized before the debounce: %+v", report)
	}
	f.clock.now = f.clock.now.Add(FeatureQuiet)
	report := f.summarize(FeatureOptions{})
	if len(report.Features) != 1 || report.Features[0].Outcome != FeatureSummarized || report.Features[0].PullSummaries != 0 ||
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
	if report.Features[0].Outcome != FeatureReused || report.Features[0].PullSummariesReused != 0 || f.backend.calls["text"] != 1 || f.backend.calls["pull"] != 0 {
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
	if report.Features[0].Outcome != FeatureSummarized || f.backend.calls["pull"] != 0 || f.backend.calls["text"] != 2 {
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
	// The first two calls return only once both started, which one feature
	// at a time never does.
	var arrived atomic.Int32
	together := make(chan struct{})
	f.backend.wait = func() {
		switch arrived.Add(1) {
		case 1:
			select {
			case <-together:
			case <-time.After(5 * time.Second):
				t.Error("the features' calls ran one after another")
			}
		case 2:
			close(together)
		}
	}
	report := f.summarize(FeatureOptions{Manual: true})
	var pulls []int
	for _, out := range report.Features {
		if out.Outcome != FeatureSummarized {
			t.Fatalf("report = %+v", report)
		}
		pulls = append(pulls, out.PullRequests...)
	}
	slices.Sort(pulls)
	if !slices.Equal(pulls, []int{1, 2}) || f.backend.calls["text"] != 2 || f.backend.calls["pull"] != 0 {
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
	for _, want := range []string{`"repository":"acme/widgets"`, `"title":"Widgets"`, `Export as CSV.`, `Adds a CSV export.`} {
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

// The gate is read before every repository task: a disabled repository
// starts no further feature text, and live text makes no per-PR call.
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
	originalSource := f.worker.source
	prepared := 0
	f.worker.source = func(ctx context.Context, dir, repository, revision, branch string, cfg config.Config) (repositorySource, error) {
		source, err := originalSource(ctx, dir, repository, revision, branch, cfg)
		prepared++
		f.off = true
		return source, err
	}
	f.clock.now = f.clock.now.Add(FeatureQuiet)
	report := f.summarize(FeatureOptions{})
	if report.Stopped != ReasonDisabled || prepared != 1 || f.backend.calls["pull"] != 0 || f.backend.calls["text"] != 0 {
		t.Fatalf("disabled between prepare/call: %+v prepared=%d calls=%v", report, prepared, f.backend.calls)
	}

}

func TestAttributionQuantumWritesTextBeforeDrainingBacklog(t *testing.T) {
	f := newFeatureFixture(t)
	ctx := context.Background()
	f.clock.now = t0.Add(time.Hour)
	f.pull(1, "Export widgets", t0, "h1", []string{"export.go"}, "Export configuration")
	version := f.live()
	f.feature(version, "export", 1)
	f.exec(`INSERT INTO pull_commit(repository,number,sha,committed_at)VALUES(?,1,'shared',0)`, repo)
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("s%d", i)
		f.saveSession(id, "export.go")
		f.exec(`INSERT INTO session_commit(agent,session_id,repository,sha,method,confidence)VALUES('claude-code',?,?,'shared','checkpoint',1)`, id, repo)
	}
	events := []string{}
	seen := map[string]int{}
	f.backend.attribute = func(user string) (string, error) {
		events = append(events, "attribute")
		var input attributionInput
		if err := json.Unmarshal([]byte(user), &input); err != nil {
			return "", err
		}
		reply := struct {
			Decisions []decisionResolution `json:"decisions"`
		}{}
		for _, d := range input.Decisions {
			seen[d.ID]++
			reply.Decisions = append(reply.Decisions, decisionResolution{DecisionID: d.ID, FeatureID: "export", KeyEligible: true, Reason: "Explicit export configuration choice."})
		}
		data, err := json.Marshal(reply)
		return string(data), err
	}
	f.backend.text = func() (string, error) {
		events = append(events, "text")
		return `{"title":"Widget export","description":"Exports widgets.","digest":"Export controls.","key_decisions":["D1"]}`, nil
	}
	first, err := f.worker.Summarize(ctx, repo, "", FeatureOptions{Manual: true})
	if err != nil || len(first.Features) != 1 {
		t.Fatalf("first %+v %v", first, err)
	}
	if len(seen) != FeatureAttributionRunLimit || !slices.Equal(events, []string{"attribute", "text"}) {
		t.Fatalf("backlog delayed first text: seen=%v events=%v", seen, events)
	}
	second, err := f.worker.Summarize(ctx, repo, "", FeatureOptions{Manual: true})
	if err != nil || len(second.Features) != 1 {
		t.Fatalf("next %+v %v", second, err)
	}
	if len(seen) != 2*FeatureAttributionRunLimit {
		t.Fatalf("next quantum did not resume: %v", seen)
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("accepted decision %s regenerated %d times", id, count)
		}
	}
	if f.backend.calls["pull"] != 0 {
		t.Fatal("backfill generated per-PR summaries")
	}
}

// In daemon runs a feature quiet for FeatureQuiet gets its text after the
// first attribution quantum; attributing its backlog does not restart the
// debounce.
func TestAttributionQuantumDoesNotRestartSummaryDebounce(t *testing.T) {
	f := newFeatureFixture(t)
	ctx := context.Background()
	f.clock.now = t0.Add(time.Hour)
	f.pull(1, "Export widgets", t0, "h1", []string{"export.go"}, "Export configuration")
	version := f.live()
	f.feature(version, "export", 1)
	f.exec(`INSERT INTO pull_commit(repository,number,sha,committed_at)VALUES(?,1,'shared',0)`, repo)
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("s%d", i)
		f.saveSession(id, "export.go")
		f.exec(`INSERT INTO session_commit(agent,session_id,repository,sha,method,confidence)VALUES('claude-code',?,?,'shared','checkpoint',1)`, id, repo)
	}
	f.backend.attribute = func(user string) (string, error) {
		var input attributionInput
		if err := json.Unmarshal([]byte(user), &input); err != nil {
			return "", err
		}
		reply := struct {
			Decisions []decisionResolution `json:"decisions"`
		}{}
		for _, d := range input.Decisions {
			reply.Decisions = append(reply.Decisions, decisionResolution{DecisionID: d.ID, FeatureID: "export", KeyEligible: true, Reason: "Explicit export configuration choice."})
		}
		data, err := json.Marshal(reply)
		return string(data), err
	}
	f.backend.text = func() (string, error) {
		return `{"title":"Widget export","description":"Exports widgets.","digest":"Export controls.","key_decisions":["D1"]}`, nil
	}
	f.clock.now = f.clock.now.Add(FeatureQuiet + time.Minute)
	first, err := f.worker.Summarize(ctx, repo, "", FeatureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Features) != 1 || f.backend.calls["text"] != 1 {
		t.Fatalf("attribution held back the quiet feature's text: %+v calls=%v", first, f.backend.calls)
	}
	f.clock.now = f.clock.now.Add(time.Minute)
	if second, err := f.worker.Summarize(ctx, repo, "", FeatureOptions{}); err != nil || len(second.Features) != 1 {
		t.Fatalf("next quantum's text was held back: %+v %v", second, err)
	}
}

func TestLiveTextReusesOnlyCurrentGeneratedPullSummary(t *testing.T) {
	for _, change := range []string{"title", "body", "decision"} {
		t.Run(change, func(t *testing.T) {
			f := newFeatureFixture(t)
			ctx := context.Background()
			id := f.placed()
			if _, err := f.worker.AttributeDecisions(ctx, repo, "", FeatureOptions{Manual: true}); err != nil {
				t.Fatal(err)
			}
			src, err := f.store.summarySource(ctx, f.live(), repo, id)
			if err != nil {
				t.Fatal(err)
			}
			rendered, _, err := summary.RenderPullRequest(pullSummaryInput(src, src.Pulls[0]))
			if err != nil {
				t.Fatal(err)
			}
			cacheHash := summary.ContentHash(summary.Public.PullRequestVersion(SummaryVersion), rendered)
			const cached = "Previously generated export summary"
			if err = f.store.storePullSummary(ctx, repo, 1, cacheHash, cached); err != nil {
				t.Fatal(err)
			}
			f.summarize(FeatureOptions{Manual: true})
			if !strings.Contains(f.backend.textInput, cached) {
				t.Fatal("valid cached summary was not reused")
			}
			switch change {
			case "title":
				f.exec(`UPDATE pull_request SET title='New export outcome' WHERE number=1`)
			case "body":
				f.exec(`UPDATE pull_request SET body='New export behavior' WHERE number=1`)
			case "decision":
				f.exec(`UPDATE decision SET summary='Choose a different export format' WHERE decision_id='d-s1'`)
			}
			f.markAll(id)
			f.summarize(FeatureOptions{Manual: true})
			if strings.Contains(f.backend.textInput, cached) {
				t.Fatalf("stale cached text after %s: %s", change, f.backend.textInput)
			}
			if f.backend.calls["pull"] != 0 {
				t.Fatal("stale cache generated a per-PR replacement")
			}
		})
	}
}
