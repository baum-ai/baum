package cli

import (
	"context"
	"testing"

	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewindex"
)

type noRepositories struct{}

func (noRepositories) Repositories(context.Context) ([]repos.Repository, error) { return nil, nil }

// A client that reads the index on the event must not get the snapshot
// cached before the change.
func TestIndexChangedInvalidatesBeforePublishing(t *testing.T) {
	ctx := context.Background()
	index := reviewindex.NewCache(ctx, &reviewindex.Service{Registry: noRepositories{}})
	if _, err := index.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	before, ok := index.Fresh()
	if !ok {
		t.Fatal("no cached snapshot")
	}
	published := 0
	changed := indexChanged(index, func(kind string, _ any) error {
		if kind != "index" {
			t.Fatal(kind)
		}
		if _, ok := index.Fresh(); ok {
			t.Fatal("published before invalidating")
		}
		_, version, err := index.Current(ctx)
		if err != nil || version == before {
			t.Fatal("a reader on the event got the old snapshot", version, err)
		}
		published++
		return nil
	})
	changed()
	if published != 1 {
		t.Fatal("published", published)
	}
}
