package tilelibrary

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing/fstest"
	"unicode"
	"unicode/utf8"

	"github.com/baum-ai/baum/reviewv2"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
	"github.com/baum-ai/baum/tiles/view"
)

// The limits of the library.
const (
	// MaxTypes is how many types the library holds.
	MaxTypes = 200
	// MaxVersions is how many versions of one type are kept.
	MaxVersions = 20
	// MaxBytes bounds the definitions and samples the library holds.
	MaxBytes = 64 << 20
	// MaxPaths bounds a type's path globs, MaxPathLength each of them.
	MaxPaths      = 16
	MaxPathLength = 200
	// MaxSamples bounds a type's samples, MaxSampleBytes each, encoded.
	MaxSamples     = 5
	MaxSampleBytes = 64 << 10
)

// Origins of a type.
const (
	OriginChat   = "chat"
	OriginImport = "import"
)

// namePattern is a type's name: it is also the ID of its tiles, which the
// layout edits key on.
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// reservedPrefixes are the prefixes tiles.LibraryTileID would rename an ID
// for: a type may not start with one, so its tile's ID is always its name.
var reservedPrefixes = []string{"sec_", "table_", "code_", "prose_", "diagram_", "library_"}

// Errors a save answers with. Each names no content of what was saved.
var (
	ErrUnknown  = errors.New("There is no tile type of that name in your library")
	ErrExists   = errors.New("A tile type of that name is already in your library")
	ErrStale    = errors.New("The tile type changed meanwhile; read it again")
	ErrTooMany  = fmt.Errorf("The library holds %d tile types; delete one first", MaxTypes)
	ErrTooLarge = fmt.Errorf("The library is full (%d MiB); delete a tile type first", MaxBytes>>20)
	ErrDeleted  = errors.New("That tile type was deleted; undo the deletion first")
	// ErrNoVersion is a restore of a version the type does not have.
	ErrNoVersion = errors.New("The type has no such version")
)

// RefusedError is a save refused because the change did not pass its
// checks (Prepare); its message names places, never content.
type RefusedError struct{ Err error }

func (e *RefusedError) Error() string { return e.Err.Error() }
func (e *RefusedError) Unwrap() error { return e.Err }

// ValidName reports whether name has a type's name format: a lowercase
// letter, then lowercase letters, digits and underscores, 64 at most.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// ReservedNames are the names a library type may not have, apart from the
// prefixes: custom, the older composition types (tiles.Builtins), the types
// the review page draws itself and add_tile takes (flow, columns, sequence,
// table, code, images) and baum's own library types. The tile guide lists
// them (a test keeps it so).
func ReservedNames() []string {
	var names []string
	add := func(n string) {
		if !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	add(tiles.TypeCustom)
	for _, b := range tiles.Builtins {
		add(b.Name)
	}
	for _, n := range view.NativeTypes {
		add(n)
	}
	add(tiles.TypeCode)
	add(reviewv2.TypeImages)
	for _, n := range library.Names {
		add(n)
	}
	return names
}

// CheckName refuses a name a library type may not have: not the format, one
// of ReservedNames, nor a prefix that changes a tile's ID. Whether it is
// taken is the store's to say.
func CheckName(name string) error {
	switch {
	case !ValidName(name):
		return errors.New("A tile type's name is 1 to 64 characters: a lowercase letter, then lowercase letters, digits and underscores")
	case name == tiles.TypeCustom:
		return errors.New("A tile type cannot be named custom")
	case slices.Contains(library.Names, name):
		return fmt.Errorf("%s is one of baum's own library types; choose another name", name)
	case slices.Contains(ReservedNames(), name):
		return fmt.Errorf("%s is a built-in tile type of the review page; choose another name", name)
	}
	for _, prefix := range reservedPrefixes {
		if strings.HasPrefix(name, prefix) {
			return fmt.Errorf("A tile type's name cannot start with %s", prefix)
		}
	}
	return nil
}

// Change is what a save takes: a type's definition, sealed, with its texts
// beside it, the samples that draw it, and where it came from.
type Change struct {
	// Definition is the definition with its HTML (the digest is recomputed).
	Definition   json.RawMessage `json:"definition"`
	Glance       string          `json:"glance"`
	ReviewChecks string          `json:"review_checks"`
	AppliesWith  []string        `json:"applies_with"`
	Paths        []string        `json:"paths"`
	// Samples are synthetic: each cites only its own hunks and items.
	Samples []library.Sample `json:"samples"`
	Origin  string           `json:"origin"`
}

// IsSynthetic reports whether a sample is its own: it holds hunks or items
// and every citation of its data resolves to them, so it cites nothing of a
// real pull request.
func IsSynthetic(s library.Sample) bool {
	if len(s.Hunks)+len(s.Items) == 0 {
		return false
	}
	_, unresolved := s.Refs("detail")
	return len(unresolved) == 0
}

// Prepared is a change that passed every check, in the form it is stored.
type Prepared struct {
	// Change is the change as stored: invisible characters removed from its
	// texts, its definition sealed again.
	Change Change
	Entry  library.Entry
	Paths  []string
	Origin string
}

// Name is the type's name.
func (p Prepared) Name() string { return p.Entry.Definition.Name }

// Prepare checks a change and returns it as stored: the entry is built as
// the library loads one (BuildEntry), so every static check the built-in
// types pass applies (library.Check), and the name, paths, samples and
// origin are checked besides. A problem names the place, never the content.
func Prepare(c Change) (Prepared, error) {
	c, err := sanitize(c)
	if err != nil {
		return Prepared{}, errors.New("The definition is not a JSON object")
	}
	sealed, err := sealRaw(c.Definition)
	if err != nil {
		return Prepared{}, errors.New("The definition is not a JSON object")
	}
	c.Definition = sealed
	def, problems := tiles.NewValidator().CheckDefinition(c.Definition)
	if len(problems) > 0 {
		return Prepared{}, refuse("definition", problems)
	}
	if err := CheckName(def.Name); err != nil {
		return Prepared{}, err
	}
	switch c.Origin {
	case OriginChat, OriginImport:
	default:
		return Prepared{}, errors.New("A tile type's origin is chat or import")
	}
	if len(c.Paths) > MaxPaths {
		return Prepared{}, fmt.Errorf("A tile type has at most %d paths", MaxPaths)
	}
	for _, p := range c.Paths {
		if p == "" || utf8.RuneCountInString(p) > MaxPathLength || !utf8.ValidString(p) || strings.ContainsFunc(p, func(r rune) bool { return unicode.IsControl(r) || Invisible(r) }) {
			return Prepared{}, fmt.Errorf("A path is a glob of 1 to %d characters on one line, with no control or invisible characters", MaxPathLength)
		}
		if err := CheckGlob(p); err != nil {
			return Prepared{}, err
		}
	}
	if n := len(c.Samples); n < 1 || n > MaxSamples {
		return Prepared{}, fmt.Errorf("A tile type has 1 to %d samples", MaxSamples)
	}
	for i, s := range c.Samples {
		raw, err := json.Marshal(s)
		if err != nil || len(raw) > MaxSampleBytes {
			return Prepared{}, fmt.Errorf("Sample %d is larger than %d KiB", i+1, MaxSampleBytes>>10)
		}
		if !IsSynthetic(s) {
			return Prepared{}, fmt.Errorf("Sample %d is not synthetic: a sample has hunks or items of its own, in a made-up repository, that all its citations name", i+1)
		}
	}
	entry, err := BuildEntry(def, c)
	if err != nil {
		return Prepared{}, err
	}
	if problems := library.Check(entry); len(problems) > 0 {
		return Prepared{}, refuse("tile type", problems)
	}
	return Prepared{Change: c, Entry: entry, Paths: slices.Clone(c.Paths), Origin: c.Origin}, nil
}

func refuse(what string, problems []string) error {
	const shown = 6
	more := ""
	if len(problems) > shown {
		more = fmt.Sprintf(" (and %d more)", len(problems)-shown)
		problems = problems[:shown]
	}
	return fmt.Errorf("The %s did not pass its checks: %s%s", what, strings.Join(problems, "; "), more)
}

// BuildEntry builds the library.Entry of a definition and its texts the way
// the built-in types are read: it writes them as the library's directory
// layout (type.json, fill.md, review_checks.md, schema.json, tile.html and
// samples/) into memory and loads that with library.Load. The entry's
// definition is therefore exactly what Load makes of the files, digest
// included.
func BuildEntry(def tiles.TypeDefinition, c Change) (library.Entry, error) {
	meta := struct {
		Name        string       `json:"name"`
		Title       string       `json:"title"`
		UseWhen     string       `json:"use_when"`
		Glance      string       `json:"glance"`
		Sizes       []tiles.Size `json:"sizes"`
		Kit         int          `json:"kit"`
		AppliesWith []string     `json:"applies_with,omitempty"`
	}{def.Name, def.Title, def.UseWhen, c.Glance, def.Sizes, def.Kit, c.AppliesWith}
	files := fstest.MapFS{
		"fill.md":          {Data: []byte(def.Fill)},
		"review_checks.md": {Data: []byte(c.ReviewChecks)},
		"schema.json":      {Data: def.DataSchema},
		"tile.html":        {Data: []byte(def.Source.HTML)},
		"type.json":        {Data: mustEncode(meta)},
	}
	for i, s := range c.Samples {
		files[fmt.Sprintf("samples/%02d.json", i)] = &fstest.MapFile{Data: mustEncode(s)}
	}
	entry, err := library.Load(files)
	if err != nil {
		return library.Entry{}, fmt.Errorf("The tile type could not be read: %v", err)
	}
	return entry, nil
}

func mustEncode(v any) []byte {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// ChangeOf is the change that stores entry again, for an edit that keeps
// the rest of the type as it is.
func ChangeOf(entry library.Entry, paths []string, origin string) Change {
	return Change{Definition: entry.Encoded, Glance: entry.Glance, ReviewChecks: entry.ReviewChecks,
		AppliesWith: entry.AppliesWith, Paths: paths, Samples: entry.Samples, Origin: origin}
}

// LibraryTile is the generated library tile of an entry with the data a
// fill wrote: it embeds the full definition, so a review saved with it
// keeps the definition it was made with, whatever happens to the type in
// the library afterwards.
func LibraryTile(entry library.Entry, data json.RawMessage) tiles.LibraryTile {
	return tiles.LibraryTile{Definition: entry.Encoded, Data: data}
}

// sealRaw returns a definition with its digest computed from its content,
// whatever digest it came with: a digest is never taken from a file or a
// request.
func sealRaw(raw json.RawMessage) (json.RawMessage, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil || members == nil {
		return nil, errors.New("not an object")
	}
	delete(members, "digest")
	without, err := json.Marshal(members)
	if err != nil {
		return nil, err
	}
	digest, err := tiles.DefinitionDigest(without)
	if err != nil {
		return nil, err
	}
	members["digest"], _ = json.Marshal(digest)
	return json.Marshal(members)
}
