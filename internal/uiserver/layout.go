package uiserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/tilelayout"
	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/internal/viewserver"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// A person's own tile layout of a pull request (internal/tilelayout) is
// applied to every version the tile route serves, and changed through
// GET and POST /api/tiles/layout here and the session panel's tools
// (LayoutEditor), both through LocalAPI.Layouts.

// layoutAnswer is what the layout routes answer: the layout's summary and,
// for a write, what it did.
type layoutAnswer struct {
	tilelayout.Summary
	Applied []string             `json:"applied,omitempty"`
	Refused []tilelayout.Refusal `json:"refused,omitempty"`
	Undone  []string             `json:"undone,omitempty"`
	Redone  []string             `json:"redone,omitempty"`
}

func answerOf(change tilelayout.Change) layoutAnswer {
	return layoutAnswer{Summary: change.State.Summary(), Applied: change.Applied, Refused: change.Refused, Undone: change.Undone, Redone: change.Redone}
}

// layoutRoutes mounts GET /api/tiles/layout?clone=&pr= (the layout's
// summary) and POST /api/tiles/layout ({clone, pr, action: edit, undo, redo
// or reset, ops?, batch?}: a page edit, an undo of the newest batch, which
// must be batch when it is named, a redo of the batch the last undo took off
// (batch names it, as the summary's redo_batch), or a reset). A write
// answers the new summary, or 400 with the host's reason when nothing was
// written (every op refused, nothing to undo or redo, already the generated
// layout) and 409 when the batch an undo or redo names is no longer the
// one it would act on.
func (a LocalAPI) layoutRoutes(mux *http.ServeMux) {
	unavailable := func(w http.ResponseWriter) bool {
		if a.Layouts == nil {
			http.Error(w, "capability unavailable", http.StatusServiceUnavailable)
			return true
		}
		return false
	}
	mux.HandleFunc("GET /api/tiles/layout", func(w http.ResponseWriter, r *http.Request) {
		if unavailable(w) {
			return
		}
		c, key, _, ok := a.selector(w, r)
		if !ok {
			return
		}
		state, err := a.Layouts.Read(r.Context(), tilelayout.Key(c.Repository, key.Number))
		if err != nil {
			apiError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, layoutAnswer{Summary: state.Summary()})
	})
	mux.HandleFunc("POST /api/tiles/layout", func(w http.ResponseWriter, r *http.Request) {
		if unavailable(w) {
			return
		}
		var request struct {
			Clone  string     `json:"clone"`
			PR     int        `json:"pr"`
			Action string     `json:"action"`
			Ops    []tiles.Op `json:"ops,omitempty"`
			Batch  string     `json:"batch,omitempty"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid JSON request", http.StatusBadRequest)
			return
		}
		c, ok := a.cloneByID(w, r, request.Clone)
		if !ok {
			return
		}
		if request.PR < 1 {
			http.Error(w, "positive pr required", http.StatusBadRequest)
			return
		}
		var change tilelayout.Change
		var err error
		switch request.Action {
		case "edit":
			change, err = a.editLayout(r.Context(), c, request.PR, "page", request.Ops)
		case "undo":
			change, err = a.Layouts.Undo(r.Context(), tilelayout.Key(c.Repository, request.PR), request.Batch)
		case "redo":
			change, err = a.Layouts.Redo(r.Context(), tilelayout.Key(c.Repository, request.PR), request.Batch)
		case "reset":
			change, err = a.Layouts.Reset(r.Context(), tilelayout.Key(c.Repository, request.PR), "page")
		default:
			http.Error(w, "action is edit, undo, redo or reset", http.StatusBadRequest)
			return
		}
		switch {
		case errors.Is(err, tilelayout.ErrStale):
			http.Error(w, err.Error(), http.StatusConflict)
			return
		case errors.Is(err, store.ErrNotFound):
			apiError(w, err)
			return
		case errors.Is(err, tilelayout.ErrUnsaved):
			http.Error(w, tilelayout.ErrUnsaved.Error(), http.StatusInternalServerError)
			return
		case err != nil:
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		case !change.Saved:
			reasons := make([]string, len(change.Refused))
			for i, refusal := range change.Refused {
				reasons[i] = refusal.Reason
			}
			http.Error(w, strings.Join(reasons, "; "), http.StatusBadRequest)
			return
		}
		writeJSON(w, answerOf(change))
	})
}

// cloneByID resolves a registered clone, answering 400 or 404.
func (a LocalAPI) cloneByID(w http.ResponseWriter, r *http.Request, id string) (repos.Clone, bool) {
	if id == "" {
		http.Error(w, "clone required", http.StatusBadRequest)
		return repos.Clone{}, false
	}
	clones, err := a.Registry.List(r.Context())
	if err != nil {
		apiError(w, err)
		return repos.Clone{}, false
	}
	for _, c := range clones {
		if c.ID == id {
			return c, true
		}
	}
	http.NotFound(w, r)
	return repos.Clone{}, false
}

// editLayout checks ops against the tiles the pull request's latest saved
// version is shown with, the person's layout applied, and saves those that
// pass as one batch.
func (a LocalAPI) editLayout(ctx context.Context, c repos.Clone, number int, by string, ops []tiles.Op) (tilelayout.Change, error) {
	// An add names a library type by its name; the host writes the digest
	// of the definition added, and an unknown type is refused.
	ops = slices.Clone(ops)
	for i, op := range ops {
		if op.Op != tiles.OpAdd || op.Add == nil {
			continue
		}
		entry, ok := library.Get(op.Add.Name)
		if !ok {
			return tilelayout.Change{}, fmt.Errorf("No tile type %s in baum's tile library", op.Add.Name)
		}
		added := *op.Add
		added.Kind, added.Digest, added.Title = "library", entry.Definition.Digest, entry.Definition.Title
		ops[i].Add = &added
	}
	// The ops are checked inside the write against the layout the tiles were
	// shown with; when it moved meanwhile (another write), read again.
	for attempt := 0; ; attempt++ {
		shown, err := a.shownTiles(ctx, c, number, 0)
		if err != nil {
			return tilelayout.Change{}, err
		}
		change, err := a.Layouts.Edit(ctx, tilelayout.Key(c.Repository, number), by, shown.Document, shown.State.Revision, ops)
		if errors.Is(err, tilelayout.ErrStale) && attempt < 2 {
			continue
		}
		return change, err
	}
}

// shownLayout is a version's tiles as a person is shown them.
type shownLayout struct {
	Document *tiles.Document
	Version  int
	State    tilelayout.State
}

// errNoTiles is a saved version that cannot be shown as tiles.
var errNoTiles = errors.New("This review cannot be shown as tiles")

// shownTiles builds a saved version (the latest for 0) of a pull request of
// the clone's repository with the person's layout applied.
func (a LocalAPI) shownTiles(ctx context.Context, c repos.Clone, number, n int) (shownLayout, error) {
	s, err := store.Open(c.CommonDir, "", false)
	if err != nil {
		return shownLayout{}, err
	}
	key := store.Key{Repository: c.Repository, Number: number}
	var version store.Version
	if n == 0 {
		version, err = s.Latest(ctx, key)
	} else {
		version, err = s.Get(ctx, key, n)
	}
	if err != nil {
		return shownLayout{}, err
	}
	if len(version.Document) > viewserver.MaxDocumentBytes {
		return shownLayout{}, errors.New("The saved review is too large to show")
	}
	state, err := a.Layouts.Read(ctx, tilelayout.Key(c.Repository, number))
	if err != nil {
		return shownLayout{}, err
	}
	var fills []tilelayout.Fill
	if len(state.AddedTypes()) > 0 {
		if fills, err = a.Layouts.Fills(ctx, tilelayout.Key(c.Repository, number)); err != nil {
			return shownLayout{}, err
		}
	}
	latest := n == 0
	if !latest {
		if current, err := s.Latest(ctx, key); err == nil {
			latest = current.Metadata.Version == version.Metadata.Version
		}
	}
	layout := a.layoutFor(ctx, c, number, version, latest, state, fills)
	built := tileview.BuildWith(version.Document, version.Composition, layout, tiles.Options{})
	if built.Tiles == nil {
		return shownLayout{}, errNoTiles
	}
	return shownLayout{Document: built.Tiles, Version: version.Metadata.Version, State: state}, nil
}
