package jobs

import (
	"context"
	"slices"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/tilelibrary"
	"github.com/baum-ai/baum/tiles/library"
)

// offerSaved adds the person's saved tile types to a new review's tiles:
// the types pinned to the repository are filled whatever the outline
// answers, each with the last data held for it (as an added type is), and
// up to tilelibrary.MaxOffered other saved types whose paths the diff
// matches are offered to the outline (tilelibrary.Offer, once the pull
// request is assembled). Without a library it adds nothing.
func (l *Local) offerSaved(ctx context.Context, t *generate.Tiles, versions *store.Store, key store.Key, repository string) {
	if t == nil || l.TileLibrary == nil {
		return
	}
	pinned := l.pinnedTypes(ctx, repository)
	var names []string
	for _, name := range pinned {
		if !slices.ContainsFunc(t.Always, func(a agent.AlwaysTile) bool { return a.Name == name }) {
			names = append(names, name)
		}
	}
	t.Always = append(t.Always, l.lastTiles(ctx, versions, key, names)...)
	lib := l.TileLibrary
	t.Saved = func(ctx context.Context, diffPaths, taken []string) ([]library.Entry, []library.Entry) {
		always, offered, err := lib.Offer(ctx, tilelibrary.Offering{Repository: repository, DiffPaths: diffPaths, Pinned: pinned, Taken: taken})
		if err != nil && l.Log != nil {
			l.Log.Warn("the tile library could not be read; the review offers the built-in tile types only", "error", err)
		}
		return always, offered
	}
}

// pinnedTypes are the saved tile types pinned to the repository; none when
// its settings cannot be read now.
func (l *Local) pinnedTypes(ctx context.Context, repository string) []string {
	if l.Registry == nil {
		return nil
	}
	settings, _, err := l.Registry.RepositorySettings(ctx, repository)
	if err != nil || settings.Tiles == nil {
		return nil
	}
	return settings.Tiles.Types
}

// recordTileUse records the saved tile types a stored version of the
// repository holds a ready fill of (tilelibrary.Store.RecordUse), which
// the library page shows and the next reviews rank by. A failure is only
// logged: the version is stored.
func (l *Local) recordTileUse(ctx context.Context, repository string, result generate.Result) {
	if l.TileLibrary == nil {
		return
	}
	names := tilelibrary.UsedTypes(result.Composition)
	if len(names) == 0 {
		return
	}
	if err := l.TileLibrary.RecordUse(context.WithoutCancel(ctx), repository, names); err != nil && l.Log != nil {
		l.Log.Warn("recording the use of saved tile types", "repository", repository, "error", err)
	}
}
