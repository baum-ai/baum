package reviewindex

import (
	"testing"

	"github.com/baum-ai/baum/internal/store"
)

// A review is current at its own head and at the head it was kept for.
func TestCoversKeptHead(t *testing.T) {
	latest := &store.Metadata{Head: "a"}
	kept := &store.Kept{Head: "b"}
	for _, tc := range []struct {
		latest *store.Metadata
		kept   *store.Kept
		head   string
		want   bool
	}{
		{latest, nil, "a", true},
		{latest, nil, "b", false},
		{latest, kept, "b", true},
		{latest, kept, "c", false},
		{latest, kept, "", false},
		{nil, kept, "b", false},
	} {
		if got := covers(tc.latest, tc.kept, tc.head); got != tc.want {
			t.Errorf("covers(%+v, %+v, %q) = %v, want %v", tc.latest, tc.kept, tc.head, got, tc.want)
		}
	}
}
