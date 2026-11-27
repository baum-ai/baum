package generate

import (
	"context"
	"slices"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/tiles/library"
)

// Tiles supplies a baum.review/2 run's dedicated tile types (Options.V2):
// the library types it offers, the person's saved types and the types the
// person added to the pull request's review.
type Tiles struct {
	// Library are the library types offered; nil offers every built-in one
	// (library.All). NoLibrary offers none.
	Library   []library.Entry
	NoLibrary bool
	// Always names the library types the person added to the pull request's
	// review: each is filled whatever the map answers. A saved type pinned
	// to the repository may be named here; it is filled only when Saved
	// returns it.
	Always []string
	// Saved, when set, chooses the person's saved tile types for the run
	// once the pull request is assembled (tilelibrary.Offer): always are
	// filled whatever the map answers, as Always are, and offered join the
	// library types offered.
	Saved SavedTypes
}

// SavedTypes chooses the saved tile types for a run: diffPaths are the
// paths its diff changes, taken the library types it offers already (the
// built-in ones and those the person added).
type SavedTypes func(ctx context.Context, diffPaths, taken []string) (always, offered []library.Entry)

// withSaved is t with the saved types Saved chooses for the pull request:
// they join the library offered, after the built-in types, and the always
// ones join Always. Without Saved, or with no library, it is t.
func (t *Tiles) withSaved(ctx context.Context, b *bundle.Bundle) *Tiles {
	if t == nil || t.Saved == nil || t.NoLibrary {
		return t
	}
	out := *t
	base := t.Library
	if base == nil {
		base = library.All()
	}
	var taken []string
	for _, e := range base {
		taken = append(taken, e.Definition.Name)
	}
	for _, name := range t.Always {
		if _, ok := library.Get(name); ok && !slices.Contains(taken, name) {
			taken = append(taken, name)
		}
	}
	paths := map[string]bool{}
	var diffPaths []string
	for _, h := range b.Diff {
		for _, p := range []string{h.Path, h.OldPath} {
			if p != "" && !paths[p] {
				paths[p] = true
				diffPaths = append(diffPaths, p)
			}
		}
	}
	always, offered := t.Saved(ctx, diffPaths, taken)
	out.Library = slices.Clone(base)
	out.Always = slices.Clone(t.Always)
	add := func(e library.Entry) bool {
		if slices.ContainsFunc(out.Library, func(have library.Entry) bool { return have.Definition.Name == e.Definition.Name }) {
			return false
		}
		out.Library = append(out.Library, e)
		return true
	}
	for _, e := range always {
		if add(e) && !slices.Contains(out.Always, e.Definition.Name) {
			out.Always = append(out.Always, e.Definition.Name)
		}
	}
	for _, e := range offered {
		add(e)
	}
	return &out
}
