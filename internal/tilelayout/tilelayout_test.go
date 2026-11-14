package tilelayout_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/appdb"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/tilelayout"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/schema"
	"github.com/baum-ai/baum/tiles"
)

const pr = "acme/widgets#7"

// served is the example review's tile document, composed with the layout's
// overlay, as the tile route serves it.
func served(t *testing.T, state tilelayout.State) *tiles.Document {
	t.Helper()
	normalized, err := review.NormalizeStored(schema.Example)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := tiles.FromReview(normalized, tiles.Options{})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := tiles.ApplyOverlay(doc, nil, state.Overlay, nil)
	composed, err := tiles.Compose(doc, &c)
	if err != nil {
		t.Fatal(err)
	}
	return composed
}

type events struct {
	mu   sync.Mutex
	seen []string
}

func (e *events) changed(pr string, revision int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seen = append(e.seen, fmt.Sprintf("%s@%d", pr, revision))
}

func newService(t *testing.T) (*tilelayout.Service, *events) {
	t.Helper()
	e := &events{}
	return &tilelayout.Service{Path: filepath.Join(appdbtest.TempDir(t), "app.sqlite"), Changed: e.changed}, e
}

func move(tile string, to tiles.Place) tiles.Op {
	return tiles.Op{Op: tiles.OpMove, Tile: tile, To: &to}
}

func TestEditUndoReset(t *testing.T) {
	ctx := context.Background()
	s, e := newService(t)
	state, err := s.Read(ctx, pr)
	if err != nil || state.Revision != 0 || state.Edited() {
		t.Fatalf("a layout never edited: %+v, %v", state, err)
	}
	change, err := s.Edit(ctx, pr, "chat", served(t, state), []tiles.Op{
		move("risks", tiles.Place{Edge: tiles.EdgeFirst}),
		{Op: tiles.OpHide, Tile: "overview"},
		{Op: tiles.OpHide, Tile: "context"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !change.Saved || change.State.Revision != 1 || !slices.Equal(change.Applied, []string{"Moved Risks to the top", "Removed Context"}) {
		t.Errorf("edit: %+v", change)
	}
	if len(change.Refused) != 1 || change.Refused[0].Index != 1 || change.Refused[0].Reason != "The overview always stays on the landing page" {
		t.Errorf("refused: %+v", change.Refused)
	}
	summary := change.State.Summary()
	if !summary.Edited || !summary.CanUndo || summary.Last == nil || summary.Last.By != "chat" || len(summary.Last.Summary) != 2 || !slices.Equal(summary.Last.Tiles, []string{"risks", "context"}) {
		t.Errorf("summary: %+v", summary)
	}
	if !slices.Equal(e.seen, []string{pr + "@1"}) {
		t.Errorf("events: %v", e.seen)
	}

	// Every op refused: nothing is written or published.
	refused, err := s.Edit(ctx, pr, "chat", served(t, change.State), []tiles.Op{{Op: tiles.OpHide, Tile: "context"}})
	if err != nil || refused.Saved || refused.State.Revision != 1 || refused.Refused[0].Reason != "Context is already hidden" {
		t.Errorf("an edit refused whole: %+v, %v", refused, err)
	}
	if len(e.seen) != 1 {
		t.Errorf("a refused edit published: %v", e.seen)
	}

	// The page's undo names the batch it shows.
	if _, err := s.Undo(ctx, pr, "edt_000000000000"); !errors.Is(err, tilelayout.ErrStale) {
		t.Errorf("an undo of another batch: %v", err)
	}
	reset, err := s.Reset(ctx, pr, "page")
	if err != nil || reset.State.Edited() || !reset.State.CanUndo() || reset.State.Revision != 2 {
		t.Fatalf("reset: %+v, %v", reset, err)
	}
	if _, err := s.Reset(ctx, pr, "page"); !errors.Is(err, tilelayout.ErrNotEdited) {
		t.Errorf("a reset of the generated layout: %v", err)
	}
	undone, err := s.Undo(ctx, pr, reset.State.Overlay.Last().ID)
	if err != nil || !undone.State.Edited() || !slices.Equal(undone.Undone, []string{"Reset the layout"}) {
		t.Fatalf("undoing the reset: %+v, %v", undone, err)
	}
	undone, err = s.Undo(ctx, pr, "")
	if err != nil || undone.State.Edited() || undone.State.CanUndo() || !slices.Equal(undone.Undone, change.Applied) {
		t.Fatalf("undoing the edit: %+v, %v", undone, err)
	}
	if _, err := s.Undo(ctx, pr, ""); !errors.Is(err, tilelayout.ErrNothingToUndo) {
		t.Errorf("undo with nothing to undo: %v", err)
	}
	if got := len(e.seen); got != 4 {
		t.Errorf("%d events for 4 writes: %v", got, e.seen)
	}
	if _, err := s.Edit(ctx, pr, "chat", served(t, undone.State), nil); !errors.Is(err, tilelayout.ErrNoOps) {
		t.Errorf("an edit with no op: %v", err)
	}
	if _, err := s.Edit(ctx, pr, "chat", served(t, undone.State), make([]tiles.Op, tilelayout.MaxBatch+1)); !errors.Is(err, tilelayout.ErrNoOps) {
		t.Errorf("an edit with too many ops: %v", err)
	}
}

// Writes from the panel and the page at once each land, as their own batch.
func TestConcurrentEdits(t *testing.T) {
	ctx := context.Background()
	s, e := newService(t)
	doc := served(t, tilelayout.State{})
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := range 12 {
		wg.Go(func() {
			size := []tiles.Size{tiles.SizeFull, tiles.SizeHalf, tiles.SizeThird}[i%3]
			if size == tiles.SizeThird {
				size = tiles.SizeFull
			}
			_, err := s.Edit(ctx, pr, "page", doc, []tiles.Op{{Op: tiles.OpResize, Tile: "evidence", Size: size}})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	state, err := s.Read(ctx, pr)
	if err != nil || state.Revision != 12 || len(state.Overlay.Batches) != 12 || len(e.seen) != 12 {
		t.Errorf("revision %d, %d batches, %d events, %v", state.Revision, len(state.Overlay.Batches), len(e.seen), err)
	}
}

// The most recently edited Keep pull requests are kept.
func TestRetention(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	start := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	tick := 0
	s.Now = func() time.Time { tick++; return start.Add(time.Duration(tick) * time.Second) }
	doc := served(t, tilelayout.State{})
	for i := range tilelayout.Keep + 3 {
		if _, err := s.Edit(ctx, fmt.Sprintf("acme/widgets#%d", i+1), "chat", doc, []tiles.Op{{Op: tiles.OpHide, Tile: "context"}}); err != nil {
			t.Fatal(err)
		}
	}
	for i, want := range map[int]bool{1: false, 3: false, 4: true, tilelayout.Keep + 3: true} {
		state, err := s.Read(ctx, fmt.Sprintf("acme/widgets#%d", i))
		if err != nil || state.Edited() != want {
			t.Errorf("pull request %d kept %v, want %v (%v)", i, state.Edited(), want, err)
		}
	}
}

// A database an older app made, with no table, reads as no edits; a row
// this build cannot read is no edits too, and the next write replaces it.
func TestOlderDatabase(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	if state, err := s.Read(ctx, pr); err != nil || state.Revision != 0 {
		t.Fatalf("no database: %+v, %v", state, err)
	}
	db, err := appdb.Open(s.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := s.Read(ctx, pr); err != nil || state.Revision != 0 {
		t.Fatalf("no table: %+v, %v", state, err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS tile_overlays(pr TEXT PRIMARY KEY, revision INTEGER NOT NULL, updated TEXT NOT NULL, value BLOB NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tile_overlays VALUES (?, 4, '2026-01-01T00:00:00Z', '{"schema":"baum.layout-overlay/9"}')`, pr); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	state, err := s.Read(ctx, pr)
	if err != nil || state.Revision != 4 || state.Overlay != nil || state.Edited() {
		t.Fatalf("an unreadable row: %+v, %v", state, err)
	}
	change, err := s.Edit(ctx, pr, "chat", served(t, state), []tiles.Op{{Op: tiles.OpHide, Tile: "context"}})
	if err != nil || change.State.Revision != 5 || len(change.State.Overlay.Batches) != 1 {
		t.Fatalf("the write after it: %+v, %v", change, err)
	}
}

func TestKey(t *testing.T) {
	if got := tilelayout.Key(" Acme/Widgets ", 7); got != "acme/widgets#7" {
		t.Errorf("Key = %q", got)
	}
	for key, valid := range map[string]bool{"acme/widgets#7": true, "ghe.example.com/acme/widgets#12": true, "acme#7": false, "acme/widgets#0": false, "Acme/widgets#7": false} {
		if tilelayout.ValidKey(key) != valid {
			t.Errorf("ValidKey(%q) = %v", key, !valid)
		}
	}
}
