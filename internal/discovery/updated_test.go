package discovery

import (
	"context"
	"testing"
)

// The review index cache is invalidated through Updated whenever what
// Cached returns may have changed.
func TestUpdatedFollowsWhatCachedReads(t *testing.T) {
	s, _ := service(t, recordedFixture())
	calls := 0
	s.Updated = func() { calls++ }
	if _, err := s.Poll(context.Background(), testRepo); err != nil {
		t.Fatal(err)
	}
	if calls == 0 {
		t.Fatal("a poll did not report its write")
	}
	calls = 0
	s.ResetIdentity()
	if calls != 1 {
		t.Fatal("identity reset reported", calls)
	}
	calls = 0
	if _, err := s.Cached(context.Background(), testRepo); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("a read reported a change", calls)
	}
}
