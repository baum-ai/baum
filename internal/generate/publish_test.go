package generate_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/viewserver"
	"github.com/baum-ai/baum/review"
)

// Live facts refresh independently while a generation runs, and the
// generation still publishes over the refreshed previous version.
func TestPublishAfterRefreshDuringGeneration(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	valid := func(_ context.Context, request agent.Request, _ int) (agent.Result, error) {
		document, err := generatetest.Document(request.Directory, nil)
		return agent.Result{Document: document, Model: "stub-model", Calls: 1}, err
	}
	f.backend.RunFunc = valid
	s, err := store.Open("", t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: assembletest.Repository.String(), Number: assembletest.Number}
	if _, err = generate.Publish(ctx, s, key, f.options(t), nil); err != nil {
		t.Fatal(err)
	}
	first, err := s.Latest(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err = json.Unmarshal(first.Document, &doc); err != nil {
		t.Fatal(err)
	}
	doc["refreshed"] = json.RawMessage(`true`)
	refreshed, _ := json.Marshal(doc)
	var refreshErr error
	f.backend.RunFunc = func(ctx context.Context, request agent.Request, n int) (agent.Result, error) {
		refreshErr = s.RefreshCurrent(ctx, key, first, refreshed)
		return valid(ctx, request, n)
	}
	if _, err = generate.Publish(ctx, s, key, f.options(t), nil); err != nil {
		t.Fatal(err)
	}
	if refreshErr != nil {
		t.Fatalf("refresh blocked during generation: %v", refreshErr)
	}
	if latest, err := s.Latest(ctx, key); err != nil || latest.Metadata.Version != 2 {
		t.Fatalf("generation not published: %+v %v", latest.Metadata, err)
	}
	if old, err := s.Get(ctx, key, 1); err != nil || !bytes.Equal(old.Document, refreshed) {
		t.Fatal("refreshed previous version not kept", err)
	}
}

func TestPublishCancellationDuringRepairKeepsStoreEmptyAndCleansTrees(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.backend.RunFunc = func(ctx context.Context, r agent.Request, n int) (agent.Result, error) {
		if n == 0 {
			return agent.Result{Document: []byte(`{}`), Calls: 1}, nil
		}
		cancel()
		<-ctx.Done()
		return agent.Result{}, ctx.Err()
	}
	s, e := store.Open("", t.TempDir(), true)
	if e != nil {
		t.Fatal(e)
	}
	key := store.Key{Repository: "acme/demo", Number: 7}
	_, e = generate.Publish(ctx, s, key, f.options(t), nil)
	if generate.CategoryOf(e) != generate.CategoryInterrupted {
		t.Fatal(e)
	}
	if _, e = s.Latest(context.Background(), key); !errors.Is(e, store.ErrNotFound) {
		t.Fatal("partial version published", e)
	}
	snapshot, e := s.Snapshot(context.Background())
	if e != nil || len(snapshot) != 1 || snapshot[0].Run.Reason != "interrupted" {
		t.Fatal("missing interrupted run", snapshot, e)
	}
	f.requireClean(t)
}

func TestPublishPreservesConfiguredModelEstimateWithCostCallback(t *testing.T) {
	f := newFixture(t)
	f.backend.RunFunc = func(_ context.Context, r agent.Request, _ int) (agent.Result, error) {
		doc, err := generatetest.Document(r.Directory, nil)
		return agent.Result{Document: doc, Model: "claude-sonnet-4-5", ModelSource: "configured", Calls: 1, Tokens: 10, Usage: agent.Usage{Input: 6, Output: 4, Speed: new("standard")}}, err
	}
	s, err := store.Open("", t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: "acme/demo", Number: 7}
	_, err = generate.Publish(context.Background(), s, key, f.options(t), func(r generate.Result) *float64 {
		return pricing.Embedded().OutcomeCost(r.Outcome.Model, r.Outcome)
	})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.Latest(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Metadata.RecordedCostUSD == nil || *saved.Metadata.RecordedCostUSD <= 0 || saved.Metadata.CostReason != "estimated_from_configured_model" {
		t.Fatalf("lost configured-model estimate: %+v", saved.Metadata)
	}
}

func TestPublishKeepsRetryUsageAndOldRecordsUnknown(t *testing.T) {
	f := newFixture(t)
	f.backend.RunFunc = func(_ context.Context, r agent.Request, n int) (agent.Result, error) {
		doc, err := generatetest.Document(r.Directory, nil)
		if n == 0 {
			doc = []byte(`{}`)
		}
		return agent.Result{Document: doc, Model: "reported-model", Calls: 1, Tokens: 10, Usage: agent.Usage{Input: 6, Output: 4, CacheRead: 100}}, err
	}
	s, err := store.Open("", t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: "acme/demo", Number: 7}
	result, err := generate.Publish(context.Background(), s, key, f.options(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.Latest(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	a := saved.Metadata.Accounting
	if a == nil || len(a.Attempts) != 2 || a.Usage.CacheRead != 200 || a.ChargedTokens != 20 || result.Metadata.CostReason != "speed_not_reported" {
		t.Fatalf("lost repaired usage: %+v %+v", saved.Metadata, a)
	}
	var old store.Metadata
	if err = json.Unmarshal([]byte(`{"tokens":123,"model":"old"}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Accounting != nil {
		t.Fatal("invented old usage")
	}
}

type failingFinish struct{ *store.Store }

func (failingFinish) FinishRun(context.Context, int64, string) error {
	return errors.New("database is locked")
}

func TestPublishSucceedsWhenRecordingOutcomeFailsAfterPut(t *testing.T) {
	f := newFixture(t)
	s, e := store.Open("", t.TempDir(), true)
	if e != nil {
		t.Fatal(e)
	}
	key := store.Key{Repository: assembletest.Repository.String(), Number: assembletest.Number}
	result, e := generate.Publish(context.Background(), failingFinish{s}, key, f.options(t), nil)
	if e != nil {
		t.Fatalf("published version reported as failed: %v", e)
	}
	if len(result.Document) == 0 {
		t.Fatal("published document not returned")
	}
	latest, e := s.Latest(context.Background(), key)
	stripped, _, stripErr := review.StripHunkEvidence(result.Document)
	if e != nil || stripErr != nil || !bytes.Equal(latest.Document, stripped) {
		t.Fatal("returned document is not the stored version", e, stripErr)
	}
	if !strings.Contains(f.progress.String(), "warning: record generation outcome: database is locked") {
		t.Fatalf("missing warning: %q", f.progress.String())
	}
}

// Keeping the version's commits runs after the version is stored, outside
// its transaction: a failure there is a warning, never a failed publish.
func TestPublishSucceedsWhenKeepingCommitsFails(t *testing.T) {
	f := newFixture(t)
	s, e := store.Open("", t.TempDir(), true)
	if e != nil {
		t.Fatal(e)
	}
	key := store.Key{Repository: assembletest.Repository.String(), Number: assembletest.Number}
	options := f.options(t)
	var stored store.Metadata
	options.Stored = func(_ context.Context, k store.Key, m store.Metadata) error {
		if k != key {
			t.Errorf("stored key = %+v", k)
		}
		latest, err := s.Latest(context.Background(), key)
		if err != nil || latest.Metadata.Version != m.Version {
			t.Errorf("Stored ran before the version was stored: %+v %v", latest.Metadata, err)
		}
		stored = m
		return errors.New("cannot lock ref")
	}
	result, e := generate.Publish(context.Background(), s, key, options, nil)
	if e != nil {
		t.Fatalf("published version reported as failed: %v", e)
	}
	if stored.Version != 1 || stored.Head == "" || stored.Head != result.Metadata.Head {
		t.Fatalf("Stored got %+v, published %+v", stored, result.Metadata)
	}
	if latest, e := s.Latest(context.Background(), key); e != nil || latest.Metadata.Version != 1 {
		t.Fatal("version not kept", e)
	}
	if !strings.Contains(f.progress.String(), "warning: keep the version's commits in the clone: cannot lock ref") {
		t.Fatalf("missing warning: %q", f.progress.String())
	}
}

// A locally saved document keeps no hunk evidence: the patch text and GitHub
// fragment of each indexed hunk are left out, and everything else is kept as
// generated. Serving it derives the same fragments generation recorded.
func TestPublishStoresTheDocumentWithoutHunkEvidence(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	s, err := store.Open("", t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: assembletest.Repository.String(), Number: assembletest.Number}
	result, err := generate.Publish(ctx, s, key, f.options(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := s.Latest(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	generated, stored := decodeHunks(t, result.Document), decodeHunks(t, latest.Document)
	diffs := 0
	for _, hunk := range generated.hunks {
		if hunk["github_url"] == nil {
			t.Fatalf("generated hunk without github_url: %s", hunk["id"])
		}
		if hunk["diff"] != nil {
			diffs++
		}
	}
	if len(generated.hunks) == 0 || diffs == 0 {
		t.Fatalf("fixture has %d hunks, %d with a diff; want both", len(generated.hunks), diffs)
	}
	if len(stored.hunks) != len(generated.hunks) {
		t.Fatalf("stored %d hunks, generated %d", len(stored.hunks), len(generated.hunks))
	}
	for i, hunk := range stored.hunks {
		if hunk["diff"] != nil || hunk["github_url"] != nil {
			t.Fatalf("stored hunk %s keeps evidence", hunk["id"])
		}
		for _, k := range []string{"id", "path", "change", "role", "new_range", "additions", "deletions"} {
			if hunk[k] == nil {
				t.Fatalf("stored hunk lost %s", k)
			}
		}
		want := map[string]json.RawMessage{}
		for k, v := range generated.hunks[i] {
			if k != "diff" && k != "github_url" {
				want[k] = v
			}
		}
		got, _ := json.Marshal(hunk)
		expected, _ := json.Marshal(want)
		if !bytes.Equal(got, expected) {
			t.Fatalf("stored hunk %d = %s, want %s", i, got, expected)
		}
	}
	// Sections (their tiles and hunk assignments), stats, unassigned hunks
	// and the rest of the index are kept byte for byte.
	for _, pair := range [][2]map[string]json.RawMessage{{stored.rest, generated.rest}, {stored.index, generated.index}} {
		if len(pair[0]) != len(pair[1]) {
			t.Fatalf("stored %d members, generated %d", len(pair[0]), len(pair[1]))
		}
		for k, v := range pair[1] {
			if !bytes.Equal(pair[0][k], v) {
				t.Fatalf("stored %s changed", k)
			}
		}
	}
	if !bytes.Contains(stored.rest["sections"], []byte(`"hunks"`)) {
		t.Fatal("fixture sections assign no hunks")
	}
	served := decodeHunks(t, viewserver.WithHunkURLs(latest.Document))
	for i, hunk := range served.hunks {
		if want := generated.hunks[i]["github_url"]; !bytes.Equal(hunk["github_url"], want) {
			t.Fatalf("served github_url %s, generated %s", hunk["github_url"], want)
		}
	}
}

// hunkDocument is a document split into its index.hunks, the rest of its
// index and the rest of the document.
type hunkDocument struct {
	rest, index map[string]json.RawMessage
	hunks       []map[string]json.RawMessage
}

func decodeHunks(t *testing.T, raw []byte) hunkDocument {
	t.Helper()
	var d hunkDocument
	if err := json.Unmarshal(raw, &d.rest); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(d.rest["index"], &d.index); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(d.index["hunks"], &d.hunks); err != nil {
		t.Fatal(err)
	}
	delete(d.rest, "index")
	delete(d.index, "hunks")
	return d
}
