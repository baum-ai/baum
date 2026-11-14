// Package tilelayout keeps each person's own tile layout edits: one layout
// overlay (tiles.Overlay) per pull request, in the private application
// database (app.sqlite), never in the review store a version is saved in.
// The session panel's tools and the review page's buttons both write through
// Service; the tile routes read the overlay and apply it to whatever version
// they serve (tiles.ApplyOverlay), so a person's layout carries over to the
// pull request's later versions.
package tilelayout

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/appdb"
	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/tiles"
)

// schema is the overlays' table: one row per pull request, its overlay and a
// revision that moves with every write. It is created when a write opens the
// database; an older app ignores it, and a read of a database without it
// finds no edits.
const schema = `CREATE TABLE IF NOT EXISTS tile_overlays(
  pr TEXT PRIMARY KEY,
  revision INTEGER NOT NULL,
  updated TEXT NOT NULL,
  value BLOB NOT NULL)`

// Keep is how many pull requests' overlays are kept, the most recently
// edited first.
const Keep = 200

// MaxBatch bounds the ops of one edit.
const MaxBatch = tiles.MaxOverlayOps

var (
	// ErrNothingToUndo is an undo with no batch to remove.
	ErrNothingToUndo = errors.New("Nothing to undo")
	// ErrNotEdited is a reset of a layout that is already the generated one.
	ErrNotEdited = errors.New("The layout is already the generated one")
	// ErrStale is an undo of a batch that is no longer the newest.
	ErrStale = errors.New("The layout changed meanwhile; read it again")
	// ErrNoOps is an edit with no op, or more than MaxBatch.
	ErrNoOps = fmt.Errorf("An edit has 1 to %d ops", MaxBatch)
	// ErrUnsaved is a write the database did not take.
	ErrUnsaved = errors.New("The layout could not be saved; try again")
)

// pullKey is a pull request's key: owner/repository#number, lowercased,
// optionally after an Enterprise host, as uistate keys reviews.
var pullKey = regexp.MustCompile(`^[a-z0-9._-]+(?:/[a-z0-9._-]+){1,2}#[1-9][0-9]*$`)

// Key is the key of a pull request's overlay.
func Key(repository string, number int) string {
	return strings.ToLower(strings.TrimSpace(repository)) + "#" + strconv.Itoa(number)
}

// ValidKey reports whether key is a pull request's key.
func ValidKey(key string) bool { return len(key) <= 300 && pullKey.MatchString(key) }

// State is a pull request's layout: its overlay (nil for none) and revision,
// 0 when it was never edited.
type State struct {
	PR       string
	Revision int64
	Overlay  *tiles.Overlay
}

// Edited reports whether the layout has edits to apply.
func (s State) Edited() bool { return s.Overlay.Edited() }

// CanUndo reports whether there is a batch to undo.
func (s State) CanUndo() bool { return s.Overlay.CanUndo() }

// Summary is what the page and the tools say of a layout.
type Summary struct {
	Revision int64 `json:"revision"`
	Edited   bool  `json:"edited"`
	CanUndo  bool  `json:"can_undo"`
	// Last is the batch an undo removes, if any.
	Last    *Last `json:"last"`
	Batches int   `json:"batches"`
}

// Last names the newest batch, and the tiles its ops name, which the page
// marks briefly.
type Last struct {
	ID      string   `json:"id"`
	By      string   `json:"by"`
	Reset   bool     `json:"reset,omitempty"`
	Summary []string `json:"summary"`
	Tiles   []string `json:"tiles,omitempty"`
}

// Summary sums the state up.
func (s State) Summary() Summary {
	out := Summary{Revision: s.Revision, Edited: s.Edited(), CanUndo: s.CanUndo()}
	if s.Overlay != nil {
		out.Batches = len(s.Overlay.Batches)
	}
	if last := s.Overlay.Last(); last != nil {
		out.Last = &Last{ID: last.ID, By: last.By, Reset: last.Reset, Summary: last.Summary}
		if out.Last.Summary == nil {
			out.Last.Summary = []string{}
		}
		for _, op := range last.Ops {
			tile := op.Tile
			if op.Add != nil {
				tile = tiles.LibraryTileID(op.Add.Name)
			}
			if tile != "" && !slices.Contains(out.Last.Tiles, tile) {
				out.Last.Tiles = append(out.Last.Tiles, tile)
			}
		}
	}
	return out
}

// Refusal is an op of an edit that was refused, with the host's reason.
type Refusal struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

// Change is what a write did: the new state, and for an edit the summary
// lines of the ops that applied and the ops refused; for an undo the summary
// of the batch it removed.
type Change struct {
	State   State
	Applied []string
	Refused []Refusal
	Undone  []string
	// Saved is false when nothing was written (every op refused).
	Saved bool
}

// Service reads and writes the overlays. The zero Path is the default
// application database.
type Service struct {
	Path string
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Changed is told of every committed write, with the pull request's key
	// and its new revision: the daemon publishes the layout event. It holds
	// no titles or summaries.
	Changed func(pr string, revision int64)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Read returns a pull request's layout. A database or table that does not
// exist yet, and an overlay that no longer reads as one, are no edits.
func (s *Service) Read(ctx context.Context, pr string) (State, error) {
	if !ValidKey(pr) {
		return State{}, errors.New("invalid pull request key")
	}
	db, release, err := appdb.Acquire(s.Path, true)
	if errors.Is(err, fs.ErrNotExist) {
		return State{PR: pr}, nil
	}
	if err != nil {
		return State{}, err
	}
	defer release()
	var revision int64
	var value []byte
	err = db.QueryRowContext(ctx, `SELECT revision, value FROM tile_overlays WHERE pr = ?`, pr).Scan(&revision, &value)
	switch {
	case errors.Is(err, sql.ErrNoRows), err != nil && strings.Contains(err.Error(), "no such table"):
		return State{PR: pr}, nil
	case err != nil:
		return State{}, err
	}
	overlay, err := tiles.ParseOverlay(value)
	if err != nil {
		overlay = nil
	}
	return State{PR: pr, Revision: revision, Overlay: overlay}, nil
}

// Layout is a pull request's layout as the tile views apply it; none when
// it cannot be read.
func (s *Service) Layout(repository string, number int) tileview.Layout {
	state, err := s.Read(context.Background(), Key(repository, number))
	if err != nil {
		return tileview.Layout{}
	}
	return tileview.Layout{Overlay: state.Overlay}
}

// Edit applies an edit made by ("chat" or "page") to the layout shown, doc:
// the served tile document of the pull request's latest version with its
// overlay applied. Each op is checked against it (tiles.CheckOp); those that
// pass are appended as one batch, with the host's summary lines, and the
// rest are refused with the host's reason. When every op is refused nothing
// is written.
func (s *Service) Edit(ctx context.Context, pr, by string, doc *tiles.Document, ops []tiles.Op) (Change, error) {
	if len(ops) < 1 || len(ops) > MaxBatch {
		return Change{}, ErrNoOps
	}
	if doc == nil {
		return Change{}, errors.New("This pull request has no saved review yet")
	}
	var accepted []tiles.Op
	var change Change
	for i, op := range ops {
		if reason := tiles.CheckOp(doc, op); reason != "" {
			change.Refused = append(change.Refused, Refusal{Index: i, Reason: reason})
			continue
		}
		accepted = append(accepted, op)
		change.Applied = append(change.Applied, tiles.OpSummary(doc, op))
	}
	if len(accepted) == 0 {
		state, err := s.Read(ctx, pr)
		change.State = state
		return change, err
	}
	batch := tiles.Batch{ID: newBatchID(), At: s.now().UTC().Format(time.RFC3339), By: by, Ops: accepted, Summary: change.Applied}
	state, err := s.write(ctx, pr, func(o *tiles.Overlay) (tiles.Overlay, error) { return o.With(batch) })
	if err != nil {
		return Change{}, err
	}
	change.State, change.Saved = state, true
	return change, nil
}

// Undo removes the newest batch, whoever made it. With expect, only when
// that batch is the one named (the page's Undo names the batch it shows).
func (s *Service) Undo(ctx context.Context, pr, expect string) (Change, error) {
	var undone tiles.Batch
	state, err := s.write(ctx, pr, func(o *tiles.Overlay) (tiles.Overlay, error) {
		if expect != "" && (o.Last() == nil || o.Last().ID != expect) {
			return tiles.Overlay{}, ErrStale
		}
		next, batch, ok := o.Undo()
		if !ok {
			return tiles.Overlay{}, ErrNothingToUndo
		}
		undone = batch
		return next, nil
	})
	if err != nil {
		return Change{}, err
	}
	summary := undone.Summary
	if undone.Reset {
		summary = []string{"Reset the layout"}
	}
	return Change{State: state, Undone: summary, Saved: true}, nil
}

// Reset returns the layout to the generated one, as a batch of its own, so
// it can be undone.
func (s *Service) Reset(ctx context.Context, pr, by string) (Change, error) {
	batch := tiles.Batch{ID: newBatchID(), At: s.now().UTC().Format(time.RFC3339), By: by, Reset: true, Summary: []string{"Reset the layout to the generated one"}}
	state, err := s.write(ctx, pr, func(o *tiles.Overlay) (tiles.Overlay, error) {
		if !o.Edited() {
			return tiles.Overlay{}, ErrNotEdited
		}
		return o.With(batch)
	})
	if err != nil {
		return Change{}, err
	}
	return Change{State: state, Applied: batch.Summary, Saved: true}, nil
}

// write changes a pull request's overlay in one write transaction, bumps its
// revision, keeps the Keep most recently edited pull requests, and tells
// Changed once it commits.
func (s *Service) write(ctx context.Context, pr string, change func(*tiles.Overlay) (tiles.Overlay, error)) (State, error) {
	if !ValidKey(pr) {
		return State{}, errors.New("invalid pull request key")
	}
	db, release, err := appdb.Acquire(s.Path, false)
	if err != nil {
		return State{}, fmt.Errorf("%w: %v", ErrUnsaved, err)
	}
	defer release()
	var state State
	refused := false
	err = appdb.Transact(ctx, db, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, schema); err != nil {
			return err
		}
		var revision int64
		var value []byte
		err := conn.QueryRowContext(ctx, `SELECT revision, value FROM tile_overlays WHERE pr = ?`, pr).Scan(&revision, &value)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		current, parseErr := tiles.ParseOverlay(value)
		if parseErr != nil {
			// None yet, or one this build cannot read: start again.
			current = nil
		}
		next, err := change(current)
		if err != nil {
			refused = true
			return err
		}
		encoded, err := encode(next)
		if err != nil {
			refused = true
			return err
		}
		now := s.now().UTC().Format(time.RFC3339Nano)
		if _, err := conn.ExecContext(ctx, `INSERT INTO tile_overlays(pr, revision, updated, value) VALUES (?, ?, ?, ?)
ON CONFLICT(pr) DO UPDATE SET revision = excluded.revision, updated = excluded.updated, value = excluded.value`, pr, revision+1, now, encoded); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM tile_overlays WHERE pr NOT IN (SELECT pr FROM tile_overlays ORDER BY updated DESC, pr LIMIT ?)`, Keep); err != nil {
			return err
		}
		state = State{PR: pr, Revision: revision + 1, Overlay: &next}
		return nil
	})
	if err != nil {
		if refused {
			return State{}, err
		}
		return State{}, fmt.Errorf("%w: %v", ErrUnsaved, err)
	}
	if s.Changed != nil {
		s.Changed(pr, state.Revision)
	}
	return state, nil
}

func encode(o tiles.Overlay) ([]byte, error) {
	if err := tiles.CheckOverlay(&o); err != nil {
		return nil, err
	}
	return json.Marshal(o)
}

// newBatchID is "edt_" and 12 random lowercase hex digits.
func newBatchID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "edt_" + hex.EncodeToString(b)
}
