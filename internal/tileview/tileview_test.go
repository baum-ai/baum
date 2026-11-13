package tileview

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/tileview/tileviewtest"
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
	r := Build(tileviewtest.Encode(t, tileviewtest.Base(t)), tiles.Options{})
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
		r := Build(raw, tiles.Options{Draft: tc.draft})
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
		r := Build([]byte(document), tiles.Options{})
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
	r := Build(tileviewtest.Encode(t, tileviewtest.DraftOutline(t)), tiles.Options{Draft: true})
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
	if r := Build(tileviewtest.Encode(t, tileviewtest.Base(t)), tiles.Options{}); r.Tiles == nil {
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
