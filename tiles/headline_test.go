package tiles

import (
	"strings"
	"testing"
)

func TestHeadline(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"One sentence.", "One sentence"},
		{"First one. Second one.", "First one"},
		{"Version 1.2 is out. Then more.", "Version 1.2 is out"},
		{"A clause; then more. Next.", "A clause"},
		{"No end", "No end"},
		{"First line\nSecond line.", "First line"},
		{"Why? Because.", "Why"},
		{strings.Repeat("x", 400), strings.Repeat("x", 299) + "…"},
	} {
		if got := headline(tc.in); got != tc.want {
			t.Errorf("headline(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
