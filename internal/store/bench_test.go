package store_test

import (
	"context"
	"testing"

	"github.com/baum-ai/baum/internal/benchcorpus"
	"github.com/baum-ai/baum/internal/store"
)

// BenchmarkSnapshotCorpus measures Store.Snapshot, the per-clone read behind every
// index build. BAUM_BENCH_STORE selects a real store copy (read-only);
// otherwise a synthetic corpus is generated.
func BenchmarkSnapshotCorpus(b *testing.B) {
	root, label := benchcorpus.Root(b)
	s, err := store.Open("", root, false)
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	rows, err := s.Snapshot(ctx)
	if err != nil || len(rows) == 0 {
		b.Fatalf("corpus %s: %d rows, err=%v", label, len(rows), err)
	}
	b.Logf("corpus=%s prs=%d", label, len(rows))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := s.Snapshot(ctx); err != nil {
			b.Fatal(err)
		}
	}
}
