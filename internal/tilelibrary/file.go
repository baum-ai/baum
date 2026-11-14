package tilelibrary

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	baumschema "github.com/baum-ai/baum/schema"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/kit"
	"github.com/baum-ai/baum/tiles/library"
)

// A tile type file (<name>.baumtile.json, schema/tile-type-1.json) is one
// saved type as the Tile library exports and imports it: the definition
// without its digest, its texts, paths and synthetic samples. It maps one to
// one onto the built-in library's directory layout, so a file unpacked is a
// type directory `just local_app::tile-type-screens` can draw.
//
// An imported file is untrusted. ReadFile checks it before anything else
// reads it: its size, then JSON with depth and value limits, the schema id,
// the definition's own checks (shape, UTF-8, no NUL, dialect, a supported
// kit), the kit's lint (an error refuses the file), the samples against the
// data schema with every citation resolving to the sample's own hunks and
// items, and the name rules (Prepare, as every save). The digest is computed
// from the content, never read from the file; whether a type is built in is
// computed from that digest (library.IsBuiltin), never read; and the origin
// is "import".

// FileExtension ends a tile type file's name.
const FileExtension = ".baumtile.json"

// The bounds of a tile type file.
const (
	// MaxFileBytes is the largest file an import reads.
	MaxFileBytes = 3 << 20
	// maxFileDepth and maxFileValues bound the JSON's nesting and how many
	// values it holds, checked before it is decoded.
	maxFileDepth  = 64
	maxFileValues = 200_000
	// maxRenames bounds the numeric suffixes tried for a clashing name.
	maxRenames = 99
)

// File is a tile type file.
type File struct {
	Schema       string           `json:"schema"`
	Definition   json.RawMessage  `json:"definition"`
	Glance       string           `json:"glance"`
	ReviewChecks string           `json:"review_checks"`
	AppliesWith  []string         `json:"applies_with"`
	Paths        []string         `json:"paths"`
	Samples      []library.Sample `json:"samples"`
	Exported     *Exported        `json:"exported,omitempty"`
}

// Exported says when and by which baum a file was written; an import reads
// nothing from it.
type Exported struct {
	At   string `json:"at"`
	Baum string `json:"baum"`
}

// FileName is the name a type's file is saved under.
func FileName(name string) string { return name + FileExtension }

// Export writes a saved type as a file: its current version, the definition
// without its digest. version is this baum's version.
func Export(t Type, version string, now time.Time) ([]byte, error) {
	var def map[string]json.RawMessage
	if err := json.Unmarshal(t.Entry.Encoded, &def); err != nil {
		return nil, err
	}
	delete(def, "digest")
	definition, err := marshal(def)
	if err != nil {
		return nil, err
	}
	f := File{Schema: baumschema.TileTypeName, Definition: definition, Glance: t.Entry.Glance, ReviewChecks: t.Entry.ReviewChecks,
		AppliesWith: nonNil(t.Entry.AppliesWith), Paths: nonNil(t.Paths), Samples: t.Entry.Samples,
		Exported: &Exported{At: now.UTC().Format(time.RFC3339), Baum: version}}
	out, err := marshal(f)
	if err != nil {
		return nil, err
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, out, "", "  "); err != nil {
		return nil, err
	}
	indented.WriteByte('\n')
	if indented.Len() > MaxFileBytes {
		return nil, fmt.Errorf("The tile type is larger than a file holds (%d MiB)", MaxFileBytes>>20)
	}
	return indented.Bytes(), nil
}

func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// FileError is a file an import refuses; its message says why, naming
// places, never the file's content.
type FileError struct{ Reason string }

func (e *FileError) Error() string { return e.Reason }

func refuseFile(format string, args ...any) error {
	return &FileError{Reason: fmt.Sprintf(format, args...)}
}

// ReadFile checks an untrusted file and returns the change it holds, which
// passed Prepare: its origin is import and its digest is computed.
func ReadFile(raw []byte) (Change, error) {
	if len(raw) > MaxFileBytes {
		return Change{}, refuseFile("The file is larger than %d MiB", MaxFileBytes>>20)
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return Change{}, refuseFile("The file holds a NUL byte: it is not a tile type file")
	}
	if err := checkShape(raw); err != nil {
		return Change{}, err
	}
	var f File
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&f); err != nil {
		return Change{}, refuseFile("The file is not a tile type file: %s", decodeProblem(err))
	}
	if d.Decode(new(any)) != io.EOF {
		return Change{}, refuseFile("The file holds more than one JSON value")
	}
	if f.Schema != baumschema.TileTypeName {
		return Change{}, refuseFile("The file is not a tile type file this baum reads (its schema is not %s)", baumschema.TileTypeName)
	}
	var members map[string]json.RawMessage
	if json.Unmarshal(f.Definition, &members) != nil || members == nil {
		return Change{}, refuseFile("The file's definition is not a JSON object")
	}
	// Neither is read from a file: the digest is computed, and whether a type
	// is baum's own follows from that digest alone.
	delete(members, "digest")
	delete(members, "builtin")
	definition, err := marshal(members)
	if err != nil {
		return Change{}, refuseFile("The file's definition cannot be read")
	}
	sealed, err := sealRaw(definition)
	if err != nil {
		return Change{}, refuseFile("The file's definition cannot be read")
	}
	def, problems := tiles.NewValidator().CheckDefinition(sealed)
	if len(problems) > 0 {
		return Change{}, &FileError{Reason: refuse("definition", problems).Error()}
	}
	var lint []string
	for _, finding := range kit.Lint(def.Kit, def.Source.HTML) {
		if finding.Severity == kit.Error {
			lint = append(lint, fmt.Sprintf("line %d: %s", finding.Line, finding.Message))
		}
	}
	if len(lint) > 0 {
		return Change{}, &FileError{Reason: refuse("component", lint).Error()}
	}
	c := Change{Definition: sealed, Glance: f.Glance, ReviewChecks: f.ReviewChecks, AppliesWith: f.AppliesWith,
		Paths: f.Paths, Samples: f.Samples, Origin: OriginImport}
	if _, err := Prepare(c); err != nil {
		return Change{}, &FileError{Reason: err.Error()}
	}
	return c, nil
}

// decodeProblem says where a strict decode failed, without the content.
func decodeProblem(err error) string {
	var syntax *json.SyntaxError
	var typed *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntax):
		return fmt.Sprintf("invalid JSON at byte %d", syntax.Offset)
	case errors.As(err, &typed):
		return fmt.Sprintf("%s has the wrong type", typed.Field)
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		return "it has a member a tile type file does not have"
	}
	return "invalid JSON"
}

// checkShape walks the JSON's tokens and refuses nesting deeper than
// maxFileDepth or more than maxFileValues values, before it is decoded.
func checkShape(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	depth, values := 0, 0
	for {
		token, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return refuseFile("The file is not valid JSON")
		}
		if text, ok := token.(string); ok && strings.ContainsRune(text, 0) {
			return refuseFile("The file holds a NUL character: it is not a tile type file")
		}
		switch token {
		case json.Delim('{'), json.Delim('['):
			depth++
			if depth > maxFileDepth {
				return refuseFile("The file's JSON is nested deeper than %d levels", maxFileDepth)
			}
		case json.Delim('}'), json.Delim(']'):
			depth--
			continue
		}
		values++
		if values > maxFileValues {
			return refuseFile("The file holds more than %d JSON values", maxFileValues)
		}
	}
}

// Imported is what an import saved, or would save: the type's name, the
// name the file gave it when that one was taken (renamed with a numeric
// suffix), and whether its render check ran.
type Imported struct {
	Name        string `json:"name"`
	RenamedFrom string `json:"renamed_from,omitempty"`
	Checked     bool   `json:"checked"`
}

// SampleCheck runs the render check on an imported type's samples and says
// whether it passed (VerdictPass) or the checker's own word. ErrNoRunner
// says no runner is available: the type is then marked "Not checked yet".
// The render check lives beside this package; a nil SampleCheck is no
// runner.
type SampleCheck func(ctx context.Context, entry library.Entry) (verdict string, err error)

// ErrNoRunner is a SampleCheck without a runner to check on.
var ErrNoRunner = errors.New("no render check runner is available")

// VerdictUnchecked marks a version imported without a render check.
const VerdictUnchecked = "unchecked"

// Candidate is a checked file, ready to save: the change (renamed when its
// name is taken) and how it was named.
type Candidate struct {
	Change  Change
	Entry   library.Entry
	Name    string
	Renamed string
}

// Inspect checks a file and plans its import against the library as it is
// now: a name already in the library (a deleted type's too, until it is
// purged) gets the first free numeric suffix, name_2, name_3 and so on.
func (s *Store) Inspect(ctx context.Context, raw []byte) (Candidate, error) {
	c, err := ReadFile(raw)
	if err != nil {
		return Candidate{}, err
	}
	p, err := Prepare(c)
	if err != nil {
		return Candidate{}, &FileError{Reason: err.Error()}
	}
	asked := p.Name()
	name, err := s.freeName(ctx, asked)
	if err != nil {
		return Candidate{}, err
	}
	out := Candidate{Change: c, Entry: p.Entry, Name: name}
	if name != asked {
		out.Renamed = asked
		if out.Change, err = renamed(c, name); err != nil {
			return Candidate{}, err
		}
		p, err := Prepare(out.Change)
		if err != nil {
			return Candidate{}, &FileError{Reason: err.Error()}
		}
		out.Entry = p.Entry
	}
	return out, nil
}

// freeName is name, or name with the first numeric suffix the library does
// not hold.
func (s *Store) freeName(ctx context.Context, name string) (string, error) {
	for n := 1; n <= maxRenames+1; n++ {
		candidate := name
		if n > 1 {
			suffix := "_" + strconv.Itoa(n)
			base := name
			if len(base)+len(suffix) > 64 {
				base = base[:64-len(suffix)]
			}
			candidate = base + suffix
		}
		if CheckName(candidate) != nil {
			continue
		}
		st, err := s.State(ctx, candidate)
		if err != nil {
			return "", err
		}
		if !st.Present {
			return candidate, nil
		}
	}
	return "", refuseFile("Every name like %s is taken in your library; rename or delete one first", name)
}

// renamed is the change with its definition's name replaced.
func renamed(c Change, name string) (Change, error) {
	var def map[string]json.RawMessage
	if err := json.Unmarshal(c.Definition, &def); err != nil {
		return c, err
	}
	def["name"], _ = json.Marshal(name)
	delete(def, "digest")
	encoded, err := marshal(def)
	if err != nil {
		return c, err
	}
	if c.Definition, err = sealRaw(encoded); err != nil {
		return c, err
	}
	return c, nil
}

// Import checks a file and saves it as a new type (Inspect, then Create):
// origin import, renamed when its name is taken. check runs the render check
// on its samples; with none, or without a runner, the version is marked
// "Not checked yet" (VerdictUnchecked) until a check marks it.
func (s *Store) Import(ctx context.Context, raw []byte, check SampleCheck) (Imported, error) {
	candidate, err := s.Inspect(ctx, raw)
	if err != nil {
		return Imported{}, err
	}
	verdict := VerdictUnchecked
	if check != nil {
		switch v, err := check(ctx, candidate.Entry); {
		case errors.Is(err, ErrNoRunner):
		case err != nil:
			return Imported{}, err
		case v != VerdictPass:
			return Imported{}, refuseFile("The tile type did not pass its render check (%s)", v)
		default:
			verdict = VerdictPass
		}
	}
	saved, err := s.Create(ctx, candidate.Change, nil)
	if err != nil {
		return Imported{}, err
	}
	if err := s.MarkChecked(ctx, saved.Entry.Definition.Digest, verdict); err != nil {
		return Imported{}, err
	}
	return Imported{Name: saved.Name, RenamedFrom: candidate.Renamed, Checked: verdict != VerdictUnchecked}, nil
}

// MarkChecked records a render check's verdict on a definition digest: an
// imported version is "Not checked yet" while its verdict is
// VerdictUnchecked. The render check calls it when it checks a version.
func (s *Store) MarkChecked(ctx context.Context, digest, verdict string) error {
	return s.write(ctx, func(conn querierExec) error {
		_, err := conn.ExecContext(ctx, `INSERT INTO tile_type_checks(digest, verdict, at) VALUES (?, ?, ?)
ON CONFLICT(digest) DO UPDATE SET verdict = excluded.verdict, at = excluded.at`, digest, verdict, s.now().Format(time.RFC3339Nano))
		return err
	})
}
