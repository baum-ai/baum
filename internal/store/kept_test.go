package store

import (
	"context"
	"testing"
	"time"
)

// A kept head belongs to its version: the snapshot and KeptHead report the
// latest version's, the latest record replaces an earlier one, and a newer
// version is not kept until triage keeps it.
func TestKeptHeads(t *testing.T) {
	ctx := context.Background()
	s := opened(t)
	k := Key{"Acme/Widgets", 7}
	if kept, err := s.KeptHead(ctx, k, 1); err != nil || kept != nil {
		t.Fatalf("no kept head yet: %+v %v", kept, err)
	}
	first := version(k, 1)
	if err := s.Put(ctx, k, Version{}, first); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	if err := s.KeepHead(ctx, k, 1, "head-b", "base-a", "typo", at); err != nil {
		t.Fatal(err)
	}
	if err := s.KeepHead(ctx, k, 1, "head-c", "base-b", "comments only", at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	kept, err := s.KeptHead(ctx, k, 1)
	if err != nil || kept == nil || kept.Head != "head-c" || kept.Base != "base-b" || kept.Reason != "comments only" || !kept.At.Equal(at.Add(time.Minute)) {
		t.Fatalf("kept %+v %v", kept, err)
	}
	rows, err := s.Snapshot(ctx)
	if err != nil || len(rows) != 1 || rows[0].Kept == nil || rows[0].Kept.Head != "head-c" {
		t.Fatalf("rows %+v %v", rows, err)
	}
	latest, _ := s.Latest(ctx, k)
	second := version(k, 2)
	if err := s.Put(ctx, k, latest, second); err != nil {
		t.Fatal(err)
	}
	rows, err = s.Snapshot(ctx)
	if err != nil || len(rows) != 1 || rows[0].Kept != nil {
		t.Fatalf("a newer version inherited the kept head: %+v %v", rows, err)
	}
	if err := s.KeepHead(ctx, k, 0, "x", "", "", at); err == nil {
		t.Fatal("kept a head for version 0")
	}
}
