package uiserver

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/tilelayout"
	"github.com/baum-ai/baum/tiles"
)

// LayoutEditor is the session panel's way to the person's own tile layouts
// (agentcontext.LayoutEditor): the same service the page's buttons use, with
// edits made by "chat". A chat's edit is checked against the tiles of the
// pull request's latest saved version as the person is shown them.
type LayoutEditor struct {
	API LocalAPI
}

var _ agentcontext.LayoutEditor = LayoutEditor{}

func (e LayoutEditor) service() (*tilelayout.Service, error) {
	if e.API.Layouts == nil {
		return nil, errors.New("tile layouts are not available in this baum")
	}
	return e.API.Layouts, nil
}

// Shown implements agentcontext.LayoutEditor.
func (e LayoutEditor) Shown(ctx context.Context, repository string, number int) (agentcontext.ShownLayout, error) {
	if _, err := e.service(); err != nil {
		return agentcontext.ShownLayout{}, err
	}
	c, err := e.API.pullClone(ctx, repository, number)
	if err != nil {
		return agentcontext.ShownLayout{}, err
	}
	shown, err := e.API.shownTiles(ctx, c, number, 0)
	if err != nil {
		return agentcontext.ShownLayout{}, err
	}
	return agentcontext.ShownLayout{Version: shown.Version, Document: shown.Document, Revision: shown.State.Revision, Edited: shown.State.Edited(), CanUndo: shown.State.CanUndo()}, nil
}

// Edit implements agentcontext.LayoutEditor.
func (e LayoutEditor) Edit(ctx context.Context, repository string, number int, ops []tiles.Op) (agentcontext.LayoutChange, error) {
	if _, err := e.service(); err != nil {
		return agentcontext.LayoutChange{}, err
	}
	c, err := e.API.pullClone(ctx, repository, number)
	if err != nil {
		return agentcontext.LayoutChange{}, err
	}
	change, err := e.API.editLayout(ctx, c, number, "chat", ops)
	return changeOf(change), err
}

// Add implements agentcontext.LayoutEditor.
func (e LayoutEditor) Add(ctx context.Context, repository string, number int, add agentcontext.TileAdd) (agentcontext.TileAdded, error) {
	if _, err := e.service(); err != nil {
		return agentcontext.TileAdded{}, err
	}
	c, err := e.API.pullClone(ctx, repository, number)
	if err != nil {
		return agentcontext.TileAdded{}, err
	}
	added, err := e.API.addTile(ctx, c, number, "chat", AddTile{Type: add.Type, Size: add.Size, To: add.To, Data: add.Data})
	if errors.Is(err, ErrUnknownType) {
		return agentcontext.TileAdded{}, errors.New("No tile type " + add.Type + " in baum's tile library; tile_layout lists the types it can add")
	}
	if err != nil {
		return agentcontext.TileAdded{}, err
	}
	return agentcontext.TileAdded{Tile: added.Tile, State: added.State, Change: changeOf(added.Change)}, nil
}

// Undo implements agentcontext.LayoutEditor.
func (e LayoutEditor) Undo(ctx context.Context, repository string, number int) (agentcontext.LayoutChange, error) {
	service, err := e.service()
	if err != nil {
		return agentcontext.LayoutChange{}, err
	}
	change, err := service.Undo(ctx, tilelayout.Key(repository, number), "")
	return changeOf(change), err
}

// Reset implements agentcontext.LayoutEditor.
func (e LayoutEditor) Reset(ctx context.Context, repository string, number int) (agentcontext.LayoutChange, error) {
	service, err := e.service()
	if err != nil {
		return agentcontext.LayoutChange{}, err
	}
	change, err := service.Reset(ctx, tilelayout.Key(repository, number), "chat")
	return changeOf(change), err
}

func changeOf(change tilelayout.Change) agentcontext.LayoutChange {
	out := agentcontext.LayoutChange{Applied: change.Applied, Undone: change.Undone, Revision: change.State.Revision,
		Edited: change.State.Edited(), CanUndo: change.State.CanUndo(), Saved: change.Saved}
	for _, refusal := range change.Refused {
		out.Refused = append(out.Refused, agentcontext.LayoutRefusal{Index: refusal.Index, Reason: refusal.Reason})
	}
	return out
}

// pullClone is the registered, available clone of a repository whose review
// store holds a saved version of the pull request.
func (a LocalAPI) pullClone(ctx context.Context, repository string, number int) (repos.Clone, error) {
	clones, err := a.Registry.List(ctx)
	if err != nil {
		return repos.Clone{}, err
	}
	found := false
	for _, c := range clones {
		if !strings.EqualFold(c.Repository, repository) {
			continue
		}
		found = true
		s, err := store.Open(c.CommonDir, "", false)
		if err != nil {
			continue
		}
		if _, err := s.Latest(ctx, store.Key{Repository: c.Repository, Number: number}); err == nil {
			return c, nil
		}
	}
	if !found {
		return repos.Clone{}, errors.New("The repository " + repository + " has no registered clone")
	}
	return repos.Clone{}, errors.New("Pull request #" + strconv.Itoa(number) + " has no saved review yet")
}
