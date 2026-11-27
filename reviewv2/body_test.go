package reviewv2

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/tiles/view"
)

// isTile reports whether a block is a tile block of a tile and nothing else.
func isTile(b Block, id string) bool {
	return b.Type == BlockTile && b.Tile == id && b.Title == "" && b.Text == "" && len(b.Items) == 0
}

func TestEnsureBodies(t *testing.T) {
	d := Document{
		Sections: []Section{
			{ID: "sec_a", Body: []Block{
				{Type: BlockProse, Text: "Why."},
				{Type: BlockTile, Tile: "tl_second"},
				{Type: BlockTile, Tile: "tl_gone"},     // does not exist
				{Type: BlockTile, Tile: "tl_foreign"},  // owned by another section
				{Type: BlockTile, Tile: "tl_second"},   // placed twice
				{Type: BlockTile, Tile: "tl_overview"}, // not a section's tile
			}},
			{ID: "sec_b", Body: []Block{{Type: BlockProse, Text: "Older."}, {Type: BlockPoints, Items: []Point{{Text: "X."}}}}},
			{ID: "sec_c"},
		},
		Tiles: []Tile{
			{ID: "tl_first", Owner: "sec_a"}, {ID: "tl_second", Owner: "sec_a"}, {ID: "tl_foreign", Owner: "sec_b"},
			{ID: "tl_overview", Owner: OwnerOverview}, {ID: "tl_added", Owner: "sec_a", Added: true},
		},
	}
	EnsureBodies(&d)
	ids := func(body []Block) (out []string) {
		for _, b := range body {
			if b.Type == BlockTile {
				out = append(out, b.Tile)
			}
		}
		return out
	}
	a := d.Sections[0]
	// The second tile where the section put it, once; the ones it does not
	// own or that do not exist gone; the owned ones it did not place after
	// the blocks, in tile order.
	if got := strings.Join(ids(a.Body), ","); got != "tl_second,tl_first,tl_added" || a.Body[0].Type != BlockProse || len(a.Body) != 4 {
		t.Fatalf("sec_a body = %+v", a.Body)
	}
	b := d.Sections[1]
	if len(b.Body) != 3 || b.Body[0].Text != "Older." || b.Body[1].Type != BlockPoints || b.Body[2].Tile != "tl_foreign" {
		t.Fatalf("sec_b body = %+v", b)
	}
	if c := d.Sections[2]; c.Body == nil || len(c.Body) != 0 {
		t.Fatalf("sec_c body = %#v", c.Body)
	}
	encoded, _ := json.Marshal(d.Sections[2])
	if !strings.Contains(string(encoded), `"body":[]`) {
		t.Errorf("encoded = %s", encoded)
	}
	// Running it again changes nothing.
	again := Document{Sections: append([]Section(nil), d.Sections...), Tiles: d.Tiles}
	EnsureBodies(&again)
	if x, y := encode(t, Document{Sections: d.Sections}), encode(t, Document{Sections: again.Sections}); string(x) != string(y) {
		t.Errorf("not a fixed point:\n%s\n%s", x, y)
	}
}

// Complete leaves every section with a body, whichever way it was written.
func TestCompleteLeavesEveryBody(t *testing.T) {
	d := authored(t)
	d.Sections[0].Body = append(d.Sections[0].Body, Block{Type: BlockTile, Tile: "tl_nowhere"})
	out := completeFixture(t, d, 1, nil)
	for _, s := range out.Sections {
		if len(s.Body) == 0 {
			t.Errorf("section %s: %+v", s.ID, s)
		}
	}
	var placed []string
	for _, b := range out.Sections[0].Body {
		if b.Type == BlockTile {
			placed = append(placed, b.Tile)
		}
	}
	// The fixture's body names a tile of another section and one that does
	// not exist: neither stays.
	if strings.Join(placed, ",") != "tl_wait_code" {
		t.Errorf("sec_retry_policy tile blocks = %v", placed)
	}
	// Read gives a stored document without bodies one.
	stored := encode(t, Document{Schema: Name, Sections: []Section{{ID: "sec_a", Hunks: []string{}}}, Tiles: []Tile{{ID: "tl_a", Owner: "sec_a"}}})
	read, err := Read(stored)
	if err != nil || len(read.Sections[0].Body) != 1 || read.Sections[0].Body[0].Tile != "tl_a" {
		t.Fatalf("Read = %+v, %v", read.Sections, err)
	}
}

func TestCheckBodyCutsAndCaps(t *testing.T) {
	refs := RefsOf(fixtureBundle(t))
	good := HunkAnchor("h_a00000000001", nil)
	bad := HunkAnchor("h_ffffffffffff", nil)
	long := func(n int) string { return strings.Repeat("word ", n) }
	var points []Point
	for i := 0; i < MaxPoints+2; i++ {
		points = append(points, Point{Text: "A **point**."})
	}
	points[0] = Point{Text: long(60), More: long(120), Cites: []Anchor{good, good, bad}}
	points[1] = Point{Text: "  "}
	body := []Block{
		{Type: BlockProse, Title: long(30), Text: "  Why\nit changed.  "},
		{Type: BlockProse, Text: ""},
		{Type: BlockNote, Text: long(80)},
		{Type: BlockPoints, Items: []Point{{Text: ""}}},
		{Type: BlockPoints, Items: points},
		{Type: "chart", Text: "?"},
		{Type: BlockTile, Tile: " tl_a ", Text: "ignored"},
		{Type: BlockTile},
		{Type: BlockNote, Text: "Six."},
		{Type: BlockNote, Text: "Seven."},
		{Type: BlockNote, Text: "Eight."},
	}
	got, report := CheckBody(body, refs)
	if len(got) != MaxBlocks {
		t.Fatalf("%d blocks, want %d: %+v", len(got), MaxBlocks, got)
	}
	if got[0].Text != "Why it changed." || len([]rune(got[0].Title)) > MaxBlockTitle || !strings.HasSuffix(got[0].Title, "…") {
		t.Errorf("prose = %+v", got[0])
	}
	if n := len([]rune(got[1].Text)); n > MaxNote || !strings.HasSuffix(got[1].Text, "…") {
		t.Errorf("note of %d characters = %q", n, got[1].Text)
	}
	p := got[2]
	if len(p.Items) != MaxPoints || len([]rune(p.Items[0].Text)) > MaxPoint || len([]rune(p.Items[0].More)) > MaxPointDetail || p.Items[1].Text != "A **point**." {
		t.Fatalf("points = %+v", p)
	}
	if len(p.Items[0].Cites) != 1 || p.Items[1].Cites == nil {
		t.Errorf("cites = %v / %v", p.Items[0].Cites, p.Items[1].Cites)
	}
	if !isTile(got[3], "tl_a") {
		t.Errorf("tile block = %+v", got[3])
	}
	counts := report.Counts()
	// Cut: the title, the note, a point and its detail. Dropped: an empty
	// prose, a point without text (twice) and the points block it left empty,
	// a block of an unknown type, a tile block naming nothing. Capped: a
	// point past eight and the seventh block. Cites: one repeated, one
	// that does not resolve.
	if counts[view.FixCut] != 4 || counts[view.FixNativeItem] != 6 || counts[view.FixLimit] != 2 || counts[view.FixAnchor] != 1 {
		t.Errorf("counts = %v", counts)
	}
	// What is already within its limits is a fixed point, and a body that
	// ends up empty gets nothing invented.
	again, second := CheckBody(got, refs)
	if second.Total() != 0 || string(encodeBlocks(t, again)) != string(encodeBlocks(t, got)) {
		t.Errorf("not a fixed point: %v", second.Counts())
	}
	if none, _ := CheckBody([]Block{{Type: BlockProse}}, refs); len(none) != 0 {
		t.Errorf("an empty body was filled: %+v", none)
	}
}

// A point keeps a tone other than plain and a note a kind it knows; any
// other value is dropped, and a kind on another block is not kept.
func TestCheckBodyKeepsToneAndKind(t *testing.T) {
	body := []Block{
		{Type: BlockPoints, Items: []Point{{Text: "Good.", Tone: "good"}, {Text: "Plain.", Tone: "plain"}, {Text: "Odd.", Tone: "loud"}}},
		{Type: BlockNote, Kind: "caveat", Text: "Careful."},
		{Type: BlockNote, Kind: "rant", Text: "Hm."},
		{Type: BlockProse, Kind: "caveat", Text: "Prose."},
	}
	got, _ := CheckBody(body, nil)
	if tones := []string{got[0].Items[0].Tone, got[0].Items[1].Tone, got[0].Items[2].Tone}; !slices.Equal(tones, []string{"good", "", ""}) {
		t.Errorf("tones = %v", tones)
	}
	if got[1].Kind != "caveat" || got[2].Kind != "" || got[3].Kind != "" {
		t.Errorf("kinds = %q %q %q", got[1].Kind, got[2].Kind, got[3].Kind)
	}
}

// review-2.json lists the same point tones and note kinds as the host keeps.
func TestSchemaListsToneAndKind(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "schema", "review-2.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Defs struct {
			Block struct {
				Properties struct {
					Kind  struct{ Enum []string } `json:"kind"`
					Items struct {
						Items struct {
							Properties struct {
								Tone struct{ Enum []string } `json:"tone"`
							} `json:"properties"`
						} `json:"items"`
					} `json:"items"`
				} `json:"properties"`
			} `json:"v2Block"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	props := schema.Defs.Block.Properties
	if !slices.Equal(props.Kind.Enum, NoteKinds) || !slices.Equal(props.Items.Items.Properties.Tone.Enum, PointTones) {
		t.Errorf("kinds %v, tones %v", props.Kind.Enum, props.Items.Items.Properties.Tone.Enum)
	}
}

func encodeBlocks(t *testing.T, blocks []Block) []byte {
	t.Helper()
	raw, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// A section converted from baum.review/1 has a body: prose from its summary,
// points from its behaviour steps, and a tile block for each tile it owns.
func TestFromV1Body(t *testing.T) {
	d := fromV1WithComposition(t, readFile(t, "v1.review.json"), readFile(t, "v1.composition.json"))
	tilesOf := map[string][]string{}
	for _, tile := range d.Tiles {
		tilesOf[tile.Owner] = append(tilesOf[tile.Owner], tile.ID)
	}
	for _, s := range d.Sections {
		if len(s.Body) == 0 || s.Body[0].Type != BlockProse {
			t.Fatalf("section %s: %+v", s.ID, s)
		}
		var placed []string
		points := 0
		for _, b := range s.Body {
			switch b.Type {
			case BlockTile:
				placed = append(placed, b.Tile)
			case BlockPoints:
				for _, p := range b.Items {
					points++
					if p.Text == "" || p.Cites == nil {
						t.Errorf("point = %+v", p)
					}
				}
			}
		}
		if strings.Join(placed, ",") != strings.Join(tilesOf[s.ID], ",") {
			t.Errorf("section %s places %v, owns %v", s.ID, placed, tilesOf[s.ID])
		}
		if s.ID == "sec_retry_policy" && points == 0 {
			t.Errorf("section %s has no points", s.ID)
		}
	}
}
