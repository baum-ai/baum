package tileview

import (
	"github.com/baum-ai/baum/tiles/library"
)

// RegisterLibrary registers the frame documents of the built-in library
// types, so their tiles' frames load before any tile route served one. The
// tile routes register them again as they serve them.
func RegisterLibrary() error {
	for _, e := range library.All() {
		if _, err := frames.Add(e.Definition); err != nil {
			return err
		}
	}
	return nil
}
