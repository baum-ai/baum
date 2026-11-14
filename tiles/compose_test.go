package tiles_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/goldentest"
	"github.com/baum-ai/baum/schema"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/tilestest"
)

// exhibits is the rare-blocks fixture with a table in the retry policy
// change too, so two changes have blocks to promote.
var exhibits = fixture{name: "exhibits", mutate: func(doc map[string]any) {
	namedFixture("rare-blocks").mutate(doc)
	retry := sectionOf(doc, "sec_retry_policy")
	retry["blocks"] = append(array(retry, "blocks"), map[string]any{
		"kind": "table", "columns": []any{"Attempt", "Delay"},
		"rows":   []any{[]any{"1", "1s"}, []any{"2", "2s"}, []any{"3", "4s"}},
		"source": []any{map[string]any{"type": "hunk", "id": "h_000000000001"}},
	})
}}

// namedFixture returns the fixture with the name.
func namedFixture(name string) fixture {
	for _, f := range fixtures {
		if f.name == name {
			return f
		}
	}
	panic("no fixture " + name)
}

// withKind returns a fixture with the summary's change kind set.
func withKind(f fixture, kind string) fixture {
	mutate := f.mutate
	f.name = f.name + "-" + kind
	f.mutate = func(doc map[string]any) {
		if mutate != nil {
			mutate(doc)
		}
		object(doc, "summary")["change_kind"] = kind
	}
	return f
}

// composedView is what a golden of a composition records: the fixes, the
// normalised composition and the composed document's layout and tiles.
type composedView struct {
	Fixes       []string          `json:"fixes"`
	Composition tiles.Composition `json:"composition"`
	Layout      tiles.Layout      `json:"layout"`
	Tiles       []tileView        `json:"tiles"`
}

type tileView struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Title    string   `json:"title"`
	State    string   `json:"state"`
	Covers   []string `json:"covers,omitempty"`
	Problems []string `json:"problems,omitempty"`
}

func viewOf(t *testing.T, n tiles.Composition, report tiles.Report, doc *tiles.Document) composedView {
	t.Helper()
	view := composedView{Fixes: nonNilStrings(report.Messages()), Composition: n, Layout: doc.Layout}
	for _, tile := range doc.Tiles {
		view.Tiles = append(view.Tiles, tileView{tile.ID, tile.Type, tile.Title, tile.State, tile.Covers, tile.Problems})
	}
	return view
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// checkComposed runs every check on a composition applied to a fixture's
// document: the generated invariants, a fixed point of normalisation, no
// unavailable tile once normalised, and a composition that validates.
func checkComposed(t *testing.T, normalized []byte, doc *tiles.Document, c *tiles.Composition) (tiles.Composition, tiles.Report, *tiles.Document) {
	t.Helper()
	n, report := tiles.NormalizeComposition(doc, c)
	again, fixes := tiles.NormalizeComposition(doc, &n)
	if len(fixes.Fixes) > 0 {
		t.Errorf("normalising twice fixes again: %v", fixes.Messages())
	}
	if first, second := mustJSON(t, n), mustJSON(t, again); !bytes.Equal(first, second) {
		t.Errorf("normalising twice changes the composition:\n%s\n%s", first, second)
	}
	composed, err := tiles.Compose(doc, &n)
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range tilestest.CheckGenerated(normalized, &n, composed) {
		t.Error(failure)
	}
	for _, tile := range composed.Tiles {
		if tile.State == tiles.StateUnavailable {
			t.Errorf("tile %s of a normalised composition is unavailable: %v", tile.ID, tile.Problems)
		}
	}
	if !n.Empty() {
		validateComposition(t, n)
	}
	return n, report, composed
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

var compiledComposition = sync.OnceValues(schema.CompileComposition)

// validateComposition checks a normalised composition against
// schema/composition-1.json.
func validateComposition(t *testing.T, c tiles.Composition) {
	t.Helper()
	compiled, err := compiledComposition()
	if err != nil {
		t.Fatal(err)
	}
	var instance any
	if err := json.Unmarshal(mustJSON(t, c), &instance); err != nil {
		t.Fatal(err)
	}
	if err := compiled.Validate(instance); err != nil {
		t.Errorf("the normalised composition fails its schema: %v", err)
	}
}

// A nil or empty composition gives the converted document, byte for byte.
func TestComposeEmptyIsConversion(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			normalized, doc := convert(t, f)
			want := mustJSON(t, doc)
			for _, c := range []*tiles.Composition{nil, {Schema: tiles.CompositionName}, {Schema: tiles.CompositionName, Notes: &tiles.Notes{Why: map[string]string{"overview": "Leads."}}}} {
				composed, err := tiles.Compose(doc, c)
				if err != nil {
					t.Fatal(err)
				}
				if got := mustJSON(t, composed); !bytes.Equal(got, want) {
					t.Fatalf("composing %v changed the document", c)
				}
				if c == nil {
					for _, failure := range tilestest.CheckGenerated(normalized, c, composed) {
						t.Error(failure)
					}
				}
			}
			if got := mustJSON(t, doc); !bytes.Equal(got, want) {
				t.Fatal("Compose changed its input")
			}
		})
	}
}

// Each template lays out a document of its change kinds, with a library,
// a promoted and an authored tile.
func TestTemplates(t *testing.T) {
	for _, tc := range []struct{ template, kind string }{
		{tiles.TemplateFeature, "feature"},
		{tiles.TemplateFeature, "bugfix"},
		{tiles.TemplateRefactor, "refactor"},
		{tiles.TemplateDependency, "dependency"},
		{tiles.TemplateDocs, "docs"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			if got := tiles.TemplateName(tc.kind); got != tc.template {
				t.Fatalf("TemplateName(%q) = %q, want %q", tc.kind, got, tc.template)
			}
			normalized, doc := convert(t, withKind(exhibits, tc.kind))
			content := &tiles.Composition{
				Library:    []tiles.LibraryTile{tilestest.LibraryTile("retry_budget", "h_000000000001")},
				Promotions: []tiles.Promotion{{ID: "table_delays", Change: "sec_retry_policy", Block: 4, Title: "Delays by attempt"}},
				Authored:   []tiles.AuthoredTile{tilestest.AuthoredTile("prose_why_jitter", "h_000000000002")},
			}
			n, report := tiles.Template(doc, content)
			if len(report.Fixes) > 0 {
				t.Errorf("the template needed fixes: %v", report.Messages())
			}
			if n.Generator == nil || n.Generator.Template != tc.template {
				t.Errorf("the generator names template %+v, want %s", n.Generator, tc.template)
			}
			_, again, composed := checkComposed(t, normalized, doc, &n)
			if len(again.Fixes) > 0 {
				t.Errorf("the template's composition needs fixes: %v", again.Messages())
			}
			if tc.kind == tc.template {
				goldentest.Golden(t, "testdata/compositions/template-"+tc.template+".json", viewOf(t, n, report, composed))
			}
		})
	}
	for kind, want := range map[string]string{
		"cherry_pick": tiles.TemplateDependency, "upstream_sync": tiles.TemplateDependency,
		"ci": tiles.TemplateDocs, "mechanical": tiles.TemplateDocs, "translation": tiles.TemplateDocs,
		"release": tiles.TemplateDocs, "export": tiles.TemplateDocs, "": tiles.TemplateFeature, "unknown": tiles.TemplateFeature,
	} {
		if got := tiles.TemplateName(kind); got != want {
			t.Errorf("TemplateName(%q) = %q, want %q", kind, got, want)
		}
	}
}

// A draft's template places the pending library tiles the outline planned.
func TestTemplateDraft(t *testing.T) {
	normalized, doc := convert(t, fixtureNamed(t, "draft-outline"))
	planned := tilestest.LibraryTile("retry_budget")
	planned.Pending, planned.Data = true, json.RawMessage(`{}`)
	n, report := tiles.Template(doc, &tiles.Composition{Library: []tiles.LibraryTile{planned}})
	if len(report.Fixes) > 0 {
		t.Errorf("fixes: %v", report.Messages())
	}
	composed, err := tiles.Compose(doc, &n)
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range tilestest.CheckGenerated(normalized, &n, composed) {
		t.Error(failure)
	}
	for _, tile := range composed.Tiles {
		if tile.ID == "retry_budget" && tile.State != tiles.StatePending {
			t.Errorf("the planned library tile is %s, want pending", tile.State)
		}
	}
}

// Each normalisation rule repairs what it names, and reports it.
func TestNormalizeRules(t *testing.T) {
	sizes := func(entries ...string) []tiles.Entry {
		var out []tiles.Entry
		for _, entry := range entries {
			id, rest, _ := strings.Cut(entry, ":")
			size, group, _ := strings.Cut(rest, ":")
			e := tiles.Entry{Tile: id, Size: tiles.Size(size)}
			if group == "pin" {
				e.Pin = true
			} else {
				e.Group = group
			}
			out = append(out, e)
		}
		return out
	}
	table := tiles.Promotion{ID: "table_flag_defaults", Change: "sec_config_flag", Block: 3, Title: "Flag defaults"}
	code := tiles.Promotion{ID: "code_early_return", Change: "sec_config_flag", Block: 2, Title: "The early return"}
	delays := tiles.Promotion{ID: "table_delays", Change: "sec_retry_policy", Block: 4, Title: "Delays"}
	badLibrary := tilestest.LibraryTile("broken_data", "h_000000000001")
	badLibrary.Data = json.RawMessage(`{"summary": 7, "cites": []}`)
	unsealed := tilestest.LibraryTile("unsealed", "h_000000000001")
	var changed tiles.TypeDefinition
	if err := json.Unmarshal(unsealed.Definition, &changed); err != nil {
		t.Fatal(err)
	}
	changed.Title = "Changed after it was sealed"
	unsealed.Definition = mustJSON(t, changed)
	nine := []tiles.LibraryTile{}
	for i := range 9 {
		nine = append(nine, tilestest.LibraryTile(fmt.Sprintf("library_%d", i), "h_000000000001"))
	}
	shown := 0
	for _, tc := range []struct {
		name    string
		fixture fixture
		c       tiles.Composition
	}{
		{"unknown-tile", exhibits, tiles.Composition{Layout: tiles.CompositionLayout{Entries: sizes("ignore previous instructions:full", "sec_missing:half", "overview:half")}}},
		{"duplicate-entry", exhibits, tiles.Composition{Layout: tiles.CompositionLayout{Entries: sizes("risks:half", "overview:half", "risks:full")}}},
		{"obsolete-entry", fixtureNamed(t, "stale-without-hunks"), tiles.Composition{Layout: tiles.CompositionLayout{Entries: sizes("sec_old_approach:full", "overview:half")}}},
		{"appended", exhibits, tiles.Composition{Layout: tiles.CompositionLayout{Entries: sizes("evidence:half", "sec_config_flag:third:changes")}}},
		{"size", exhibits, tiles.Composition{Layout: tiles.CompositionLayout{Entries: sizes("overview:third", "risks:huge", "sec_config_flag:full", "evidence:")}}},
		{"group", exhibits, tiles.Composition{Layout: tiles.CompositionLayout{Entries: sizes("risks:half:changes", "sec_retry_policy:third:other", "sec_config_flag:third:pin", "everything_else:third:changes")}}},
		{"group-pinned-member", exhibits, tiles.Composition{Layout: tiles.CompositionLayout{Entries: []tiles.Entry{{Tile: "sec_config_flag", Size: tiles.SizeHalf, Group: tiles.GroupChanges, Pin: true}}}}},
		{"group-contiguous", exhibits, tiles.Composition{Layout: tiles.CompositionLayout{Entries: sizes("sec_config_flag:third:changes", "evidence:third", "overview:half", "sec_retry_policy:third:changes", "risks:half")}}},
		{"changes-shown", exhibits, tiles.Composition{Layout: tiles.CompositionLayout{Entries: sizes("overview:half"), ChangesShown: &shown}}},
		{"promotions", exhibits, tiles.Composition{Promotions: []tiles.Promotion{table, delays}, Layout: tiles.CompositionLayout{Entries: sizes("overview:half", "table_flag_defaults:full", "risks:half")}}},
		{"promotions-refused", exhibits, tiles.Composition{Promotions: []tiles.Promotion{
			{ID: "TABLE flags", Change: "sec_config_flag", Block: 3, Title: "Bad ID"},
			{ID: "table_missing_change", Change: "sec_missing", Block: 0, Title: "No change"},
			{ID: "table_no_block", Change: "sec_config_flag", Block: 9, Title: "No block"},
			{ID: "table_moved_block", Change: "sec_retry_policy", Block: 0, Title: "An intent block, not a table"},
			{ID: "code_wrong_kind", Change: "sec_config_flag", Block: 3, Title: "A table, not code"},
			{ID: "table_changed", Change: "sec_retry_policy", Block: 4, Digest: "sha256:" + strings.Repeat("0", 64), Title: "Changed"},
			table,
			code,
			table,
			{ID: "table_obsolete", Change: "sec_generated_client", Block: 0, Title: "Everything else's"},
		}}},
		{"promotions-limit", withManyBlocks(), tiles.Composition{Promotions: manyPromotions(t)}},
		{"authored", exhibits, tiles.Composition{Authored: []tiles.AuthoredTile{
			tilestest.AuthoredTile("table_settings", "h_000000000003"),
			tilestest.AuthoredTile("code_give_up", "h_000000000001"),
			tilestest.AuthoredTile("prose_why", "h_000000000002"),
			tilestest.AuthoredTile("prose_fourth", "h_000000000002"),
		}}},
		{"authored-refused", exhibits, tiles.Composition{Authored: []tiles.AuthoredTile{
			tilestest.AuthoredTile("table_uncited"),
			tilestest.AuthoredTile("code_unknown_hunk", "h_ffffffffffff"),
			{ID: "prose_with_origin", Title: "Moved", Data: json.RawMessage(`{"text": "Moved.", "anchors": [{"type": "hunk", "id": "h_000000000001"}], "origin": {"change": "sec_retry_policy", "block": 0}}`)},
			{ID: "prose_array", Title: "Array", Data: json.RawMessage(`[1]`)},
			{ID: "prose_shape", Title: "Shape", Data: json.RawMessage(`{"text": 3, "anchors": [{"type": "hunk", "id": "h_000000000001"}]}`)},
			{ID: "diagram_flow", Title: "Diagram", Data: json.RawMessage(`{}`)},
			{ID: "prose_file_only", Title: "Cites a file", Data: json.RawMessage(`{"text": "The worker.", "anchors": [{"type": "file", "path": "worker/deliver.go"}]}`)},
		}}},
		{"library", exhibits, tiles.Composition{Library: []tiles.LibraryTile{
			tilestest.LibraryTile("retry_budget", "h_000000000001"),
			{Definition: json.RawMessage(`{"name": "broken"`), Data: json.RawMessage(`{}`)},
			unsealed,
			badLibrary,
			tilestest.LibraryTile("retry_budget", "h_000000000002"),
			tilestest.LibraryTile("sec_config_flag", "h_000000000003"),
			tilestest.LibraryTile("cites_nothing_known", "h_eeeeeeeeeeee"),
			{Definition: tilestest.SealDefinition(tilestest.LibraryDefinition("planned")), Data: json.RawMessage(`{}`), Pending: true},
		}}},
		{"library-limit", exhibits, tiles.Composition{Library: nine}},
		{"renamed", exhibits, tiles.Composition{
			Library:    []tiles.LibraryTile{tilestest.LibraryTile("table_flag_defaults", "h_000000000003")},
			Promotions: []tiles.Promotion{table},
			Authored:   []tiles.AuthoredTile{tilestest.AuthoredTile("table_flag_defaults", "h_000000000003"), tilestest.AuthoredTile("table_flag_defaults_2", "h_000000000003")},
			Layout:     tiles.CompositionLayout{Entries: sizes("table_flag_defaults:half", "overview:half")},
		}},
		{"renamed-long", exhibits, tiles.Composition{
			Authored: []tiles.AuthoredTile{
				tilestest.AuthoredTile("prose_"+strings.Repeat("a", 64), "h_000000000001"),
				tilestest.AuthoredTile("prose_"+strings.Repeat("a", 64), "h_000000000002"),
			},
		}},
		{"titles", exhibits, tiles.Composition{
			Promotions: []tiles.Promotion{{ID: "table_flag_defaults", Change: "sec_config_flag", Block: 3, Title: "  "}},
			Authored:   []tiles.AuthoredTile{{ID: "prose_long", Title: strings.Repeat("A very long title\n", 10), Data: tilestest.AuthoredTile("prose_x", "h_000000000001").Data}},
		}},
		{"notes", exhibits, tiles.Composition{
			Layout: tiles.CompositionLayout{Entries: sizes("overview:half")},
			Notes: &tiles.Notes{
				Why:              map[string]string{"overview": "Leads: " + strings.Repeat("why ", 60), "no_such_tile": "Nothing.", "risks": "Holds the high risk."},
				Moves:            slices.Repeat([]tiles.Move{{Tile: "risks", Why: "Moved up."}, {Tile: "gone", Why: "Gone."}}, 12),
				CustomSuggestion: &tiles.CustomSuggestion{Title: "Queue states", Shows: strings.Repeat("states ", 60), Why: "No tile shows them."},
			},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			normalized, doc := convert(t, tc.fixture)
			n, report, composed := checkComposed(t, normalized, doc, &tc.c)
			for _, message := range report.Messages() {
				if strings.Contains(message, "ignore previous") || strings.Contains(message, "TABLE") || strings.Contains(message, "why why") {
					t.Errorf("a fix quotes the composition: %s", message)
				}
			}
			goldentest.Golden(t, "testdata/compositions/normalize-"+tc.name+".json", viewOf(t, n, report, composed))
		})
	}
}

// manyPromotions promotes a table added to each of five changes.
func manyPromotions(t *testing.T) []tiles.Promotion {
	t.Helper()
	var out []tiles.Promotion
	for i := range 5 {
		out = append(out, tiles.Promotion{ID: fmt.Sprintf("table_part_%d", i), Change: fmt.Sprintf("sec_part_%d", i), Block: 0, Title: fmt.Sprintf("Part %d", i)})
	}
	return out
}

// Random compositions, as a model or an attacker might write them, applied
// to every fixture always give a valid generated document that keeps every
// guarantee, deterministically, and normalise to a fixed point that applies
// with no unavailable tile.
func TestComposeRandom(t *testing.T) {
	all := append(slices.Clone(fixtures), exhibits, withManyBlocks())
	runs := 24
	if testing.Short() || tiles.RaceEnabled {
		runs = 4
	}
	for _, f := range all {
		t.Run(f.name, func(t *testing.T) {
			normalized, doc := convert(t, f)
			before := mustJSON(t, doc)
			for seed := range runs {
				hostile := seed%2 == 1
				c := tilestest.RandomComposition(rand.New(rand.NewPCG(uint64(seed), 2022)), doc, hostile)
				composed, err := tiles.Compose(doc, c)
				if err != nil {
					t.Fatalf("seed %d: %v", seed, err)
				}
				for _, failure := range tilestest.CheckGenerated(normalized, c, composed) {
					t.Errorf("seed %d (hostile %v): %s", seed, hostile, failure)
				}
				again, err := tiles.Compose(doc, c)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(mustJSON(t, composed), mustJSON(t, again)) {
					t.Errorf("seed %d: composing twice gave different bytes", seed)
				}
				n, _, _ := checkComposed(t, normalized, doc, c)
				if first, second := mustJSON(t, n), mustJSON(t, mustNormalize(doc, c)); !bytes.Equal(first, second) {
					t.Errorf("seed %d: normalising twice gave different bytes", seed)
				}
				if t.Failed() {
					t.Fatalf("seed %d failed; composition: %s", seed, mustJSON(t, c))
				}
			}
			if !bytes.Equal(before, mustJSON(t, doc)) {
				t.Fatal("Compose changed its input")
			}
		})
	}
}

func mustNormalize(doc *tiles.Document, c *tiles.Composition) tiles.Composition {
	n, _ := tiles.NormalizeComposition(doc, c)
	return n
}

// withManyBlocks is the many-changes fixture with a table and a code block
// in each added change.
func withManyBlocks() fixture {
	many := namedFixture("many-changes")
	return fixture{name: "many-blocks", mutate: func(doc map[string]any) {
		many.mutate(doc)
		for i := range 6 {
			s := sectionOf(doc, fmt.Sprintf("sec_part_%d", i))
			source := []any{map[string]any{"type": "hunk", "id": fmt.Sprintf("h_00000000010%d", i)}}
			s["blocks"] = []any{
				map[string]any{"kind": "table", "columns": []any{"Key", "Value"}, "rows": []any{[]any{"part", fmt.Sprint(i)}}, "source": source},
				map[string]any{"kind": "intent", "text": "Keep parts small.", "anchors": source},
				map[string]any{"kind": "code", "language": "go", "content": fmt.Sprintf("part%d()", i), "source": source},
			}
		}
	}}
}

// StabilizeIDs gives a promotion of the same change and kind its previous
// ID, and renames a tile that would take an ID the previous version gave
// another subject.
func TestStabilizeIDs(t *testing.T) {
	_, doc := convert(t, exhibits)
	previous, _ := tiles.NormalizeComposition(doc, &tiles.Composition{
		Promotions: []tiles.Promotion{{ID: "table_flag_defaults", Change: "sec_config_flag", Block: 3, Title: "Flag defaults"}},
		Authored:   []tiles.AuthoredTile{tilestest.AuthoredTile("prose_why", "h_000000000001")},
		Layout:     tiles.CompositionLayout{Entries: []tiles.Entry{{Tile: "table_flag_defaults", Size: tiles.SizeHalf}}},
	})
	next := &tiles.Composition{
		Promotions: []tiles.Promotion{
			{ID: "table_settings", Change: "sec_config_flag", Block: 3, Title: "Settings"},
			{ID: "table_why", Change: "sec_retry_policy", Block: 4, Title: "Delays"},
		},
		Authored: []tiles.AuthoredTile{tilestest.AuthoredTile("table_flag_defaults", "h_000000000003")},
		Layout:   tiles.CompositionLayout{Entries: []tiles.Entry{{Tile: "table_settings", Size: tiles.SizeFull}, {Tile: "table_flag_defaults", Size: tiles.SizeHalf}}},
		Notes:    &tiles.Notes{Why: map[string]string{"table_settings": "Leads."}, Moves: []tiles.Move{{Tile: "table_settings", Why: "Up."}}},
	}
	got := tiles.StabilizeIDs(&previous, next)
	ids := func(c tiles.Composition) []string {
		var out []string
		for _, p := range c.Promotions {
			out = append(out, p.ID)
		}
		for _, a := range c.Authored {
			out = append(out, a.ID)
		}
		return out
	}
	// The authored table claims the ID the previous version gave the
	// promotion, so it is renamed, and the promotion of the same block takes
	// its previous ID back.
	if want := []string{"table_flag_defaults", "table_why", "table_flag_defaults_2"}; !slices.Equal(ids(got), want) {
		t.Errorf("IDs = %v, want %v", ids(got), want)
	}
	// Without the clash the promotion keeps its previous ID.
	next.Authored = nil
	next.Layout.Entries = next.Layout.Entries[:1]
	got = tiles.StabilizeIDs(&previous, next)
	if want := []string{"table_flag_defaults", "table_why"}; !slices.Equal(ids(got), want) {
		t.Errorf("IDs = %v, want %v", ids(got), want)
	}
	if got.Layout.Entries[0].Tile != "table_flag_defaults" || got.Notes.Why["table_flag_defaults"] != "Leads." || got.Notes.Moves[0].Tile != "table_flag_defaults" {
		t.Errorf("the layout and notes are not renamed: %+v %+v", got.Layout.Entries, got.Notes)
	}
	if next.Promotions[0].ID != "table_settings" {
		t.Error("StabilizeIDs changed its input")
	}
	// The composed versions keep the promoted tile's ID and subject.
	before, err := tiles.Compose(doc, &previous)
	if err != nil {
		t.Fatal(err)
	}
	stable, _ := tiles.NormalizeComposition(doc, &got)
	after, err := tiles.Compose(doc, &stable)
	if err != nil {
		t.Fatal(err)
	}
	if failures := tilestest.CheckStability(before, after); len(failures) > 0 {
		t.Errorf("stabilised: %v", failures)
	}
	unstable, _ := tiles.NormalizeComposition(doc, next)
	moved, err := tiles.Compose(doc, &unstable)
	if err != nil {
		t.Fatal(err)
	}
	if failures := tilestest.CheckStability(before, moved); len(failures) == 0 {
		t.Error("CheckStability passes a promotion that changed its ID")
	}
	// A tile whose ID the previous version gave a promotion of another
	// change is renamed.
	other := tiles.StabilizeIDs(&previous, &tiles.Composition{Promotions: []tiles.Promotion{{ID: "table_flag_defaults", Change: "sec_retry_policy", Block: 4, Title: "Delays"}}})
	if want := []string{"table_flag_defaults_2"}; !slices.Equal(ids(other), want) {
		t.Errorf("IDs = %v, want %v", ids(other), want)
	}
	if got := tiles.StabilizeIDs(nil, next); !slices.Equal(ids(got), ids(*next)) {
		t.Error("without a previous composition the IDs changed")
	}
}

// A previous tile ID the next version gives another subject fails
// CheckStability.
func TestCheckStabilitySubjects(t *testing.T) {
	_, doc := convert(t, exhibits)
	compose := func(c tiles.Composition) *tiles.Document {
		n, _ := tiles.NormalizeComposition(doc, &c)
		composed, err := tiles.Compose(doc, &n)
		if err != nil {
			t.Fatal(err)
		}
		return composed
	}
	promoted := compose(tiles.Composition{Promotions: []tiles.Promotion{{ID: "table_x", Change: "sec_config_flag", Block: 3, Title: "X"}}})
	authored := compose(tiles.Composition{Authored: []tiles.AuthoredTile{tilestest.AuthoredTile("table_x", "h_000000000003")}})
	library := compose(tiles.Composition{Library: []tiles.LibraryTile{tilestest.LibraryTile("retry_budget", "h_000000000001")}})
	if failures := tilestest.CheckStability(promoted, authored); len(failures) == 0 {
		t.Error("a promoted tile that became authored under the same ID passes")
	}
	for _, pair := range [][2]*tiles.Document{{promoted, promoted}, {authored, authored}, {library, library}, {library, promoted}} {
		if failures := tilestest.CheckStability(pair[0], pair[1]); len(failures) > 0 {
			t.Errorf("stable versions fail: %v", failures)
		}
	}
}

// Normalising and applying a composition runs on every read of a generated
// review, so it stays cheap on the largest fixture.
func TestComposeCost(t *testing.T) {
	_, doc := convert(t, withManyBlocks())
	c := tilestest.RandomComposition(rand.New(rand.NewPCG(7, 7)), doc, true)
	content := &tiles.Composition{
		Library:    []tiles.LibraryTile{tilestest.LibraryTile("retry_budget", "h_000000000001")},
		Promotions: manyPromotions(t)[:3],
		Authored:   []tiles.AuthoredTile{tilestest.AuthoredTile("prose_why", "h_000000000002"), tilestest.AuthoredTile("table_parts", "h_000000000100")},
	}
	template, _ := tiles.Template(doc, content)
	bound := 20 * time.Millisecond
	if tiles.RaceEnabled {
		bound = 200 * time.Millisecond
	}
	for _, tc := range []struct {
		name string
		c    *tiles.Composition
	}{{"hostile", c}, {"template", &template}} {
		best := time.Hour
		for range 5 {
			start := time.Now()
			n, _ := tiles.NormalizeComposition(doc, tc.c)
			if _, err := tiles.Compose(doc, &n); err != nil {
				t.Fatal(err)
			}
			best = min(best, time.Since(start))
		}
		t.Logf("%s: normalise and compose in %v", tc.name, best)
		if best > bound {
			t.Errorf("%s: normalise and compose took %v, more than %v", tc.name, best, bound)
		}
	}
}

func TestParseComposition(t *testing.T) {
	_, doc := convert(t, exhibits)
	n, _ := tiles.Template(doc, nil)
	parsed, err := tiles.ParseComposition(mustJSON(t, n))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mustJSON(t, parsed), mustJSON(t, n)) {
		t.Error("the parsed composition differs")
	}
	if _, err := tiles.ParseComposition([]byte(`{"schema": "baum.composition/2", "layout": {"entries": []}}`)); err == nil {
		t.Error("another schema parsed")
	}
	if _, err := tiles.ParseComposition([]byte(`{"schema": "baum.composition/1", "layout": {"entries": []}, "later": true}`)); err != nil {
		t.Errorf("a member from a later version is refused: %v", err)
	}
}

// Compose refuses a document that is not a converted one, and
// NormalizeComposition reports it.
func TestComposeNeedsConvertedDocument(t *testing.T) {
	_, doc := convert(t, exhibits)
	n, _ := tiles.Template(doc, nil)
	composed, err := tiles.Compose(doc, &n)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tiles.Compose(composed, &n); err == nil {
		t.Error("a generated document was composed again")
	}
	if _, report := tiles.NormalizeComposition(composed, &n); len(report.Fixes) != 1 || report.Fixes[0].Kind != tiles.FixUnusable {
		t.Errorf("fixes = %v", report.Fixes)
	}
}
