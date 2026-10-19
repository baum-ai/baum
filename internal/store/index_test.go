package store

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSnapshotRunLifecycleAndVersions(t *testing.T) {
	ctx := context.Background()
	s := opened(t)
	k := Key{"acme/widgets", 7}
	first, err := s.BeginRun(ctx, k, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.Snapshot(ctx)
	if err != nil || len(rows) != 1 || rows[0].Run.State != "generating" || rows[0].VersionCount != 0 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	newer, err := s.BeginRun(ctx, k, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	v := version(k, 1)
	cost := 0.12
	v.Metadata.RecordedCostUSD = &cost
	v.Document = []byte(`{"review":{"repository":"acme/widgets","pull_request":7,"version":1,"pr_state":"merged"},"summary":{"title":"Saved title","risk_score":{"score":42}}}`)
	if err = s.Put(ctx, k, Version{}, v); err != nil {
		t.Fatal(err)
	}
	rows, err = s.Snapshot(ctx)
	if err != nil || rows[0].Run.ID != newer || rows[0].Run.State != "generating" || rows[0].VersionCount != 1 {
		t.Fatalf("older publication changed newer run: %+v %v", rows, err)
	}
	if err = s.HeartbeatRun(ctx, newer, time.Now().Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, newer, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, first, "budget"); err != nil {
		t.Fatal(err)
	}
	rows, err = s.Snapshot(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	row := rows[0]
	if row.Run.ID != newer || row.Run.State != "completed" || row.VersionCount != 1 || row.Title != "Saved title" || row.PRState != "merged" || string(row.RiskScore) != `{"score":42}` || *row.Latest.RecordedCostUSD != cost {
		t.Fatalf("row=%+v", row)
	}
	failed, err := s.BeginRun(ctx, k, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, failed, "validation"); err != nil {
		t.Fatal(err)
	}
	rows, err = s.Snapshot(ctx)
	if err != nil || rows[0].VersionCount != 1 || rows[0].Run.Reason != "validation" {
		t.Fatal(rows, err)
	}
	if err = s.FinishRun(ctx, failed, "secret provider error"); err == nil {
		t.Fatal("accepted arbitrary failure")
	}
}

func TestSnapshotLegacyAndExpiredAttempt(t *testing.T) {
	ctx := context.Background()
	s := opened(t)
	k := Key{"a/b", 1}
	if err := s.Put(ctx, k, Version{}, version(k, 1)); err != nil {
		t.Fatal(err)
	}
	db, err := s.database(true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DROP TABLE runs`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	rows, err := s.Snapshot(ctx)
	if err != nil || len(rows) != 1 || rows[0].Run != nil {
		t.Fatal(rows, err)
	}
	s, err = Open("", s.Root(), true)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.BeginRun(ctx, k, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	db, err = s.database(true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE runs SET expires=? WHERE id=?`, time.Now().Add(-time.Second).UnixNano(), id); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	rows, err = s.Snapshot(ctx)
	if err != nil || rows[0].Run.State != "failed" || rows[0].Run.Reason != "interrupted" {
		t.Fatal(rows, err)
	}
}

func TestSnapshotConcurrentPublication(t *testing.T) {
	ctx := context.Background()
	s := opened(t)
	k := Key{"a/b", 1}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var previous Version
		for n := 1; n <= 40; n++ {
			if err := s.Put(ctx, k, previous, version(k, n)); err != nil {
				t.Error(err)
				return
			}
			previous = version(k, n)
		}
	}()
	for i := 0; i < 40; i++ {
		rows, err := s.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) > 0 && rows[0].Latest.Version != rows[0].VersionCount {
			t.Fatalf("torn snapshot: %+v", rows[0])
		}
	}
	wg.Wait()
}

func BenchmarkSnapshot(b *testing.B) {
	s, err := Open("", b.TempDir(), true)
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	for pr := 1; pr <= 100; pr++ {
		k := Key{"a/b", pr}
		var previous Version
		for n := 1; n <= 10; n++ {
			v := version(k, n)
			v.Document = append(v.Document[:len(v.Document)-1], []byte(`,"padding":"`+strings.Repeat("x", 32<<10)+`"}`)...)
			if err = s.Put(ctx, k, previous, v); err != nil {
				b.Fatal(err)
			}
			previous = v
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := s.Snapshot(ctx)
		if err != nil || len(rows) != 100 {
			b.Fatal(fmt.Sprint(rows), err)
		}
	}
}
