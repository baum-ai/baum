package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/store"
)

// draftExecutor reports a draft, then another, then what the review was
// published without, waiting for step between them, and ends with err.
type draftExecutor struct {
	fakeExecutor
	step chan struct{}
	err  error
}

func (d *draftExecutor) Execute(ctx context.Context, p Prepared, progress func(Progress)) (*float64, error) {
	d.started <- p
	for _, report := range []Progress{
		{Phase: "generating", Calls: 1, Draft: []byte(`{"draft":1}`)},
		{Phase: "generating", Calls: 2, Draft: []byte(`{"draft":2}`)},
		{Phase: "publishing", Calls: 3, Gaps: true, FailedSections: []string{"sec_rest"}, ContextFailed: true},
	} {
		progress(report)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-d.step:
		}
	}
	return nil, d.err
}

func draftQueue(t *testing.T, err error) (*Queue, *draftExecutor) {
	t.Helper()
	d := &draftExecutor{fakeExecutor: fakeExecutor{head: "first", started: make(chan Prepared, 10)}, step: make(chan struct{}), err: err}
	q, e := Open(filepath.Join(privateDir(t), "jobs.json"), d, 1, nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(q.Close)
	return q, d
}

func readDraft(t *testing.T, handler http.Handler, id string) (int, map[string]any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("GET", "/api/jobs/"+id+"/draft", nil))
	if recorder.Code != 200 {
		return recorder.Code, nil
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return recorder.Code, body
}

// The queue holds a running staged review's latest draft in memory and
// serves it, with its count, until the job ends; then the draft is gone and
// the job records what the review was published without.
func TestQueueServesTheDraftUntilTheJobEnds(t *testing.T) {
	q, d := draftQueue(t, nil)
	handler := q.Handler()
	jobs, err := q.Generate(context.Background(), []Item{{Clone: "clone", PR: 1}}, "single", false)
	if err != nil {
		t.Fatal(err)
	}
	id := jobs[0].ID
	<-d.started
	waitDraft := func(n int) map[string]any {
		t.Helper()
		for range 3000 {
			if code, body := readDraft(t, handler, id); code == 200 && body["draft"] == float64(n) {
				return body
			}
			if j := waitState(t, q, id, "running"); j.Draft > n {
				t.Fatalf("draft %d passed", n)
			}
		}
		t.Fatalf("draft %d never served", n)
		return nil
	}
	if body := waitDraft(1); body["document"].(map[string]any)["draft"] != float64(1) {
		t.Fatalf("draft %v", body)
	}
	if j := waitState(t, q, id, "running"); j.Draft != 1 || j.Calls != 1 {
		t.Fatalf("job %+v", j)
	}
	d.step <- struct{}{}
	if body := waitDraft(2); body["document"].(map[string]any)["draft"] != float64(2) {
		t.Fatalf("draft %v", body)
	}
	d.step <- struct{}{}
	d.step <- struct{}{}
	j := waitState(t, q, id, "completed")
	if j.Draft != 0 || !slices.Equal(j.FailedSections, []string{"sec_rest"}) || !j.ContextFailed {
		t.Fatalf("job %+v", j)
	}
	if code, _ := readDraft(t, handler, id); code != 404 {
		t.Fatalf("an ended job's draft is %d", code)
	}
}

// A retry of a version's details takes the version it completes, alone; it
// stands down, cancelled, when the version changed under it or the pull
// request moved.
func TestDetailsJobsStandDown(t *testing.T) {
	q, _ := newQueue(t)
	for _, items := range [][]Item{
		{{Clone: "clone", PR: 1, Kind: KindDetails}},
		{{Clone: "clone", PR: 1, Version: 2}},
		{{Clone: "clone", PR: 1, Kind: KindDetails, Version: 1}, {Clone: "clone", PR: 2}},
	} {
		mode := "single"
		if len(items) > 1 {
			mode = "batch"
		}
		if _, err := q.Estimate(context.Background(), items, mode); err == nil {
			t.Errorf("accepted %+v", items)
		}
	}
	for _, test := range []struct {
		err    error
		reason string
	}{{ErrSuperseded, "superseded"}, {&BlockedError{ReasonHeadChanged}, ReasonHeadChanged}} {
		q, d := draftQueue(t, test.err)
		jobs, err := q.Generate(context.Background(), []Item{{Clone: "clone", PR: 1, Kind: KindDetails, Version: 1}}, "single", false)
		if err != nil {
			t.Fatal(err)
		}
		<-d.started
		for range 3 {
			d.step <- struct{}{}
		}
		if j := waitState(t, q, jobs[0].ID, "cancelled"); j.Reason != test.reason {
			t.Errorf("reason %q, want %q", j.Reason, test.reason)
		}
	}
}

// detailsFixture publishes a staged review of the fixture pull request whose
// sec_rest fails, and returns what completes it: the options, the store and
// the hook the details stage calls once before it answers.
func detailsFixture(t *testing.T) (generate.Options, *store.Store, store.Key, *func()) {
	t.Helper()
	ctx := context.Background()
	failing := true
	var before func()
	var mu sync.Mutex
	backend := &generatetest.Backend{Version: "9.9.9"}
	without := generatetest.WithoutSection("sec_rest")
	backend.RunFunc = generatetest.StagedReplies(func(stage string, request agent.Request) (agent.Result, error, bool) {
		mu.Lock()
		hook := before
		before = nil
		mu.Unlock()
		if hook != nil && stage == agent.StageDetails {
			hook()
		}
		if failing {
			return without(stage, request)
		}
		return agent.Result{}, nil, false
	})
	o := reviewOptions(t, backend)
	o.Staged, o.Budgets.MaxCalls = true, 16
	versions, err := store.Open("", t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: assembletest.Repository.String(), Number: assembletest.Number}
	local := &Local{ReviewRuns: filepath.Join(t.TempDir(), "review-runs")}
	t.Cleanup(func() { chmodTree(local.ReviewRuns) })
	result, err := local.publish(ctx, versions, versions, key, agent.BackendCursorAgent, o, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.FailedSections, []string{"sec_rest"}) {
		t.Fatalf("published without %v", result.FailedSections)
	}
	failing = false
	return o, versions, key, &before
}

// A retry writes the failed details again and updates the version in place;
// a refresh meanwhile is retried against its bytes, and a new version
// stands it down.
func TestCompleteDetailsUpdatesTheVersionInPlace(t *testing.T) {
	ctx := context.Background()
	local := &Local{}
	details := Prepared{Item: Item{Kind: KindDetails, Version: 1}}
	t.Run("in place", func(t *testing.T) {
		o, versions, key, _ := detailsFixture(t)
		saved, _ := versions.Latest(ctx, key)
		result, err := local.completeDetails(ctx, versions, nil, key, details, o)
		if err != nil {
			t.Fatal(err)
		}
		latest, _ := versions.Latest(ctx, key)
		if failed, _ := generate.Gaps(latest.Document); len(failed) != 0 || len(result.FailedSections) != 0 || latest.Metadata.GeneratedAt != saved.Metadata.GeneratedAt || latest.Metadata.Calls != saved.Metadata.Calls || latest.Metadata.PromptDigest != saved.Metadata.PromptDigest {
			t.Fatalf("still failed %v, metadata %+v", failed, latest.Metadata)
		}
	})
	t.Run("refreshed meanwhile", func(t *testing.T) {
		o, versions, key, before := detailsFixture(t)
		// A refresh while the details are written brings a failed check.
		*before = func() {
			saved, _ := versions.Latest(ctx, key)
			var doc map[string]any
			if err := json.Unmarshal(saved.Document, &doc); err != nil {
				t.Error(err)
				return
			}
			for _, check := range doc["index"].(map[string]any)["checks"].([]any) {
				if check := check.(map[string]any); check["name"] == "build" {
					check["conclusion"] = "failure"
				}
			}
			doc["review"].(map[string]any)["evidence_refreshed_at"] = "2099-01-01T00:00:00Z"
			refreshed, _ := json.Marshal(doc)
			if err := versions.RefreshCurrent(ctx, key, saved, refreshed); err != nil {
				t.Error(err)
			}
		}
		if _, err := local.completeDetails(ctx, versions, nil, key, details, o); err != nil {
			t.Fatal(err)
		}
		latest, _ := versions.Latest(ctx, key)
		if failed, _ := generate.Gaps(latest.Document); len(failed) != 0 {
			t.Fatalf("still failed %v", failed)
		}
		var doc struct {
			Review struct {
				EvidenceRefreshedAt string `json:"evidence_refreshed_at"`
			} `json:"review"`
			Index struct {
				Checks []struct{ Name, Conclusion string } `json:"checks"`
			} `json:"index"`
			Sections []struct {
				ID      string `json:"id"`
				Summary *struct {
					Text string `json:"text"`
				} `json:"summary"`
			} `json:"sections"`
		}
		if err := json.Unmarshal(latest.Document, &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Review.EvidenceRefreshedAt != "2099-01-01T00:00:00Z" {
			t.Errorf("the refresh was lost: %q", doc.Review.EvidenceRefreshedAt)
		}
		for _, check := range doc.Index.Checks {
			if check.Name == "build" && check.Conclusion != "failure" {
				t.Errorf("the refreshed check was lost: %+v", check)
			}
		}
		for _, section := range doc.Sections {
			if section.ID == "sec_rest" && (section.Summary == nil || section.Summary.Text == "") {
				t.Errorf("sec_rest has no details: %+v", section)
			}
		}
	})
	t.Run("new version meanwhile", func(t *testing.T) {
		o, versions, key, before := detailsFixture(t)
		next := []byte(`{"review":{"repository":"acme/demo","pull_request":7,"version":2}}`)
		*before = func() {
			saved, _ := versions.Latest(ctx, key)
			if err := versions.Put(ctx, key, saved, store.Version{Metadata: store.Metadata{Version: 2}, Document: next}); err != nil {
				t.Error(err)
			}
		}
		if _, err := local.completeDetails(ctx, versions, nil, key, details, o); !errors.Is(err, ErrSuperseded) {
			t.Fatalf("err %v", err)
		}
		if latest, _ := versions.Latest(ctx, key); string(latest.Document) != string(next) {
			t.Fatal("the new version was replaced")
		}
		// A retry queued for a version that is no longer the latest does
		// not run.
		if _, err := local.completeDetails(ctx, versions, nil, key, details, o); !errors.Is(err, ErrSuperseded) {
			t.Fatalf("err %v", err)
		}
	})
}

// A retry is prepared with the backend and model of the version it
// completes, and blocked when the pull request moved, the version is not
// the latest, or nothing failed.
func TestPrepareDetailsChecksTheVersion(t *testing.T) {
	ctx := context.Background()
	registry, clone := registeredClone(t)
	local := localRunner(registry, "owner/repo", "write", 200)
	versions, err := store.Open(clone.CommonDir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: clone.Repository, Number: 1}
	put := func(previous store.Version, version int, head, details string) store.Version {
		t.Helper()
		v := store.Version{Metadata: store.Metadata{Version: version, Head: head, Backend: "claude", Model: "claude-opus-5-5"},
			Document: []byte(`{"review":{"repository":"owner/repo","pull_request":1,"version":` + string(rune('0'+version)) + `},"sections":[{"id":"sec_a","details":"` + details + `"}]}`)}
		if err := versions.Put(ctx, key, previous, v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	first := put(store.Version{}, 1, "other", "failed")
	item := Item{Clone: clone.ID, PR: 1, Kind: KindDetails, Version: 1}
	if p, _, err := local.Prepare(ctx, item); err != nil || p.BlockedReason != ReasonHeadChanged {
		t.Fatalf("moved head: %+v %v", p, err)
	}
	second := put(first, 2, "first", "failed")
	if p, _, err := local.Prepare(ctx, item); err != nil || p.BlockedReason != ReasonVersionChanged {
		t.Fatalf("older version: %+v %v", p, err)
	}
	item.Version = 2
	p, _, err := local.Prepare(ctx, item)
	if err != nil || p.BlockedReason != "" || p.Backend != "claude" || p.Model != "claude-opus-5-5" || p.Version != 2 {
		t.Fatalf("retry: %+v %v", p, err)
	}
	if err := local.confirmed(ctx, p); err != nil {
		t.Fatalf("confirmed: %v", err)
	}
	put(second, 3, "first", "complete")
	item.Version = 3
	if p, _, err := local.Prepare(ctx, item); err != nil || p.BlockedReason != ReasonNothingToRetry {
		t.Fatalf("nothing failed: %+v %v", p, err)
	}
	// A queued retry whose version was replaced stands down when it runs.
	var blocked *BlockedError
	stale := p
	stale.Version = 2
	if err := local.confirmed(ctx, stale); !errors.As(err, &blocked) || blocked.Reason != ReasonVersionChanged {
		t.Fatalf("stale retry: %v", err)
	}
}
