package tilechecks

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// A check's result is kept for a proposal: the definition it checked, with
// its HTML and its digest, and each of the candidate's own samples, whether
// it passed and whether it is synthetic.
func TestResultKeepsTheCheckedCandidate(t *testing.T) {
	f := newFixture(t, nil)
	candidate := starter(t)
	own := candidate.Samples[0]
	// A sample with no hunks or items of its own is not synthetic.
	candidate.Samples = append(candidate.Samples, library.Sample{Name: "bare", Data: own.Data})
	done := f.submit(candidate)
	job := f.claim(t)
	if err := f.checker.Report(job.ID, Report{Problems: []Problem{{Code: CodeScriptError, Message: "x", Sample: "bare"}}}); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.Verdict != VerdictFail {
		t.Fatalf("result = %+v", result)
	}
	kept, ok := f.checker.Result(context.Background(), result.Check)
	if !ok {
		t.Fatal("the result was not kept")
	}
	if kept.ID != result.Check || kept.Verdict != VerdictFail || kept.Digest != result.Digest || !strings.Contains(string(kept.Definition), "baum.tile") {
		t.Fatalf("kept = %+v", kept)
	}
	if digest, err := tiles.DefinitionDigest(kept.Definition); err != nil || digest != kept.Digest {
		t.Fatalf("digest = %s %v, want %s", digest, err, kept.Digest)
	}
	if len(kept.Samples) != len(candidate.Samples) {
		t.Fatalf("%d samples", len(kept.Samples))
	}
	if s := kept.Samples[0]; s.Sample.Name != own.Name || !s.Passed || !s.Synthetic || len(s.Sample.Hunks) == 0 {
		t.Errorf("the example sample: %s %v %v", s.Sample.Name, s.Passed, s.Synthetic)
	}
	if s := kept.Samples[len(kept.Samples)-1]; s.Sample.Name != "bare" || s.Passed || s.Synthetic {
		t.Errorf("the bare sample: %s %v %v", s.Sample.Name, s.Passed, s.Synthetic)
	}
	if _, ok := f.checker.Result(context.Background(), "chk_unknown"); ok {
		t.Error("an unknown check has a result")
	}
}

// At most keepResults results are kept, the newest, each for keepFor. A
// check refused before drawing is kept too, with its verdict.
func TestResultsAreBounded(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	f := newFixture(t, func(o *Options) { o.Now = func() time.Time { return now } })
	tooMany := starter(t)
	for len(tooMany.Samples) <= MaxSamples {
		tooMany.Samples = append(tooMany.Samples, tooMany.Samples[0])
	}
	var ids []string
	for range keepResults + 1 {
		result, err := f.checker.Submit(context.Background(), tooMany)
		if err != nil || result.Verdict != VerdictFail {
			t.Fatalf("%+v %v", result, err)
		}
		ids = append(ids, result.Check)
	}
	if _, ok := f.checker.Result(context.Background(), ids[0]); ok {
		t.Error("the oldest result was kept past the bound")
	}
	r, ok := f.checker.Result(context.Background(), ids[len(ids)-1])
	if !ok || r.Verdict != VerdictFail {
		t.Fatalf("the newest: %+v %v", r, ok)
	}
	now = now.Add(keepFor - time.Second)
	if _, ok := f.checker.Result(context.Background(), ids[1]); !ok {
		t.Error("a result expired early")
	}
	now = now.Add(time.Second)
	if _, ok := f.checker.Result(context.Background(), ids[1]); ok {
		t.Error("an expired result was kept")
	}
	if len(f.checker.results) > keepResults {
		t.Errorf("%d results kept", len(f.checker.results))
	}
}
