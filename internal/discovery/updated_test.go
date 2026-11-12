package discovery

import (
	"context"
	"testing"
	"time"
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

// The credential's budget record is not read by Cached: writing it alone,
// as a probe does, does not report a change, so the review index does not
// rebuild for it.
func TestBudgetWriteIsNotReported(t *testing.T) {
	ctx := context.Background()
	s, now := service(t, recordedFixture())
	if _, err := s.Poll(ctx, testRepo); err != nil {
		t.Fatal(err)
	}
	reader, err := s.client.REST(ctx, testRepo)
	if err != nil {
		t.Fatal(err)
	}
	key, err := s.path(reader.CacheIdentity(), "")
	if err != nil {
		t.Fatal(err)
	}
	key += budgetSuffix
	writes := func() uint64 {
		s.writesMu.Lock()
		defer s.writesMu.Unlock()
		return s.writes[key]
	}
	before, err := s.Revision(ctx, testRepo)
	if err != nil {
		t.Fatal(err)
	}
	written := writes()
	calls := 0
	s.Updated = func() { calls++ }
	remaining := 1
	if err := s.save(key, diskCache{GraphQLRate: RateLimit{Remaining: &remaining, Reset: now.Add(time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	if writes() == written {
		t.Fatal("the budget record was not written")
	}
	if calls != 0 {
		t.Fatal("a budget write reported a change", calls)
	}
	if after, err := s.Revision(ctx, testRepo); err != nil || after != before {
		t.Fatalf("revision moved: %+v -> %+v (%v)", before, after, err)
	}
}
