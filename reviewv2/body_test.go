package reviewv2

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/baum-ai/baum/tiles/view"
)

// isTile reports whether a block is a tile block of a tile and nothing else.
func isTile(b Block, id string) bool {
	return b.Type == BlockTile && b.Tile == id && b.Title == "" && b.Text == "" && len(b.Items) == 0
}

func TestBodyOf(t *testing.T) {
	tiles := []Tile{{ID: "tl_a", Owner: "sec_x"}, {ID: "tl_other", Owner: "sec_y"}, {ID: "tl_b", Owner: "sec_x"}}
	cite := HunkAnchor("h_a00000000001", nil)
	older := Section{ID: "sec_x", Summary: "It does a thing.", Steps: []Step{
		{ID: "st_1", Title: "First.", Text: "One happens.", Cites: []Anchor{cite}},
		{ID: "st_2", Title: "Second", Text: "Two happens."},
	}}
	body := BodyOf(older, tiles)
	if len(body) != 4 || body[0].Type != BlockProse || body[0].Text != "It does a thing." {
		t.Fatalf("body = %+v", body)
	}
	if p := body[1]; p.Type != BlockPoints || len(p.Items) != 2 || p.Items[0].Text != "**First.** One happens." || len(p.Items[0].Cites) != 1 ||
		p.Items[1].Text != "**Second.** Two happens." || p.Items[1].Cites == nil {
		t.Fatalf("points = %+v", body[1])
	}
	if !isTile(body[2], "tl_a") || !isTile(body[3], "tl_b") {
		t.Fatalf("tile blocks = %+v", body[2:])
	}
	// A section with a body keeps it, whatever it carries besides.
	own := Section{ID: "sec_x", Summary: "Old.", Body: []Block{{Type: BlockNote, Text: "Mine."}}}
	if got := BodyOf(own, tiles); len(got) != 1 || got[0].Text != "Mine." {
		t.Fatalf("own body = %+v", got)
	}
	// Nothing to derive from is an empty body, never an invented one.
	if got := BodyOf(Section{ID: "sec_z"}, tiles); len(got) != 0 {
		t.Fatalf("empty = %+v", got)
	}
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
			}, Summary: "kept out", Steps: []Step{{ID: "st_1"}}},
			{ID: "sec_b", Summary: "Older.", Steps: []Step{{ID: "st_1", Title: "T", Text: "X."}}},
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
	if a.Summary != "" || a.Steps != nil {
		t.Errorf("the older text is written again: %q %v", a.Summary, a.Steps)
	}
	b := d.Sections[1]
	if len(b.Body) != 3 || b.Body[0].Text != "Older." || b.Body[1].Type != BlockPoints || b.Body[2].Tile != "tl_foreign" || b.Summary != "" || b.Steps != nil {
		t.Fatalf("sec_b body = %+v", b)
	}
	if c := d.Sections[2]; c.Body == nil || len(c.Body) != 0 {
		t.Fatalf("sec_c body = %#v", c.Body)
	}
	encoded, _ := json.Marshal(d.Sections[2])
	if !strings.Contains(string(encoded), `"body":[]`) || strings.Contains(string(encoded), `"summary"`) || strings.Contains(string(encoded), `"steps"`) {
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
		if len(s.Body) == 0 || s.Summary != "" || s.Steps != nil {
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
	// The older section (a summary and steps) is derived, with its tile.
	older := out.Sections[1].Body
	if len(older) != 3 || older[0].Type != BlockProse || older[1].Type != BlockPoints || !isTile(older[2], "tl_error_paths") {
		t.Errorf("derived body = %+v", older)
	}
	// What was written is not changed in place.
	if len(d.Sections[1].Body) != 0 || d.Sections[1].Summary == "" {
		t.Error("Complete changed the authored document")
	}
	// Read gives a stored document without bodies one.
	stored := encode(t, Document{Schema: Name, Sections: []Section{{ID: "sec_a", Summary: "Older.", Hunks: []string{}}}, Tiles: []Tile{{ID: "tl_a", Owner: "sec_a"}}})
	read, err := Read(stored)
	if err != nil || len(read.Sections[0].Body) != 2 || read.Sections[0].Body[1].Tile != "tl_a" || read.Sections[0].Summary != "" {
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
	d, err := FromV1(readFile(t, "v1.review.json"), readFile(t, "v1.composition.json"))
	if err != nil {
		t.Fatal(err)
	}
	tilesOf := map[string][]string{}
	for _, tile := range d.Tiles {
		tilesOf[tile.Owner] = append(tilesOf[tile.Owner], tile.ID)
	}
	for _, s := range d.Sections {
		if len(s.Body) == 0 || s.Body[0].Type != BlockProse || s.Summary != "" || s.Steps != nil {
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
