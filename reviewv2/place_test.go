package reviewv2

import (
	"encoding/json"
	"testing"
)

// An added tile goes where it is asked for, else on the current section
// whose hunks it cites most, else on the overview.
func TestOwnerFor(t *testing.T) {
	d := Document{Sections: []Section{
		{ID: "sec_a", Hunks: []string{"h_1"}},
		{ID: "sec_gone", Hunks: []string{"h_2"}, Derived: json.RawMessage(`{"state":"stale"}`)},
		{ID: "sec_b", Hunks: []string{"h_2", "h_3"}},
	}}
	cases := []struct {
		asked  string
		hunks  []string
		expect string
	}{
		{OwnerOverview, []string{"h_1"}, OwnerOverview},
		{OwnerContext, []string{"h_1"}, OwnerContext},
		{"sec_a", []string{"h_3"}, "sec_a"},
		{"", []string{"h_2", "h_3"}, "sec_b"},
		{"sec_gone", []string{"h_1"}, "sec_a"},
		{"", nil, OwnerOverview},
		{"sec_nope", []string{"h_9"}, OwnerOverview},
	}
	for _, c := range cases {
		if got := OwnerFor(d, c.asked, c.hunks); got != c.expect {
			t.Errorf("OwnerFor(%q, %v) = %q, want %q", c.asked, c.hunks, got, c.expect)
		}
	}
}
