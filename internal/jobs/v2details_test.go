package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/reviewv2"
)

// v2DetailsFixture publishes a baum.review/2 version whose second section
// failed. The returned calls lists every call's stage; failing, while
// true, keeps that section failing.
func v2DetailsFixture(t *testing.T) (generate.Options, *store.Store, store.Key, *bool, func() []string) {
	t.Helper()
	failing := true
	var mu sync.Mutex
	var calls []string
	backend := &generatetest.Backend{Version: "9.9.9"}
	backend.RunFunc = func(_ context.Context, r agent.Request, _ int) (agent.Result, error) {
		mu.Lock()
		calls = append(calls, r.Stage)
		mu.Unlock()
		raw, err := os.ReadFile(filepath.Join(r.Directory, "bundle.json"))
		if err != nil {
			return agent.Result{}, err
		}
		var b struct {
			Diff []struct {
				ID string `json:"id"`
			} `json:"diff"`
		}
		if err := json.Unmarshal(raw, &b); err != nil || len(b.Diff) < 2 {
			t.Fatalf("the fixture's bundle has %d hunks: %v", len(b.Diff), err)
		}
		var first, rest []string
		for i, h := range b.Diff {
			if i == 0 {
				first = append(first, `"`+h.ID+`"`)
			} else {
				rest = append(rest, `"`+h.ID+`"`)
			}
		}
		section := func(text string) string {
			return `{"body":[{"type":"prose","title":null,"kind":null,"text":"` + text + `","items":null,"tile":null}],"risks":[],"tiles":[],"propose":null}`
		}
		var reply string
		switch r.Stage {
		case "map":
			reply = `{"change_kind":"feature","sections":[` +
				`{"id":"sec_one","title":"The first part","kind":"logical","hunks":[` + strings.Join(first, ",") + `],"gist":"It changes the first part.","scope":null,"level":2,"level_reasons":[{"text":"It is small.","cites":[]}]},` +
				`{"id":"sec_two","title":"The rest","kind":"logical","hunks":[` + strings.Join(rest, ",") + `],"gist":"It changes the rest.","scope":null,"level":2,"level_reasons":[{"text":"It is small.","cites":[]}]}],"tiles":[]}`
		case "section-sec-one":
			reply = section("It changes the first part.")
		case "section-sec-two":
			mu.Lock()
			fail := failing
			mu.Unlock()
			if fail {
				return agent.Result{Calls: 1}, errors.New("the provider failed")
			}
			reply = section("It changes the rest.")
		case "context":
			reply = `{"area":{"text":"The code.","files":[]},"reached_by":{"text":"","files":[]},"contracts":{"text":"","files":[]},"related":{"text":"","files":[]}}`
		case "overview":
			reply = `{"summary":"It changes the code. That is all.","verdict":{"decision":"approve","text":"Merge it.","points":[]},"risk_why":"It is small.","screenshots":[],"diagram":null,"intent":[]}`
		default:
			return agent.Result{Calls: 1}, errors.New("an unexpected call: " + r.Stage)
		}
		return agent.Result{Document: []byte(reply), Model: "stub-model", Calls: 1, Tokens: 100}, nil
	}
	o := reviewOptions(t, backend)
	o.Staged, o.Budgets.MaxCalls = true, 0
	o.V2 = &generate.V2{}
	versions, err := store.Open("", t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: assembletest.Repository.String(), Number: assembletest.Number}
	result, err := generate.Publish(context.Background(), versions, key, o, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.FailedSections, []string{"sec_two"}) {
		t.Fatalf("published without %v", result.FailedSections)
	}
	return o, versions, key, &failing, func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := slices.Clone(calls)
		calls = nil
		return out
	}
}

// A details retry of a baum.review/2 version writes its failed section
// again through the levels, reusing its complete ones, and updates the
// version in place; one that fails again leaves it as it was.
func TestCompleteDetailsRetriesAV2VersionInPlace(t *testing.T) {
	ctx := context.Background()
	local := &Local{}
	details := Prepared{Item: Item{Kind: KindDetails, Version: 1}}

	t.Run("written", func(t *testing.T) {
		o, versions, key, failing, calls := v2DetailsFixture(t)
		saved, _ := versions.Latest(ctx, key)
		calls()
		*failing = false
		result, err := local.completeDetails(ctx, versions, nil, key, details, o)
		if err != nil {
			t.Fatal(err)
		}
		if ran := calls(); slices.Contains(ran, "section-sec-one") || !slices.Contains(ran, "section-sec-two") {
			t.Errorf("calls %v: the complete section was written again or the failed one not", ran)
		}
		latest, _ := versions.Latest(ctx, key)
		if failed, _ := generate.Gaps(latest.Document); len(failed) != 0 || len(result.FailedSections) != 0 {
			t.Fatalf("still failed %v %v", failed, result.FailedSections)
		}
		if latest.Metadata.Version != 1 || latest.Metadata.GeneratedAt != saved.Metadata.GeneratedAt || latest.Metadata.Schema != reviewv2.Name {
			t.Errorf("metadata %+v", latest.Metadata)
		}
		doc, err := reviewv2.Read(latest.Document)
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.Sections) != 2 || doc.Sections[1].ID != "sec_two" || len(doc.Sections[1].Body) == 0 || doc.Sections[1].Body[0].Text != "It changes the rest." {
			t.Errorf("sections %+v", doc.Sections)
		}
	})
	t.Run("fails again", func(t *testing.T) {
		o, versions, key, _, _ := v2DetailsFixture(t)
		saved, _ := versions.Latest(ctx, key)
		result, err := local.completeDetails(ctx, versions, nil, key, details, o)
		if err != nil {
			t.Fatal(err)
		}
		latest, _ := versions.Latest(ctx, key)
		if !slices.Equal(result.FailedSections, []string{"sec_two"}) || string(latest.Document) != string(saved.Document) {
			t.Errorf("failed %v; the version changed", result.FailedSections)
		}
	})
}

// Only a failed section that the new run wrote over the same hunks is put
// in, with the tiles it owns; the rest of the document stays.
func TestSpliceV2Sections(t *testing.T) {
	current := []byte(`{"schema":"baum.review/2","review":{"version":3},"summary":{"text":"kept"},` +
		`"sections":[{"id":"sec_a","hunks":["h1"],"details":"failed"},{"id":"sec_b","hunks":["h2"],"body":[{"type":"prose","text":"old b"}]},{"id":"sec_c","hunks":["h3"],"details":"failed"}],` +
		`"tiles":[{"id":"tl_old_a","owner":"sec_a"},{"id":"tl_b","owner":"sec_b"}]}`)
	written := []byte(`{"schema":"baum.review/2","review":{"version":3},"summary":{"text":"new"},` +
		`"sections":[{"id":"sec_a","hunks":["h1"],"body":[{"type":"prose","text":"new a"}]},{"id":"sec_b","hunks":["h2"],"body":[{"type":"prose","text":"new b"}]},{"id":"sec_c","hunks":["h3","h4"],"body":[]}],` +
		`"tiles":[{"id":"tl_new_a","owner":"sec_a"},{"id":"tl_b","owner":"sec_a"},{"id":"tl_b2","owner":"sec_b"}]}`)
	out, done, err := spliceV2Sections(current, written, []string{"sec_a", "sec_c"})
	if err != nil || !slices.Equal(done, []string{"sec_a"}) {
		t.Fatalf("done %v: %v", done, err)
	}
	var doc struct {
		Summary  struct{ Text string } `json:"summary"`
		Sections []struct {
			ID      string          `json:"id"`
			Details string          `json:"details"`
			Body    json.RawMessage `json:"body"`
		} `json:"sections"`
		Tiles []struct{ ID, Owner string } `json:"tiles"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Summary.Text != "kept" || doc.Sections[0].Details != "" || !strings.Contains(string(doc.Sections[0].Body), "new a") ||
		!strings.Contains(string(doc.Sections[1].Body), "old b") || doc.Sections[2].Details != "failed" {
		t.Errorf("document %s", out)
	}
	var tiles []string
	for _, tile := range doc.Tiles {
		tiles = append(tiles, tile.ID+"@"+tile.Owner)
	}
	if !slices.Equal(tiles, []string{"tl_b@sec_b", "tl_new_a@sec_a"}) {
		t.Errorf("tiles %v", tiles)
	}
	if same, done, _ := spliceV2Sections(current, written, []string{"sec_c"}); len(done) != 0 || string(same) != string(current) {
		t.Errorf("a section over other hunks was put in: %v", done)
	}
}
