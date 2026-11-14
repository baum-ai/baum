package tileview

import (
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// LibraryType is a built-in library tile type as GET /api/tile-library
// lists it: what the page needs to name and recognise its tiles, never the
// type's source, which the frame route serves by digest.
type LibraryType struct {
	Name    string       `json:"name"`
	Title   string       `json:"title"`
	UseWhen string       `json:"use_when"`
	Glance  string       `json:"glance"`
	Sizes   []tiles.Size `json:"sizes"`
	Kit     int          `json:"kit"`
	Digest  string       `json:"digest"`
}

// SingleType is a built-in tile type with at most one tile in a review, as
// GET /api/tile-library lists it beside the library types: a repository can
// pin it.
type SingleType struct {
	Name  string `json:"name"`
	Title string `json:"title"`
}

// Singles lists the single built-in tile types, in catalogue order.
func Singles() []SingleType {
	var out []SingleType
	for _, b := range tiles.Builtins {
		if b.Single {
			out = append(out, SingleType{Name: b.Name, Title: b.Title})
		}
	}
	return out
}

// Library lists the built-in library types, in offer order.
func Library() []LibraryType {
	entries := library.All()
	out := make([]LibraryType, 0, len(entries))
	for _, e := range entries {
		d := e.Definition
		out = append(out, LibraryType{Name: d.Name, Title: d.Title, UseWhen: d.UseWhen, Glance: e.Glance, Sizes: d.Sizes, Kit: d.Kit, Digest: d.Digest})
	}
	return out
}

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
