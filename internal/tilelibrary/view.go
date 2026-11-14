package tilelibrary

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// View is a type in full, as tile_type reads it: its definition with the HTML, its texts, samples and
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

// Base is the revision of a type a proposal was made from: the digest and
// updated time of the version the proposal's texts started from.
type Base struct {
	Digest  string `json:"digest"`
	Updated string `json:"updated"`
}

// Card is a type as a proposal shows it, in one shape for the person's Before
// (the type now, CardOfType) and After (the change, CardOf), so the two diff
// field by field. The texts that instruct a fill, the review and the composer
// (title, use_when, glance, review_checks, paths and fill) are top-level
// fields beside the definition, which holds them again with the component's
// HTML; a Card whose texts differ from its definition's is refused
// (Change). BasedOn is the version of the type the change started from, empty
// for a type that is new.
type Card struct {
	Name         string           `json:"name"`
	Title        string           `json:"title"`
	UseWhen      string           `json:"use_when"`
	Glance       string           `json:"glance"`
	ReviewChecks string           `json:"review_checks"`
	Paths        []string         `json:"paths"`
	AppliesWith  []string         `json:"applies_with"`
	Fill         string           `json:"fill"`
	Origin       string           `json:"origin"`
	BasedOn      *Base            `json:"based_on,omitempty"`
	Definition   json.RawMessage  `json:"definition"`
	Samples      []library.Sample `json:"samples"`
}

// CardOf is the card of a change; based is the version it started from, nil
// for a new type. The change's definition must be a valid one.
func CardOf(c Change, based *Base) (Card, error) {
	def, problems := tiles.NewValidator().CheckDefinition(c.Definition)
	if len(problems) > 0 {
		return Card{}, refuse("definition", problems)
	}
	return Card{Name: def.Name, Title: def.Title, UseWhen: def.UseWhen, Glance: c.Glance, ReviewChecks: c.ReviewChecks,
		Paths: nonNil(c.Paths), AppliesWith: nonNil(c.AppliesWith), Fill: def.Fill, Origin: c.Origin, BasedOn: based,
		Definition: c.Definition, Samples: c.Samples}, nil
}

// CardOfType is the card of a saved type as it is now, in the shape of a
// proposal's After, based on itself.
func CardOfType(t Type) Card {
	c, err := CardOf(ChangeOf(t.Entry, t.Paths, t.Origin), &Base{Digest: t.Entry.Definition.Digest, Updated: t.Updated})
	if err != nil {
		// A stored type passed the same checks when it was read.
		return Card{Name: t.Name}
	}
	return c
}

// Change is the change a card holds. The texts a person was shown (name,
// title, use_when and fill) must be those of the definition that is saved.
func (c Card) Change() (Change, error) {
	def, problems := tiles.NewValidator().CheckDefinition(c.Definition)
	if len(problems) > 0 {
		return Change{}, refuse("definition", problems)
	}
	if def.Name != c.Name || def.Title != c.Title || def.UseWhen != c.UseWhen || def.Fill != c.Fill {
		return Change{}, errors.New("the proposal's texts are not those of its definition")
	}
	return Change{Definition: c.Definition, Glance: c.Glance, ReviewChecks: c.ReviewChecks, AppliesWith: c.AppliesWith,
		Paths: c.Paths, Samples: c.Samples, Origin: c.Origin}, nil
}
