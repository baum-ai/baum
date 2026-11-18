package tileview

import (
	"encoding/json"
	"math/rand/v2"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/tileview/tileviewtest"
	"github.com/baum-ai/baum/internal/workstats"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/tilestest"
)

func decode(t *testing.T, r Response) map[string]any {
	t.Helper()
	body, err := Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(body.Data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestBuildConvertsAReview(t *testing.T) {
	r := Build(tileviewtest.Encode(t, tileviewtest.Base(t)), nil, tiles.Options{})
	if r.Tiles == nil || r.Problem != "" {
		t.Fatalf("tiles %v, problem %q", r.Tiles != nil, r.Problem)
	}
	out := decode(t, r)
	if out["document"].(map[string]any)["schema"] != "baum.review/1" || out["tiles"].(map[string]any)["schema"] != "baum.tiles/1" {
		t.Fatalf("schemas %v", out)
	}
	if _, ok := out["problem"]; ok {
		t.Error("a converted review has a problem")
	}
	// The tile document converts exactly the document served beside it.
	back, err := tiles.ToReview(r.Tiles)
	if err != nil {
		t.Fatal(err)
	}
	if equal, err := tilestest.RoundTripEqual(r.Document, back); err != nil || !equal {
		t.Fatalf("round trip: %v %v", equal, err)
	}
}

func TestBuildDraftHasAPendingContext(t *testing.T) {
	doc := tileviewtest.Base(t)
	delete(doc["summary"].(map[string]any), "context")
	raw := tileviewtest.Encode(t, doc)
	for _, tc := range []struct {
		draft bool
		want  string
	}{{false, ""}, {true, tiles.StatePending}} {
		r := Build(raw, nil, tiles.Options{Draft: tc.draft})
		var state string
		for _, tile := range r.Tiles.Tiles {
			if tile.ID == tiles.TypeContext {
				state = tile.State
			}
		}
		if state != tc.want {
			t.Errorf("draft %v: context %q, want %q", tc.draft, state, tc.want)
		}
	}
}

func TestBuildFailureLeavesTilesNullWithoutContent(t *testing.T) {
	for _, document := range []string{
		`{"review":{"repository":"acme/widgets"},"secret":"PRIVATE_TEXT"}`,
		`{"schema":"baum.review/1","unknown_member":"PRIVATE_TEXT"}`,
		`not json PRIVATE_TEXT`,
	} {
		r := Build([]byte(document), nil, tiles.Options{})
		if r.Tiles != nil || r.Problem == "" || strings.Contains(r.Problem, "PRIVATE_TEXT") {
			t.Errorf("%s: tiles %v, problem %q", document, r.Tiles != nil, r.Problem)
		}
		body, err := Encode(r)
		if err != nil || !json.Valid(body.Data) || !strings.Contains(string(body.Data), `"tiles":null`) {
			t.Errorf("%s: %s %v", document, body.Data, err)
		}
	}
}

func TestDraftOutlineConverts(t *testing.T) {
	r := Build(tileviewtest.Encode(t, tileviewtest.DraftOutline(t)), nil, tiles.Options{Draft: true})
	if r.Tiles == nil || r.Problem != "" {
		t.Fatalf("problem %q", r.Problem)
	}
	for _, tile := range r.Tiles.Tiles {
		if tile.State == tiles.StateUnavailable {
			t.Errorf("%s is unavailable: %v", tile.ID, tile.Problems)
		}
		if tile.ID == tiles.TypeContext && tile.State != tiles.StatePending {
			t.Errorf("context is %s", tile.State)
		}
	}
}

// Warm compiles the checks once and is safe to repeat and to run beside
// requests.
func TestWarm(t *testing.T) {
	done := make(chan error, 2)
	for range 2 {
		go func() { done <- Warm() }()
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if r := Build(tileviewtest.Encode(t, tileviewtest.Base(t)), nil, tiles.Options{}); r.Tiles == nil {
		t.Fatal(r.Problem)
	}
}

// The helper's draft converts as the tiles package's draft-outline golden does.
func TestDraftOutlineMatchesTheGolden(t *testing.T) {
	// The golden is converted from the normalised review, without the hunk
	// URLs Build derives.
	normalized, err := review.NormalizeStored(tileviewtest.Encode(t, tileviewtest.DraftOutline(t)))
	if err != nil {
		t.Fatal(err)
	}
	converted, err := tiles.FromReview(normalized, tiles.Options{Draft: true})
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile("../../tiles/testdata/draft-outline.tiles.json")
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal(golden, &want); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(converted)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatal("the draft helper no longer converts to the draft-outline golden")
	}
}

// With no composition, an empty one or one that chooses nothing, the answer
// is the converted document's, byte for byte.
func TestBuildWithoutACompositionIsUnchanged(t *testing.T) {
	raw := tileviewtest.Encode(t, tileviewtest.Base(t))
	want, err := Encode(Build(raw, nil, tiles.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	for name, composition := range map[string][]byte{
		"empty":      {},
		"null":       []byte("null"),
		"chooses":    []byte(`{"schema":"baum.composition/1","layout":{"entries":[]}}`),
		"unreadable": []byte(`{"schema":`),
		"other":      []byte(`{"schema":"baum.composition/2","layout":{"entries":[]}}`),
		"not json":   []byte(`PRIVATE_TEXT`),
	} {
		got, err := Encode(Build(raw, composition, tiles.Options{}))
		if err != nil {
			t.Fatal(err)
		}
		if string(got.Data) != string(want.Data) || got.ETag != want.ETag {
			t.Errorf("%s: the answer changed", name)
		}
	}
}

// A composition that cannot be used is ignored and counted, without a problem
// for the reader and without quoting it.
func TestBuildIgnoresAnUnusableCompositionAndCountsIt(t *testing.T) {
	raw := tileviewtest.Encode(t, tileviewtest.Base(t))
	before := workstats.Take()
	r := Build(raw, []byte(`{"schema":"baum.composition/9","secret":"PRIVATE_TEXT"}`), tiles.Options{})
	if r.Tiles == nil || r.Problem != "" {
		t.Fatalf("tiles %v, problem %q", r.Tiles != nil, r.Problem)
	}
	if got := workstats.Take().Counters["tileview.compositions_ignored"] - before.Counters["tileview.compositions_ignored"]; got != 1 {
		t.Errorf("%d ignored compositions counted, want 1", got)
	}
}

// A composition changes the layout as composed, and the document it gives
// passes every check of a generated one.
func TestBuildComposesTheLayout(t *testing.T) {
	raw := tileviewtest.Encode(t, tileviewtest.Base(t))
	plain := Build(raw, nil, tiles.Options{})
	composition, pinned := tileviewtest.PinnedComposition(t, raw)
	r := Build(raw, composition, tiles.Options{})
	if r.Tiles == nil || r.Problem != "" {
		t.Fatalf("problem %q", r.Problem)
	}
	if plain.Tiles.Layout.Entries[0].Tile == pinned || r.Tiles.Layout.Entries[0].Tile != pinned || !r.Tiles.Layout.Entries[0].Pin {
		t.Fatalf("first tile %q, want %q pinned (plain: %q)", r.Tiles.Layout.Entries[0].Tile, pinned, plain.Tiles.Layout.Entries[0].Tile)
	}
	if r.Tiles.Origin.Kind != tiles.OriginGenerated {
		t.Errorf("origin %q", r.Tiles.Origin.Kind)
	}
	parsed, err := tiles.ParseComposition(composition)
	if err != nil {
		t.Fatal(err)
	}
	if failures := tilestest.CheckGenerated(r.Document, parsed, r.Tiles); len(failures) > 0 {
		t.Fatalf("generated document: %v", failures)
	}
	// The review document served beside the tiles is the same.
	if string(r.Document) != string(plain.Document) {
		t.Error("the review document changed")
	}
	// It still round-trips to the review.
	back, err := tiles.ToReview(r.Tiles)
	if err != nil {
		t.Fatal(err)
	}
	if equal, err := tilestest.RoundTripEqual(r.Document, back); err != nil || !equal {
		t.Fatalf("round trip: %v %v", equal, err)
	}
}

// Seeded random compositions, hostile ones included, always give a checked
// document.
func TestBuildComposesRandomCompositions(t *testing.T) {
	raw := tileviewtest.Encode(t, tileviewtest.Base(t))
	normalized, err := review.NormalizeStored(raw)
	if err != nil {
		t.Fatal(err)
	}
	converted, err := tiles.FromReview(normalized, tiles.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for seed := range uint64(20) {
		c := tilestest.RandomComposition(rand.New(rand.NewPCG(seed, 1)), converted, seed%2 == 1)
		// What a store holds: the normalised composition.
		normal, _ := tiles.NormalizeComposition(converted, c)
		encoded, err := json.Marshal(normal)
		if err != nil {
			t.Fatal(err)
		}
		r := Build(raw, encoded, tiles.Options{})
		if r.Tiles == nil || r.Problem != "" {
			t.Fatalf("seed %d: problem %q", seed, r.Problem)
		}
		if failures := tilestest.CheckGenerated(r.Document, &normal, r.Tiles); len(failures) > 0 {
			t.Fatalf("seed %d: %v", seed, failures)
		}
	}
}

func TestKeyNamesTheComposition(t *testing.T) {
	a, b := []byte(`{"a":1}`), []byte(`{"a":2}`)
	if Key(nil) != "" || Key([]byte{}) != "" || Key(a) == "" || Key(a) == Key(b) || Key(a) != Key([]byte(`{"a":1}`)) {
		t.Error("keys do not name the composition's bytes")
	}
}

func ignoredCount() int64 { return workstats.Take().Counters["tileview.compositions_ignored"] }

// A composition over the size bound is ignored and counted, whatever it holds.
func TestBuildIgnoresAnOversizedComposition(t *testing.T) {
	raw := tileviewtest.Encode(t, tileviewtest.Base(t))
	want, _ := Encode(Build(raw, nil, tiles.Options{}))
	huge := []byte(`{"schema":"baum.composition/1","layout":{"entries":[]},"x":"` + strings.Repeat("a", tiles.MaxStoredComposition) + `"}`)
	before := ignoredCount()
	got, err := Encode(Build(raw, huge, tiles.Options{}))
	if err != nil || string(got.Data) != string(want.Data) || ignoredCount() != before+1 {
		t.Errorf("answer changed %v, counted %d: %v", string(got.Data) != string(want.Data), ignoredCount()-before, err)
	}
}

// A failure while composing, even a panic, serves the converted layout and is
// counted.
func TestBuildSurvivesAComposeFailure(t *testing.T) {
	raw := tileviewtest.Encode(t, tileviewtest.Base(t))
	composition, _ := tileviewtest.PinnedComposition(t, raw)
	want, _ := Encode(Build(raw, nil, tiles.Options{}))
	defer func(old func(*tiles.Document, *tiles.Composition) (*tiles.Document, error)) { applyComposition = old }(applyComposition)
	applyComposition = func(*tiles.Document, *tiles.Composition) (*tiles.Document, error) { panic("PRIVATE_TEXT") }
	before := ignoredCount()
	r := Build(raw, composition, tiles.Options{})
	got, err := Encode(r)
	if err != nil || r.Problem != "" || string(got.Data) != string(want.Data) || ignoredCount() != before+1 {
		t.Errorf("problem %q, changed %v, counted %d: %v", r.Problem, string(got.Data) != string(want.Data), ignoredCount()-before, err)
	}
}

// A composition stored for a document that has since changed (a refresh or a
// completion that removed a section, here) still gives a checked document.
func TestBuildComposesAfterTheDocumentDrifted(t *testing.T) {
	base := tileviewtest.Base(t)
	original := tileviewtest.Encode(t, base)
	normalized, err := review.NormalizeStored(original)
	if err != nil {
		t.Fatal(err)
	}
	converted, err := tiles.FromReview(normalized, tiles.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for seed := range uint64(6) {
		c := tilestest.RandomComposition(rand.New(rand.NewPCG(seed, 9)), converted, false)
		normal, _ := tiles.NormalizeComposition(converted, c)
		stored, err := json.Marshal(normal)
		if err != nil {
			t.Fatal(err)
		}
		drifted := tileviewtest.Base(t)
		// The last section goes; its hunks are unassigned, as a document
		// refreshed against a moved head leaves them.
		sections := drifted["sections"].([]any)
		gone := sections[len(sections)-1].(map[string]any)
		drifted["sections"] = sections[:len(sections)-1]
		drifted["unassigned_hunks"] = append(drifted["unassigned_hunks"].([]any), gone["hunks"].([]any)...)
		raw := tileviewtest.Encode(t, drifted)
		r := Build(raw, stored, tiles.Options{})
		if r.Tiles == nil || r.Problem != "" {
			t.Fatalf("seed %d: tiles %v, problem %q", seed, r.Tiles != nil, r.Problem)
		}
		// Every change of the drifted document is placed, and the removed one
		// is not.
		for _, tile := range r.Tiles.Tiles {
			if tile.ID == "sec_generated_client" {
				t.Fatalf("seed %d: the removed section has a tile", seed)
			}
		}
		now, err := review.NormalizeStored(raw)
		if err != nil {
			t.Fatal(err)
		}
		again, err := tiles.FromReview(now, tiles.Options{})
		if err != nil {
			t.Fatal(err)
		}
		applied, _ := tiles.NormalizeComposition(again, &normal)
		if failures := tilestest.CheckGenerated(r.Document, &applied, r.Tiles); len(failures) > 0 {
			t.Fatalf("seed %d: %v", seed, failures)
		}
		back, err := tiles.ToReview(r.Tiles)
		if err != nil {
			t.Fatal(err)
		}
		if equal, err := tilestest.RoundTripEqual(r.Document, back); err != nil || !equal {
			t.Fatalf("seed %d: round trip %v %v", seed, equal, err)
		}
	}
}
