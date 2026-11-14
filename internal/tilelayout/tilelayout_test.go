package tilelayout_test

import (
	"context"
	"encoding/json"
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
	change, err := s.Edit(ctx, pr, "chat", served(t, state), state.Revision, []tiles.Op{
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
	refused, err := s.Edit(ctx, pr, "chat", served(t, change.State), change.State.Revision, []tiles.Op{{Op: tiles.OpHide, Tile: "context"}})
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
	if _, err := s.Edit(ctx, pr, "chat", served(t, undone.State), undone.State.Revision, nil); !errors.Is(err, tilelayout.ErrNoOps) {
		t.Errorf("an edit with no op: %v", err)
	}
	if _, err := s.Edit(ctx, pr, "chat", served(t, undone.State), undone.State.Revision, make([]tiles.Op, tilelayout.MaxBatch+1)); !errors.Is(err, tilelayout.ErrNoOps) {
		t.Errorf("an edit with too many ops: %v", err)
	}
}

// Writes from the panel and the page at once each land, as their own batch.
func TestConcurrentEdits(t *testing.T) {
	ctx := context.Background()
	s, e := newService(t)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := range 12 {
		wg.Go(func() {
			count := 11 + i
			// A write that lost the race read the layout again, as the page does.
			for {
				state, err := s.Read(ctx, pr)
				if err == nil {
					_, err = s.Edit(ctx, pr, "page", served(t, state), state.Revision, []tiles.Op{{Op: tiles.OpChangesShown, Count: &count}})
				}
				if !errors.Is(err, tilelayout.ErrStale) {
					errs <- err
					return
				}
			}
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
		if _, err := s.Edit(ctx, fmt.Sprintf("acme/widgets#%d", i+1), "chat", doc, 0, []tiles.Op{{Op: tiles.OpHide, Tile: "context"}}); err != nil {
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
	change, err := s.Edit(ctx, pr, "chat", served(t, state), state.Revision, []tiles.Op{{Op: tiles.OpHide, Tile: "context"}})
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

// An undo can be taken back until the next edit or reset: Redo puts the batch
// back, naming it protects against a layout that changed meanwhile, and the
// summary says what a redo would restore.
func TestRedo(t *testing.T) {
	ctx := context.Background()
	s, e := newService(t)
	state, _ := s.Read(ctx, pr)
	edit, err := s.Edit(ctx, pr, "chat", served(t, state), state.Revision, []tiles.Op{{Op: tiles.OpHide, Tile: "context"}})
	if err != nil {
		t.Fatal(err)
	}
	if edit.State.Summary().CanRedo {
		t.Error("a layout never undone can redo")
	}
	if _, err := s.Redo(ctx, pr, ""); !errors.Is(err, tilelayout.ErrNothingToRedo) {
		t.Errorf("redo with nothing undone: %v", err)
	}
	undone, err := s.Undo(ctx, pr, "")
	if err != nil {
		t.Fatal(err)
	}
	summary := undone.State.Summary()
	if undone.State.Edited() || !summary.CanRedo || summary.RedoBatch != edit.State.Overlay.Last().ID {
		t.Fatalf("after the undo: %+v", summary)
	}
	if _, err := s.Redo(ctx, pr, "edt_000000000000"); !errors.Is(err, tilelayout.ErrStale) {
		t.Errorf("redo of a batch that is not the next: %v", err)
	}
	redone, err := s.Redo(ctx, pr, summary.RedoBatch)
	if err != nil || !redone.Saved || !redone.State.Edited() || redone.State.CanRedo() || !slices.Equal(redone.Redone, edit.Applied) || redone.State.Revision != 3 {
		t.Fatalf("redo: %+v, %v", redone, err)
	}
	if len(e.seen) != 3 {
		t.Errorf("events: %v", e.seen)
	}
	// A new edit after an undo forgets what was undone.
	if _, err := s.Undo(ctx, pr, ""); err != nil {
		t.Fatal(err)
	}
	state, _ = s.Read(ctx, pr)
	if _, err := s.Edit(ctx, pr, "page", served(t, state), state.Revision, []tiles.Op{{Op: tiles.OpPin, Tile: "decisions"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Redo(ctx, pr, ""); !errors.Is(err, tilelayout.ErrNothingToRedo) {
		t.Errorf("redo after a new edit: %v", err)
	}
	// So does a reset; and undoing the reset can itself be redone.
	if _, err := s.Undo(ctx, pr, ""); err != nil {
		t.Fatal(err)
	}
	state, _ = s.Read(ctx, pr)
	if _, err := s.Edit(ctx, pr, "page", served(t, state), state.Revision, []tiles.Op{{Op: tiles.OpPin, Tile: "decisions"}}); err != nil {
		t.Fatal(err)
	}
	state, _ = s.Read(ctx, pr)
	if _, err := s.Edit(ctx, pr, "page", served(t, state), state.Revision, []tiles.Op{{Op: tiles.OpHide, Tile: "context"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Undo(ctx, pr, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reset(ctx, pr, "page"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Redo(ctx, pr, ""); !errors.Is(err, tilelayout.ErrNothingToRedo) {
		t.Errorf("redo after a reset: %v", err)
	}
	undoneReset, err := s.Undo(ctx, pr, "")
	if err != nil || !slices.Equal(undoneReset.Undone, []string{"Reset the layout"}) || !undoneReset.State.CanRedo() {
		t.Fatalf("undoing the reset: %+v, %v", undoneReset, err)
	}
	if again, err := s.Redo(ctx, pr, ""); err != nil || again.State.Edited() {
		t.Errorf("redoing the reset: %+v, %v", again, err)
	}
}

// An edit is checked inside its write against the layout it was built from:
// when the layout moved since, it is refused as stale, and nothing is saved.
func TestEditAgainstAMovedLayout(t *testing.T) {
	ctx := context.Background()
	s, e := newService(t)
	state, _ := s.Read(ctx, pr)
	doc := served(t, state)
	if _, err := s.Edit(ctx, pr, "page", doc, state.Revision, []tiles.Op{{Op: tiles.OpHide, Tile: "context"}}); err != nil {
		t.Fatal(err)
	}
	// The same document, built at revision 0, now describes a layout in which
	// Context is shown: hiding it again would pass its check, but the layout
	// has moved.
	if _, err := s.Edit(ctx, pr, "chat", doc, state.Revision, []tiles.Op{{Op: tiles.OpHide, Tile: "context"}}); !errors.Is(err, tilelayout.ErrStale) {
		t.Fatalf("an edit from a layout that moved: %v", err)
	}
	after, _ := s.Read(ctx, pr)
	if after.Revision != 1 || len(after.Overlay.Batches) != 1 || len(e.seen) != 1 {
		t.Errorf("a stale edit wrote: revision %d, %d batches, %d events", after.Revision, len(after.Overlay.Batches), len(e.seen))
	}
	// Read again, the same op is refused in the host's words.
	again, err := s.Edit(ctx, pr, "chat", served(t, after), after.Revision, []tiles.Op{{Op: tiles.OpHide, Tile: "context"}})
	if err != nil || again.Saved || len(again.Refused) != 1 {
		t.Errorf("after reading again: %+v, %v", again, err)
	}
}

// A reset of an overlay at its byte bound succeeds, and undoing it brings
// back what remained.
func TestResetOfAFullOverlay(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	state, _ := s.Read(ctx, pr)
	// Edits with long tile ids are refused by CheckOp, so the full overlay
	// is stored the way an earlier build wrote it.
	full := tiles.Overlay{Schema: tiles.OverlayName, Batches: []tiles.Batch{
		{ID: tiles.BaseBatch, At: "2026-11-14T10:00:00Z", By: "page", Summary: []string{}},
		{ID: "edt_000000000001", At: "2026-11-14T10:00:00Z", By: "page", Ops: []tiles.Op{{Op: tiles.OpHide, Tile: "context"}}, Summary: []string{"Removed Context"}},
	}}
	for {
		encoded, _ := json.Marshal(full)
		if len(encoded) > tiles.MaxOverlayBytes-100 {
			break
		}
		full.Batches[0].Ops = append(full.Batches[0].Ops, tiles.Op{Op: tiles.OpResize, Tile: fmt.Sprintf("p%05d", len(full.Batches[0].Ops)), Size: tiles.SizeFull})
	}
	encoded, _ := json.Marshal(full)
	db, err := appdb.Open(s.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS tile_overlays(pr TEXT PRIMARY KEY, revision INTEGER NOT NULL, updated TEXT NOT NULL, value BLOB NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tile_overlays VALUES (?, 1, '2026-01-01T00:00:00Z', ?)`, pr, encoded); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	state, err = s.Read(ctx, pr)
	if err != nil || !state.Edited() {
		t.Fatalf("the full overlay: %+v, %v", state.Edited(), err)
	}
	reset, err := s.Reset(ctx, pr, "page")
	if err != nil || reset.State.Edited() || !reset.State.CanUndo() {
		t.Fatalf("reset of a full overlay: %+v, %v", reset, err)
	}
	undone, err := s.Undo(ctx, pr, "")
	if err != nil || !undone.State.Edited() {
		t.Errorf("undoing the reset restored nothing: %+v, %v", undone, err)
	}
}
