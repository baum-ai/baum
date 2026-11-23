package decisionlog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/locallink/linktest"
	"github.com/baum-ai/baum/internal/workstats"
)

func TestFeatureCommitRangesReuseAndInvalidate(t *testing.T) {
	f := linktest.New(t)
	ctx := context.Background()
	clone, err := gitlocal.Open(ctx, f.Repo, gitlocal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = clone.Close() }()
	cache := &featureCommitCache{}
	p := StoredPull{BaseSHA: f.Base, HeadSHA: f.Head}
	first, err := cache.read(ctx, clone, f.Repo, p, false)
	if err != nil || len(first) == 0 {
		t.Fatalf("cold read %v %v", first, err)
	}
	count := func() int64 { return workstats.Take().Counters["git.processes"] }
	before := count()
	again, err := cache.read(ctx, clone, f.Repo, p, false)
	if err != nil || !reflect.DeepEqual(first, again) || count() != before {
		t.Fatalf("unchanged read runs git or differs: %v", err)
	}
	again[0].Files[0] = "mutated"
	again[0].Subject = "mutated"
	third, err := cache.read(ctx, clone, f.Repo, p, false)
	if err != nil || !reflect.DeepEqual(first, third) {
		t.Fatal("caller mutation affected cache")
	}
	before = count()
	_, err = cache.read(ctx, clone, f.Repo, p, true)
	if err != nil || count() == before {
		t.Fatal("daily safety read skipped git")
	}
	// A new shallow boundary changes the result even at the same OIDs.
	if err := os.WriteFile(filepath.Join(clone.PrivateDir(), "shallow"), []byte(f.Head+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before = count()
	_, err = cache.read(ctx, clone, f.Repo, p, false)
	if err != nil || count() == before {
		t.Fatalf("shallow change not read: %v", err)
	}
	// Failed ranges must not become successful memo entries.
	missing := StoredPull{BaseSHA: f.Base, HeadSHA: strings.Repeat("e", 40)}
	beforeLen := len(cache.entries)
	if _, err = cache.read(ctx, clone, f.Repo, missing, false); err == nil || len(cache.entries) != beforeLen {
		t.Fatal("failed range retained")
	}
	// More than the entry bound evicts, even when each answer is small.
	if err := os.Remove(filepath.Join(clone.PrivateDir(), "shallow")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < featureCommitRanges+1; i++ {
		if _, err = cache.read(ctx, clone, fmt.Sprint(i), p, false); err != nil {
			t.Fatal(err)
		}
	}
	if len(cache.entries) > featureCommitRanges || cache.bytes > featureCommitBytes {
		t.Fatal("range cache exceeded bounds")
	}
}
