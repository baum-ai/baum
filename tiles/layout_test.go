package tiles_test

import (
	"slices"
	"testing"

	"github.com/baum-ai/baum/tiles"
)

func entries(sizes ...tiles.Size) []tiles.Entry {
	out := make([]tiles.Entry, len(sizes))
	for i, size := range sizes {
		out[i] = tiles.Entry{Tile: string(rune('a' + i)), Size: size}
	}
	return out
}

func TestLanding(t *testing.T) {
	const h, third, full = tiles.SizeHalf, tiles.SizeThird, tiles.SizeFull
	for _, tc := range []struct {
		name   string
		layout tiles.Layout
		want   int
	}{
		{"empty", tiles.Layout{}, 0},
		{"fewer than fit", tiles.Layout{Entries: entries(h, h, third)}, 3},
		{"three full rows", tiles.Layout{Entries: entries(full, full, full, full, full)}, 3},
		{"a single entry left is shown", tiles.Layout{Entries: entries(full, full, full, full)}, 4},
		{"halves and thirds", tiles.Layout{Entries: entries(h, h, third, third, third, third, third, third, third, third)}, 8},
		{"a half that does not fit its row starts the next", tiles.Layout{Entries: entries(third, third, h, full, full, full)}, 4},
		{"an entry past the last row ends the landing page", tiles.Layout{Entries: entries(h, third, h, h, full, third, third)}, 5},
		{
			// The group is a block: c1 starts a row and d the next, so the
			// third row ends with f.
			"collapsed group members take no space",
			tiles.Layout{
				Entries: []tiles.Entry{
					{Tile: "a", Size: h}, {Tile: "b", Size: h},
					{Tile: "c1", Size: third, Group: "g"}, {Tile: "c2", Size: third, Group: "g"}, {Tile: "c3", Size: third, Group: "g"},
					{Tile: "d", Size: third}, {Tile: "e", Size: third}, {Tile: "f", Size: third}, {Tile: "x", Size: third},
					{Tile: "y", Size: third}, {Tile: "z", Size: third},
				},
				Groups: []tiles.Group{{ID: "g", Title: "G", CollapseAfter: new(1)}},
			},
			8,
		},
		{
			"a group starts a row and the entry after it another",
			tiles.Layout{
				Entries: []tiles.Entry{
					{Tile: "a", Size: h}, {Tile: "c1", Size: third, Group: "g"}, {Tile: "b", Size: third},
					{Tile: "d", Size: full}, {Tile: "e", Size: full},
				},
				Groups: []tiles.Group{{ID: "g", Title: "G"}},
			},
			3,
		},
		{
			"trailing collapsed members are not counted",
			tiles.Layout{
				Entries: []tiles.Entry{{Tile: "a", Size: full}, {Tile: "o1", Size: third, Group: "o"}, {Tile: "o2", Size: third, Group: "o"}},
				Groups:  []tiles.Group{{ID: "o", Title: "O", CollapseAfter: new(0)}},
			},
			1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tiles.Landing(tc.layout); got != tc.want {
				t.Errorf("Landing = %d, want %d", got, tc.want)
			}
		})
	}
}

// The landing rule: hidden members of a collapsed group take no space and
// stay behind their group's control wherever they stand; the landing page
// shows the entries before Landing that are not hidden, and "Show N more
// tiles" reveals the rest that are not hidden.
func TestLandingRule(t *testing.T) {
	layout := tiles.Layout{
		Entries: []tiles.Entry{
			{Tile: "a", Size: tiles.SizeFull},
			{Tile: "c1", Size: tiles.SizeThird, Group: "g"}, {Tile: "c2", Size: tiles.SizeThird, Group: "g"}, {Tile: "c3", Size: tiles.SizeThird, Group: "g"},
			{Tile: "d", Size: tiles.SizeFull}, {Tile: "e", Size: tiles.SizeFull},
			{Tile: "o1", Size: tiles.SizeThird, Group: "o"}, {Tile: "o2", Size: tiles.SizeThird, Group: "o"},
			{Tile: "f", Size: tiles.SizeThird},
		},
		Groups: []tiles.Group{{ID: "g", Title: "G", CollapseAfter: new(1)}, {ID: "o", Title: "O", CollapseAfter: new(0)}},
	}
	layout.Landing = tiles.Landing(layout)
	if layout.Landing != 5 {
		t.Fatalf("Landing = %d, want 5", layout.Landing)
	}
	if got, want := tiles.Hidden(layout), []bool{false, false, true, true, false, false, true, true, false}; !slices.Equal(got, want) {
		t.Errorf("Hidden = %v, want %v", got, want)
	}
	if got, want := tiles.Visible(layout), []bool{true, true, false, false, true, false, false, false, false}; !slices.Equal(got, want) {
		t.Errorf("Visible = %v, want %v", got, want)
	}
	// e and f; the obsolete members stay behind their group's control.
	if got := tiles.More(layout); got != 2 {
		t.Errorf("More = %d, want 2", got)
	}
}

// A group that shows some of its members never folds a single one away, and
// a group folded to none (the obsolete changes) stays folded.
func TestHiddenNeverFoldsOneMember(t *testing.T) {
	layout := tiles.Layout{
		Entries: []tiles.Entry{
			{Tile: "c1", Size: tiles.SizeThird, Group: "g"}, {Tile: "c2", Size: tiles.SizeThird, Group: "g"},
			{Tile: "o1", Size: tiles.SizeThird, Group: "o"},
		},
		Groups: []tiles.Group{{ID: "g", Title: "G", CollapseAfter: new(1)}, {ID: "o", Title: "O", CollapseAfter: new(0)}},
	}
	if got, want := tiles.Hidden(layout), []bool{false, false, true}; !slices.Equal(got, want) {
		t.Errorf("Hidden = %v, want %v", got, want)
	}
}

func order(layout tiles.Layout) []string {
	var ids []string
	for _, entry := range layout.Entries {
		ids = append(ids, entry.Tile)
	}
	return ids
}

func TestArrangeMovesPinnedEntriesFirst(t *testing.T) {
	doc := &tiles.Document{Layout: tiles.Layout{Entries: []tiles.Entry{
		{Tile: "a", Size: tiles.SizeFull}, {Tile: "b", Size: tiles.SizeFull}, {Tile: "c", Size: tiles.SizeFull, Pin: true},
		{Tile: "d", Size: tiles.SizeFull}, {Tile: "e", Size: tiles.SizeHalf, Pin: true},
	}}}
	tiles.Arrange(doc)
	if want := []string{"c", "e", "a", "b", "d"}; !slices.Equal(order(doc.Layout), want) {
		t.Errorf("order = %v, want %v", order(doc.Layout), want)
	}
	if doc.Layout.Landing != 3 {
		t.Errorf("Landing = %d, want 3", doc.Layout.Landing)
	}
}

// A risks tile that ranks an active critical or high risk moves to the front
// when it would otherwise miss the landing page; one already on it stays.
func TestArrangeMovesSevereRisksOntoTheLandingPage(t *testing.T) {
	document := func(tier string, risksAt int) *tiles.Document {
		entries := []tiles.Entry{
			{Tile: "overview", Size: tiles.SizeFull}, {Tile: "a", Size: tiles.SizeFull},
			{Tile: "b", Size: tiles.SizeFull}, {Tile: "c", Size: tiles.SizeFull},
		}
		entries = slices.Insert(entries, risksAt, tiles.Entry{Tile: tiles.TypeRisks, Size: tiles.SizeHalf})
		return &tiles.Document{
			Risks:  []tiles.Risk{{ID: "rsk_x", Tier: tier, State: "active", Change: "sec_x"}},
			Tiles:  []tiles.Tile{{ID: tiles.TypeRisks, Type: tiles.TypeRisks, Data: []byte(`{"ranked":["rsk_x"]}`)}},
			Layout: tiles.Layout{Entries: entries},
		}
	}
	for _, tc := range []struct {
		tier    string
		risksAt int
		want    []string
	}{
		{"high", 4, []string{"risks", "overview", "a", "b", "c"}},
		{"critical", 4, []string{"risks", "overview", "a", "b", "c"}},
		{"medium", 4, []string{"overview", "a", "b", "c", "risks"}},
		{"high", 1, []string{"overview", "risks", "a", "b", "c"}},
	} {
		doc := document(tc.tier, tc.risksAt)
		tiles.Arrange(doc)
		if got := order(doc.Layout); !slices.Equal(got, tc.want) {
			t.Errorf("%s risk at %d: order = %v, want %v", tc.tier, tc.risksAt, got, tc.want)
		}
	}
}

// When pinned entries alone fill the landing page, a risks tile that ranks a
// high risk and the overview go before them.
func TestArrangeKeepsSevereRisksAheadOfPinsThatFillTheLandingPage(t *testing.T) {
	doc := &tiles.Document{
		Risks: []tiles.Risk{{ID: "rsk_x", Tier: "high", State: "active", Change: "sec_x"}},
		Tiles: []tiles.Tile{{ID: tiles.TypeRisks, Type: tiles.TypeRisks, Data: []byte(`{"ranked":["rsk_x"]}`)}},
		Layout: tiles.Layout{Entries: []tiles.Entry{
			{Tile: "overview", Size: tiles.SizeHalf},
			{Tile: "a", Size: tiles.SizeFull, Pin: true}, {Tile: "b", Size: tiles.SizeFull, Pin: true}, {Tile: "c", Size: tiles.SizeFull, Pin: true},
			{Tile: tiles.TypeRisks, Size: tiles.SizeHalf},
		}},
	}
	tiles.Arrange(doc)
	doc.Tiles = append(doc.Tiles, tiles.Tile{ID: tiles.TypeOverview, Type: tiles.TypeOverview})
	tiles.Arrange(doc)
	if want := []string{"risks", "overview", "a", "b", "c"}; !slices.Equal(order(doc.Layout), want) {
		t.Errorf("order = %v, want %v", order(doc.Layout), want)
	}
	if visible := tiles.Visible(doc.Layout); !visible[0] || !visible[1] {
		t.Error("the risks or overview tile is not on the landing page")
	}
}

// The overview moves onto the landing page after pins and a severe risks
// tile; one already on it stays.
func TestArrangeMovesTheOverviewOntoTheLandingPage(t *testing.T) {
	for _, tc := range []struct {
		entries []tiles.Entry
		want    []string
	}{
		{[]tiles.Entry{{Tile: "a", Size: tiles.SizeFull}, {Tile: "b", Size: tiles.SizeFull}, {Tile: "c", Size: tiles.SizeFull}, {Tile: "d", Size: tiles.SizeFull}, {Tile: "overview", Size: tiles.SizeHalf}}, []string{"overview", "a", "b", "c", "d"}},
		{[]tiles.Entry{{Tile: "a", Size: tiles.SizeFull}, {Tile: "b", Size: tiles.SizeFull, Pin: true}, {Tile: "c", Size: tiles.SizeFull}, {Tile: "d", Size: tiles.SizeFull}, {Tile: "overview", Size: tiles.SizeHalf}}, []string{"b", "overview", "a", "c", "d"}},
		{[]tiles.Entry{{Tile: "a", Size: tiles.SizeFull}, {Tile: "overview", Size: tiles.SizeHalf}, {Tile: "c", Size: tiles.SizeFull}}, []string{"a", "overview", "c"}},
	} {
		doc := &tiles.Document{Tiles: []tiles.Tile{{ID: tiles.TypeOverview, Type: tiles.TypeOverview}}, Layout: tiles.Layout{Entries: tc.entries}}
		tiles.Arrange(doc)
		if got := order(doc.Layout); !slices.Equal(got, tc.want) {
			t.Errorf("order = %v, want %v", got, tc.want)
		}
	}
}

// RowFills grows a short row's last tile to the larger allowed size that
// fills the row exactly, else every tile of the row to equal parts, when
// it never grows a pinned tile or shrinks one.
func TestRowFills(t *testing.T) {
	const h, third, full = tiles.SizeHalf, tiles.SizeThird, tiles.SizeFull
	tilesOf := func(n int) []tiles.Tile {
		out := make([]tiles.Tile, n)
		for i := range out {
			out[i] = tiles.Tile{ID: string(rune('a' + i)), Type: tiles.TypeEvidence}
		}
		return out
	}
	for _, tc := range []struct {
		name  string
		sizes []tiles.Size
		pin   int
		want  map[string]tiles.Size
	}{
		{name: "half and third before a half", sizes: []tiles.Size{h, third, h}, pin: -1, want: map[string]tiles.Size{"b": h, "c": full}},
		{name: "half before a full", sizes: []tiles.Size{h, full}, pin: -1, want: map[string]tiles.Size{"a": full}},
		{name: "two thirds before a half", sizes: []tiles.Size{third, third, h}, pin: -1, want: map[string]tiles.Size{"a": h, "b": h, "c": full}},
		{name: "a full row of thirds before a half and a full", sizes: []tiles.Size{third, third, third, h, full}, pin: -1, want: map[string]tiles.Size{"d": full}},
		{name: "pinned", sizes: []tiles.Size{h, third, h}, pin: 1, want: map[string]tiles.Size{"c": full}},
		{name: "full rows", sizes: []tiles.Size{h, h, third, third, third}, pin: -1},
		{name: "last row with nothing after", sizes: []tiles.Size{h, h, h}, pin: -1, want: map[string]tiles.Size{"c": full}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := &tiles.Document{Tiles: tilesOf(len(tc.sizes)), Layout: tiles.Layout{Entries: entries(tc.sizes...)}}
			if tc.pin >= 0 {
				doc.Layout.Entries[tc.pin].Pin = true
			}
			doc.Layout.Landing = tiles.Landing(doc.Layout)
			got := tiles.RowFills(doc)
			if len(got) != len(tc.want) {
				t.Fatalf("fills %v, want %v", got, tc.want)
			}
			for id, size := range tc.want {
				if got[id] != size {
					t.Errorf("fills %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// The landing page grows by a row for each of its rows that holds a
// composition's tile, counting the rows it grows into, at most MaxExtraRows.
func TestExtraRows(t *testing.T) {
	const full = tiles.SizeFull
	doc := &tiles.Document{}
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		doc.Tiles = append(doc.Tiles, tiles.Tile{ID: id, Type: tiles.TypeIntent})
	}
	extra := func(ids ...string) *tiles.Document {
		d := &tiles.Document{Tiles: slices.Clone(doc.Tiles)}
		for i := range d.Tiles {
			if slices.Contains(ids, d.Tiles[i].ID) {
				d.Tiles[i].Type = tiles.TypeProse
			}
		}
		return d
	}
	layout := tiles.Layout{Entries: entries(full, full, full, full, full, full, full)}
	for _, tc := range []struct {
		name  string
		extra []string
		want  int
	}{
		{"none", nil, 0},
		{"one in the first rows", []string{"b"}, 1},
		{"one in the row it grows into", []string{"a", "d"}, 2},
		{"past the rows it grows into", []string{"a", "f"}, 1},
		{"at most MaxExtraRows", []string{"a", "b", "c", "d", "e"}, tiles.MaxExtraRows},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tiles.ExtraRows(extra(tc.extra...), layout); got != tc.want {
				t.Errorf("ExtraRows = %d, want %d", got, tc.want)
			}
		})
	}
}
