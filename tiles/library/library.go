// Package library holds baum's built-in custom tile types: tile types written
// once against the tile kit, which a review generation offers to the analysis
// and fills with data for a pull request they apply to.
//
// Each type is a directory of plain files, the layout an author of a new type
// uses too (see the kit's GUIDE.md):
//
//   - type.json: name, title, use_when, glance, sizes and kit;
//   - fill.md: the instructions for the model that fills the data;
//   - review_checks.md: what the review's analysis should check when the type
//     applies, raised as risks rather than written into the tile;
//   - schema.json: the data schema, in the tiles package's restricted dialect;
//   - tile.html: the component, written against kit v1 only;
//   - samples/*.json: synthetic data in fake repositories, with the hunks and
//     items it cites, to check and draw the type without a review.
//
// All returns the built-in types with their definitions sealed (digest
// written), Load reads a directory of that layout and Check runs every static
// check a type must pass. See "Tile library" in ARCHITECTURE.md.
package library

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/kit"
)

//go:embed schema_change api_change
var files embed.FS

// Names are the built-in types' names, in the order a generation offers them.
var Names = []string{"schema_change", "api_change"}

// Related is the data property the host writes and a fill never does: the
// review's risks and evidence items whose hunks the tile cites. FillSchema
// leaves it out.
const Related = "related"

// The limits of a type's own texts beside its definition.
const (
	// MaxGlance bounds Entry.Glance, the line a composer reads about what
	// the glance shows.
	MaxGlance = 200
	// MaxReviewChecks bounds Entry.ReviewChecks.
	MaxReviewChecks = 1500
)

// Entry is a library type.
type Entry struct {
	// Definition is the type's definition with its digest written.
	Definition tiles.TypeDefinition
	// Encoded is Definition as a custom tile embeds it (tiles.LibraryTile).
	Encoded json.RawMessage
	// Glance says in one line what the glance shows, for a composer.
	Glance string
	// ReviewChecks is what the review's analysis should check when the type
	// applies: the type's knowledge of what goes wrong, raised as risks of
	// the review rather than written into the tile. Empty when the type has
	// none.
	ReviewChecks string
	// Samples are synthetic data with the hunks and items they cite.
	Samples []Sample
}

// Sample is synthetic data for a type, with what its citations resolve to,
// so the type can be checked and drawn without a review.
type Sample struct {
	Name string          `json:"name"`
	Data json.RawMessage `json:"data"`
	// Hunks are the diff hunks the data cites, by hunk ID.
	Hunks map[string]Hunk `json:"hunks,omitempty"`
	// Items are the risks and evidence items the data cites, by ID.
	Items map[string]Item `json:"items,omitempty"`
}

// Hunk is a cited hunk as a sample gives it: its file, the lines of the new
// file it covers and its lines.
type Hunk struct {
	Path      string `json:"path"`
	Change    string `json:"change,omitempty"`
	Range     [2]int `json:"range"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Header    string `json:"header,omitempty"`
	// Lines are the hunk's lines; Old and New number the side they are on.
	Lines []Line `json:"lines,omitempty"`
}

// Line is one line of a hunk: add, remove or context.
type Line struct {
	Kind string `json:"kind"`
	Old  int    `json:"old,omitempty"`
	New  int    `json:"new,omitempty"`
	Text string `json:"text"`
}

// Item is a cited risk or evidence item as a sample gives it.
type Item struct {
	Label string `json:"label"`
	// Tier is a risk's tier, or an evidence item's status.
	Tier string `json:"tier,omitempty"`
	Text string `json:"text,omitempty"`
}

// meta is type.json.
type meta struct {
	Name    string       `json:"name"`
	Title   string       `json:"title"`
	UseWhen string       `json:"use_when"`
	Glance  string       `json:"glance"`
	Sizes   []tiles.Size `json:"sizes"`
	Kit     int          `json:"kit"`
}

var all = sync.OnceValue(func() []Entry {
	entries := make([]Entry, 0, len(Names))
	for _, name := range Names {
		dir, err := fs.Sub(files, name)
		if err != nil {
			panic(err)
		}
		entry, err := Load(dir)
		if err != nil {
			panic(fmt.Sprintf("library: %s: %v", name, err))
		}
		entries = append(entries, entry)
	}
	return entries
})

// All returns the built-in types in Names order. The entries are shared:
// treat them as read-only.
func All() []Entry { return all() }

// Get returns the built-in type of that name.
func Get(name string) (Entry, bool) {
	for _, entry := range all() {
		if entry.Definition.Name == name {
			return entry, true
		}
	}
	return Entry{}, false
}

// Load reads a type from a directory of the library's layout and seals its
// definition. It fails when a file is missing or does not decode; Check says
// whether the type is good.
func Load(dir fs.FS) (Entry, error) {
	read := func(name string) (string, error) {
		data, err := fs.ReadFile(dir, name)
		if err != nil {
			return "", err
		}
		if !utf8.Valid(data) {
			return "", fmt.Errorf("%s is not valid UTF-8", name)
		}
		return string(data), nil
	}
	var m meta
	text, err := read("type.json")
	if err != nil {
		return Entry{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return Entry{}, fmt.Errorf("type.json: %w", err)
	}
	fill, err := read("fill.md")
	if err != nil {
		return Entry{}, err
	}
	checks, err := read("review_checks.md")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Entry{}, err
	}
	schema, err := read("schema.json")
	if err != nil {
		return Entry{}, err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(schema)); err != nil {
		return Entry{}, fmt.Errorf("schema.json: %w", err)
	}
	html, err := read("tile.html")
	if err != nil {
		return Entry{}, err
	}
	def := tiles.TypeDefinition{
		Name: m.Name, Title: m.Title, UseWhen: m.UseWhen, Fill: strings.TrimSpace(fill),
		DataSchema: compact.Bytes(), Kit: m.Kit, Sizes: m.Sizes,
		Source: tiles.TypeSource{Format: "html", HTML: html},
	}
	encoded, err := Seal(&def)
	if err != nil {
		return Entry{}, err
	}
	entry := Entry{Definition: def, Encoded: encoded, Glance: strings.TrimSpace(m.Glance), ReviewChecks: strings.TrimSpace(checks)}
	names, err := fs.Glob(dir, "samples/*.json")
	if err != nil {
		return Entry{}, err
	}
	sort.Strings(names)
	for _, name := range names {
		text, err := read(name)
		if err != nil {
			return Entry{}, err
		}
		var sample Sample
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&sample); err != nil {
			return Entry{}, fmt.Errorf("%s: %w", name, err)
		}
		if sample.Name == "" {
			sample.Name = strings.TrimSuffix(path.Base(name), ".json")
		}
		entry.Samples = append(entry.Samples, sample)
	}
	return entry, nil
}

// Seal writes a definition's digest and returns it encoded, as a custom tile
// embeds it.
func Seal(def *tiles.TypeDefinition) (json.RawMessage, error) {
	def.Digest = ""
	encoded, err := encode(def)
	if err != nil {
		return nil, err
	}
	digest, err := tiles.DefinitionDigest(encoded)
	if err != nil {
		return nil, err
	}
	def.Digest = digest
	return encode(def)
}

func encode(value any) (json.RawMessage, error) {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimSpace(b.Bytes())), nil
}

// FillSchema returns the schema a fill's data must have: the type's data
// schema without the properties the host writes (Related), with the reserved
// definitions it uses merged in, so a model's structured output can use it
// as it is.
func (e Entry) FillSchema() (json.RawMessage, error) {
	var schema map[string]any
	decoder := json.NewDecoder(bytes.NewReader(e.Definition.DataSchema))
	decoder.UseNumber()
	if err := decoder.Decode(&schema); err != nil {
		return nil, err
	}
	if properties, ok := schema["properties"].(map[string]any); ok {
		delete(properties, Related)
	}
	if required, ok := schema["required"].([]any); ok {
		schema["required"] = slices.DeleteFunc(required, func(name any) bool { return name == Related })
	}
	encoded, err := encode(schema)
	if err != nil {
		return nil, err
	}
	return tiles.StandaloneDataSchema(encoded)
}

// CheckFill checks data a fill wrote for a type against the pull request's
// diff and returns its problems: a property only the host writes, the
// type's own checks (tiles.Validator.CheckData), and citations: a hunk must
// be in the diff and its lines inside the hunk's new range, and nothing may
// cite a risk, evidence item or tile, which do not exist while fills run.
// Problems name the place in the data, never its values.
func CheckFill(v *tiles.Validator, def json.RawMessage, data json.RawMessage, diff []bundle.Hunk) []string {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err == nil {
		if _, ok := top[Related]; ok {
			return []string{fmt.Sprintf("/%s is written by the host; leave it out", Related)}
		}
	}
	if problems := v.CheckData(def, data); len(problems) > 0 {
		return problems
	}
	hunks := make(map[string]bundle.Hunk, len(diff))
	for _, h := range diff {
		hunks[h.ID] = h
	}
	var problems []string
	for _, c := range Citations(data) {
		switch c.Type {
		case "hunk":
			h, ok := hunks[c.ID]
			switch {
			case !ok:
				problems = append(problems, c.At+" names a hunk that is not in the diff")
			case c.Lines == nil:
			case h.LineCount == 0:
				problems = append(problems, c.At+" has lines, but the hunk adds no lines; cite it without lines")
			case c.Lines[0] > c.Lines[1]:
				problems = append(problems, c.At+" has lines that end before they start; write [first, last]")
			case c.Lines[0] < h.StartLine || c.Lines[1] > h.StartLine+h.LineCount-1:
				problems = append(problems, fmt.Sprintf("%s has lines outside the hunk, whose new lines are %d to %d", c.At, h.StartLine, h.StartLine+h.LineCount-1))
			}
		case "risk", "evidence", "tile":
			problems = append(problems, fmt.Sprintf("%s cites a %s, which a fill cannot: cite the code", c.At, c.Type))
		}
	}
	return problems
}

// Citation is a citation found in data: its place, type, ID and lines.
type Citation struct {
	// At is the citation's JSON pointer in the data.
	At   string
	Type string
	ID   string
	// Path is a file citation's path.
	Path string
	// Lines is a hunk citation's [first, last], or nil.
	Lines []int
}

// citationTypes are the types a citation may have (the tile schema's
// $defs/citation): the review anchors and the item references.
var citationTypes = map[string]bool{
	"hunk": true, "file": true, "commit": true, "pr": true, "pr_body": true, "pr_comment": true,
	"review_thread": true, "prompt": true, "session_event": true, "check": true, "bot_report": true,
	"issue": true, "spec": true, "external_url": true, "attachment": true,
	"risk": true, "evidence": true, "change": true, "tile": true,
}

// Citations returns the objects in data that look like citations (a type a
// citation has), in document order: members by name, items in order. It
// does not check their shape, which the data schema does.
func Citations(data json.RawMessage) []Citation {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil
	}
	var out []Citation
	var walk func(any, string)
	walk = func(value any, at string) {
		switch value := value.(type) {
		case []any:
			for i, item := range value {
				walk(item, fmt.Sprintf("%s/%d", at, i))
			}
		case map[string]any:
			if kind, ok := value["type"].(string); ok && citationTypes[kind] {
				c := Citation{At: at, Type: kind}
				c.ID, _ = value["id"].(string)
				c.Path, _ = value["path"].(string)
				if lines, ok := value["lines"].([]any); ok && len(lines) == 2 {
					first, ok1 := lines[0].(float64)
					last, ok2 := lines[1].(float64)
					if ok1 && ok2 {
						c.Lines = []int{int(first), int(last)}
					}
				}
				out = append(out, c)
				return
			}
			names := make([]string, 0, len(value))
			for name := range value {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				walk(value[name], at+"/"+strings.NewReplacer("~", "~0", "/", "~1").Replace(name))
			}
		}
	}
	walk(value, "")
	return out
}

// Check runs every static check a type must pass and returns its problems:
// its definition (tiles.Validator.CheckDefinition, which covers the data
// schema's dialect), the kit's lint, errors and warnings alike, the glance
// line and review checks within their limits, at least one sample, each
// sample's data against the schema with every citation resolving to the
// sample's own hunks and items, the edge samples the schema yields
// (tiles.EdgeSamples) and the frame document's composition.
func Check(e Entry) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	v := tiles.NewValidator()
	if _, defProblems := v.CheckDefinition(e.Encoded); len(defProblems) > 0 {
		for _, p := range defProblems {
			add("definition: %s", p)
		}
		return problems
	}
	def := e.Definition
	for _, f := range kit.Lint(def.Kit, def.Source.HTML) {
		add("tile.html:%d:%d: %s %s: %s", f.Line, f.Column, f.Severity, f.Rule, f.Message)
	}
	if _, err := kit.Compose(def.Kit, def.Source.HTML); err != nil {
		add("tile.html: %v", err)
	}
	switch n := utf8.RuneCountInString(e.Glance); {
	case n == 0:
		add("type.json: glance is empty; say in one line what the glance shows")
	case n > MaxGlance || strings.ContainsAny(e.Glance, "\r\n"):
		add("type.json: glance must be one line of at most %d characters", MaxGlance)
	}
	if utf8.RuneCountInString(e.ReviewChecks) > MaxReviewChecks {
		add("review_checks.md is longer than %d characters", MaxReviewChecks)
	}
	if len(e.Samples) == 0 {
		add("samples: add at least one sample in samples/*.json")
	}
	var pool []json.RawMessage
	for _, sample := range e.Samples {
		for _, p := range v.CheckData(e.Encoded, sample.Data) {
			add("sample %s: %s", sample.Name, p)
		}
		_, unresolved := sample.Refs("detail")
		for _, at := range unresolved {
			add("sample %s: the citation at %s does not resolve to the sample's hunks or items, or its lines are outside the hunk", sample.Name, at)
		}
		for _, c := range Citations(sample.Data) {
			if c.Type == "hunk" && len(pool) < 32 {
				raw, _ := encode(map[string]any{"type": "hunk", "id": c.ID})
				pool = append(pool, raw)
			}
		}
	}
	_, warnings, err := tiles.EdgeSamples(def.DataSchema, pool)
	if err != nil {
		add("schema.json: %v", err)
	}
	for _, w := range warnings {
		add("edge samples: %s", w)
	}
	return problems
}
