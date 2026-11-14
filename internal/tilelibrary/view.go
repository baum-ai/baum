package tilelibrary

import (
	"context"
	"encoding/json"

	"github.com/baum-ai/baum/tiles/library"
)

// View is a type in full, as tile_type reads it and a proposal shows what
// the person has now: its definition with the HTML, its texts, samples and
// versions. It is the one place a type's source leaves the daemon for an
// agent.
type View struct {
	Name         string           `json:"name"`
	Builtin      bool             `json:"builtin"`
	Definition   json.RawMessage  `json:"definition"`
	Glance       string           `json:"glance"`
	ReviewChecks string           `json:"review_checks"`
	AppliesWith  []string         `json:"applies_with"`
	Paths        []string         `json:"paths"`
	Samples      []library.Sample `json:"samples"`
	Versions     []Version        `json:"versions"`
	Origin       string           `json:"origin,omitempty"`
	Created      string           `json:"created,omitempty"`
	Updated      string           `json:"updated,omitempty"`
}

// ViewOf is the view of a saved type.
func ViewOf(t Type) View {
	return View{Name: t.Name, Definition: t.Entry.Encoded, Glance: t.Entry.Glance, ReviewChecks: t.Entry.ReviewChecks,
		AppliesWith: nonNil(t.Entry.AppliesWith), Paths: nonNil(t.Paths), Samples: t.Entry.Samples, Versions: t.Versions,
		Origin: t.Origin, Created: t.Created, Updated: t.Updated}
}

// BuiltinViewOf is the view of one of baum's own types, which has one
// version: this release's.
func BuiltinViewOf(e library.Entry) View {
	return View{Name: e.Definition.Name, Builtin: true, Definition: e.Encoded, Glance: e.Glance, ReviewChecks: e.ReviewChecks,
		AppliesWith: nonNil(e.AppliesWith), Paths: []string{}, Samples: e.Samples,
		Versions: []Version{{Digest: e.Definition.Digest, Current: true}}}
}

// Lookup returns the view of a built-in or saved type; built-in names come
// first and are never shadowed.
func (s *Store) Lookup(ctx context.Context, name string) (View, error) {
	if e, ok := library.Get(name); ok {
		return BuiltinViewOf(e), nil
	}
	t, err := s.Get(ctx, name)
	if err != nil {
		return View{}, err
	}
	return ViewOf(t), nil
}
