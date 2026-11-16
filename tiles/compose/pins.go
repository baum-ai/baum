package compose

import (
	"fmt"
	"strings"

	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// PinnableTile reports whether id names a tile a repository can pin: a
// single built-in tile type (overview, risks and so on), or a library tile
// type by name, title or tile ID.
func PinnableTile(id string) bool {
	if b, ok := tiles.Builtin(id); ok && b.Single {
		return true
	}
	_, ok := library.Find(id)
	return ok
}

// PinnableTiles lists the names PinnableTile accepts, for messages.
func PinnableTiles() []string {
	var out []string
	for _, b := range tiles.Builtins {
		if b.Single {
			out = append(out, b.Name)
		}
	}
	for _, e := range library.All() {
		out = append(out, e.Definition.Name)
	}
	return out
}

// CheckPin refuses a pin that is not a tile a repository can pin, naming the
// ones it can.
func CheckPin(id string) error {
	if PinnableTile(id) {
		return nil
	}
	return fmt.Errorf("%q is not a tile type that can be pinned; use one of %s", id, strings.Join(PinnableTiles(), ", "))
}
