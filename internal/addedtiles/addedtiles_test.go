package addedtiles_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/addedtiles"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/tiles/library"
)

const pr = "acme/widgets#7"

type events struct {
	mu   sync.Mutex
	seen []string
}

func (e *events) changed(pr string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seen = append(e.seen, pr)
}

func newService(t *testing.T) (*addedtiles.Service, *events) {
	t.Helper()
	e := &events{}
	return &addedtiles.Service{Path: filepath.Join(appdbtest.TempDir(t), "app.sqlite"), Changed: e.changed}, e
}

// addAPI adds the api_change library type to pr, on section.
func addAPI(t *testing.T, s *addedtiles.Service, section string) {
	t.Helper()
	entry, _ := library.Get("api_change")
	if err := s.Add(context.Background(), pr, addedtiles.Added{Tile: "api_change", Kind: addedtiles.KindLibrary, Name: "api_change", Digest: entry.Definition.Digest, Section: section, By: "chat"}); err != nil {
		t.Fatal(err)
	}
}

// An added tile is listed with its section; removing it hides it, and
// restoring it puts it back where it was. Each write tells Changed. A
// remove or restore of nothing is ErrNotFound.
func TestAddRemoveRestore(t *testing.T) {
	ctx := context.Background()
	s, e := newService(t)
	if added, err := s.List(ctx, pr); err != nil || len(added) != 0 {
		t.Fatalf("a database that does not exist yet: %+v %v", added, err)
	}
	addAPI(t, s, "sec_api")
	added, err := s.List(ctx, pr)
	if err != nil || len(added) != 1 || added[0].Tile != "api_change" || added[0].Section != "sec_api" || added[0].Kind != addedtiles.KindLibrary || added[0].At == "" {
		t.Fatalf("added: %+v %v", added, err)
	}
	if err := s.Restore(ctx, pr, "api_change"); !errors.Is(err, addedtiles.ErrNotFound) {
		t.Errorf("restoring a tile that is not removed: %v", err)
	}
	if err := s.Remove(ctx, pr, "api_change"); err != nil {
		t.Fatal(err)
	}
	if added, _ := s.List(ctx, pr); len(added) != 0 {
		t.Errorf("a removed tile is listed: %+v", added)
	}
	if err := s.Remove(ctx, pr, "api_change"); !errors.Is(err, addedtiles.ErrNotFound) {
		t.Errorf("removing a removed tile: %v", err)
	}
	if err := s.Restore(ctx, pr, "api_change"); err != nil {
		t.Fatal(err)
	}
	if added, _ := s.List(ctx, pr); len(added) != 1 || added[0].Section != "sec_api" {
		t.Errorf("the restored tile: %+v", added)
	}
	if err := s.Remove(ctx, "acme/widgets#8", "api_change"); !errors.Is(err, addedtiles.ErrNotFound) {
		t.Errorf("removing another pull request's tile: %v", err)
	}
	if len(e.seen) != 3 {
		t.Errorf("events %v, want one per write", e.seen)
	}
	// Adding it again replaces it, on its new section.
	addAPI(t, s, "context")
	if added, _ := s.List(ctx, pr); len(added) != 1 || added[0].Section != "context" {
		t.Errorf("added again: %+v", added)
	}
}

// An add the host did not check is refused.
func TestAddRefusesWhatIsNotAnAddedTile(t *testing.T) {
	s, _ := newService(t)
	for _, a := range []addedtiles.Added{
		{Tile: "", Kind: addedtiles.KindLibrary, Name: "x", Digest: "sha256:x"},
		{Tile: "x", Kind: "layout", Name: "x", Digest: "sha256:x"},
		{Tile: "x", Kind: addedtiles.KindCustom, Name: "x"},
	} {
		if err := s.Add(context.Background(), pr, a); err == nil {
			t.Errorf("%+v was added", a)
		}
	}
	if err := s.Add(context.Background(), "not a key", addedtiles.Added{Tile: "x", Kind: addedtiles.KindLibrary, Name: "x", Digest: "sha256:x"}); err == nil {
		t.Error("an add to no pull request was taken")
	}
}

// A removed tile is kept for its Undo for KeepRemoved, then dropped.
func TestRemovedTilesAreDropped(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	s, _ := newService(t)
	s.Now = func() time.Time { return now }
	addAPI(t, s, "")
	if err := s.Remove(ctx, pr, "api_change"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(addedtiles.KeepRemoved + time.Minute)
	addAPI(t, s, "")
	if err := s.Remove(ctx, pr, "api_change"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(addedtiles.KeepRemoved + time.Minute)
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Restore(ctx, pr, "api_change"); !errors.Is(err, addedtiles.ErrNotFound) {
		t.Errorf("a tile removed past KeepRemoved was restored: %v", err)
	}
}

func TestKey(t *testing.T) {
	if got := addedtiles.Key(" Acme/Widgets ", 7); got != "acme/widgets#7" || !addedtiles.ValidKey(got) {
		t.Errorf("key %q", got)
	}
	for _, key := range []string{"acme#7", "acme/widgets#0", "acme/widgets", "Acme/Widgets#7"} {
		if addedtiles.ValidKey(key) {
			t.Errorf("%q is valid", key)
		}
	}
	if !addedtiles.ValidKey("github.example.com/acme/widgets#7") {
		t.Error("an Enterprise key is not valid")
	}
}
