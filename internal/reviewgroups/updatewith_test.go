package reviewgroups

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestUpdateWithReadsInTheTransactionAndWritesNothingWhenRefused(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	g, err := s.Create(ctx, "owner/repo", "Checkout", []int{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	refused := errors.New("refused")
	name := "Renamed"
	if _, err := s.UpdateWith(ctx, g.ID, func(read Group) (*string, []int, error) {
		if read.Name != "Checkout" || !slices.Equal(read.Members, []int{1, 2}) {
			t.Errorf("read %+v", read)
		}
		return &name, []int{1, 3}, refused
	}); !errors.Is(err, refused) {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, g.ID); got.Name != "Checkout" || !slices.Equal(got.Members, []int{1, 2}) {
		t.Errorf("a refused update wrote %+v", got)
	}
	got, err := s.UpdateWith(ctx, g.ID, func(read Group) (*string, []int, error) { return &name, append(slices.Clone(read.Members), 3), nil })
	if err != nil || got.Name != name || !slices.Equal(got.Members, []int{1, 2, 3}) {
		t.Fatal(got, err)
	}
	// CheckUpdate refuses what Update refuses, with the same error.
	for _, members := range [][]int{{1}, {1, 1}, {0, 1}} {
		_, updateErr := s.Update(ctx, g.ID, nil, members)
		if err := CheckUpdate(nil, members); err == nil || updateErr == nil || err.Error() != updateErr.Error() {
			t.Errorf("%v: %v, update %v", members, err, updateErr)
		}
	}
	blank := " "
	if err := CheckUpdate(&blank, nil); err == nil {
		t.Error("a blank name")
	}
}
