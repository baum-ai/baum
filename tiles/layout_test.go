package tiles

import (
	"slices"
	"testing"
)

func entries(sizes ...Size) []Entry {
	out := make([]Entry, len(sizes))
	for i, size := range sizes {
		out[i] = Entry{Tile: string(rune('a' + i)), Size: size}
	}
	return out
}

func TestLanding(t *testing.T) {
	const h, third, full = SizeHalf, SizeThird, SizeFull
	for _, tc := range []struct {
		name   string
		layout Layout
		want   int
	}{
		{"empty", Layout{}, 0},
		{"fewer than fit", Layout{Entries: entries(h, h, third)}, 3},
		{"three full rows", Layout{Entries: entries(full, full, full, full)}, 3},
		{"halves and thirds", Layout{Entries: entries(h, h, third, third, third, third, third, third, third)}, 8},
		{"a half that does not fit its row starts the next", Layout{Entries: entries(third, third, h, full, full)}, 4},
		{"an entry past the last row ends the landing page", Layout{Entries: entries(h, third, h, h, full, third)}, 5},
		{
			"collapsed group members take no space",
			Layout{
				Entries: []Entry{
					{Tile: "a", Size: h}, {Tile: "b", Size: h},
					{Tile: "c1", Size: third, Group: "g"}, {Tile: "c2", Size: third, Group: "g"}, {Tile: "c3", Size: third, Group: "g"},
					{Tile: "d", Size: third}, {Tile: "e", Size: third}, {Tile: "f", Size: third}, {Tile: "x", Size: third},
					{Tile: "y", Size: third}, {Tile: "z", Size: third},
				},
				Groups: []Group{{ID: "g", Title: "G", CollapseAfter: new(1)}},
			},
			10,
		},
		{
			"trailing collapsed members are not counted",
			Layout{
				Entries: []Entry{{Tile: "a", Size: full}, {Tile: "o1", Size: third, Group: "o"}, {Tile: "o2", Size: third, Group: "o"}},
				Groups:  []Group{{ID: "o", Title: "O", CollapseAfter: new(0)}},
			},
			1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Landing(tc.layout); got != tc.want {
				t.Errorf("Landing = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestVisible(t *testing.T) {
	layout := Layout{
		Entries: []Entry{
			{Tile: "a", Size: SizeFull},
			{Tile: "c1", Size: SizeThird, Group: "g"}, {Tile: "c2", Size: SizeThird, Group: "g"},
			{Tile: "d", Size: SizeFull}, {Tile: "e", Size: SizeFull},
		},
		Groups: []Group{{ID: "g", Title: "G", CollapseAfter: new(1)}},
	}
	layout.Landing = Landing(layout)
	if layout.Landing != 4 {
		t.Fatalf("Landing = %d", layout.Landing)
	}
	if got, want := Visible(layout), []bool{true, true, false, true, false}; !slices.Equal(got, want) {
		t.Errorf("Visible = %v, want %v", got, want)
	}
}

func TestArrangeMovesPinnedEntriesFirst(t *testing.T) {
	layout := Arrange(Layout{Entries: []Entry{
		{Tile: "a", Size: SizeFull}, {Tile: "b", Size: SizeFull}, {Tile: "c", Size: SizeFull, Pin: true},
		{Tile: "d", Size: SizeFull}, {Tile: "e", Size: SizeHalf, Pin: true},
	}})
	var order []string
	for _, entry := range layout.Entries {
		order = append(order, entry.Tile)
	}
	if want := []string{"c", "e", "a", "b", "d"}; !slices.Equal(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
	if layout.Landing != 3 {
		t.Errorf("Landing = %d, want 3", layout.Landing)
	}
}
