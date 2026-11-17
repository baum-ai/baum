package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/baum-ai/baum/internal/tileview/tileviewtest"
	"github.com/baum-ai/baum/internal/workstats"
)

// A baum.review/2 version is stored and summarised like a baum.review/1
// one, and never with a composition.
func TestPutStoresAReview2Version(t *testing.T) {
	ctx := context.Background()
	raw, err := os.ReadFile(filepath.Join("..", "..", "reviewv2", "testdata", "v2.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := opened(t)
	dropped := workstats.Take().Counters["store.compositions_dropped"]
	v := Version{Metadata: Metadata{Version: 1, Head: "revision", BundleDigest: "digest", Backend: "claude", PromptVersion: "prompt"}, Document: raw, Composition: tileviewtest.ViewComposition(t)}
	if err := s.Put(ctx, compositionKey, Version{}, v); err != nil {
		t.Fatal(err)
	}
	got, err := s.Latest(ctx, compositionKey)
	if err != nil {
		t.Fatal(err)
	}
	if got.Composition != nil {
		t.Errorf("a baum.review/2 version was stored with a composition: %s", got.Composition)
	}
	if workstats.Take().Counters["store.compositions_dropped"] != dropped {
		t.Error("a baum.review/2 version's composition was counted as dropped")
	}
	var head struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(got.Document, &head); err != nil || head.Schema != "baum.review/2" {
		t.Fatalf("stored %s", head.Schema)
	}
	summaries, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 {
		t.Fatalf("summaries = %+v", summaries)
	}
	sum := summaries[0]
	var score struct {
		Score int `json:"score"`
	}
	if err := json.Unmarshal(sum.RiskScore, &score); err != nil || score.Score != 3 {
		t.Errorf("risk score = %s", sum.RiskScore)
	}
	if sum.Title != "Retry failed webhook deliveries" || sum.PRState != "open" || sum.ReviewMinutes == nil || *sum.ReviewMinutes != 1 {
		t.Errorf("summary = %+v", sum)
	}
}
