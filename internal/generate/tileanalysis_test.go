package generate_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/tiles"
)

// concurrentStub is the stub backend saying its calls may share the run
// directory, as Claude Code's and Codex's do: the tile analysis runs only
// then.
type concurrentStub struct{ *generatetest.Backend }

func (concurrentStub) ConcurrentCalls() bool { return true }

// firstHunk is the first hunk of the run's bundle.
func firstHunk(t *testing.T, dir string) bundle.Hunk {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "bundle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var b bundle.Bundle
	if err := json.Unmarshal(raw, &b); err != nil || len(b.Diff) == 0 {
		t.Fatalf("bundle: %v", err)
	}
	return b.Diff[0]
}

// analysisReplies answers the stages as StagedReplies does, the outline
// selecting the schema change type on the first hunk, its fill with a table
// cited there, and the exhibits call with exhibits (none when empty). fill,
// when set, is held until it is closed, so drafts can be seen with the tile
// pending.
func analysisReplies(t *testing.T, exhibits string, fill chan struct{}) func(context.Context, agent.Request, int) (agent.Result, error) {
	return generatetest.StagedReplies(func(stage string, request agent.Request) (agent.Result, error, bool) {
		h := firstHunk(t, request.Directory)
		switch {
		case stage == agent.StageOutline:
			document, err := generatetest.Document(request.Directory, generatetest.TwoSections)
			if err != nil {
				return agent.Result{}, err, true
			}
			var outline map[string]any
			_ = json.Unmarshal(generatetest.ForStage(request, document), &outline)
			outline["library"] = []map[string]any{
				{"type": "schema_change", "applies": true, "hunks": []string{h.ID}, "why": "adds the widgets table"},
				{"type": "api_change", "applies": false, "hunks": []string{}, "why": nil},
			}
			outline["exhibits"] = []map[string]any{}
			if exhibits != "" {
				outline["exhibits"] = []map[string]any{{"kind": "table", "gives": "the order of the steps", "hunks": []string{h.ID}}}
			}
			encoded, _ := json.Marshal(outline)
			return agent.Result{Document: encoded, Model: "stub-model", Calls: 1, Tokens: 100}, nil, true
		case request.Stage == "fill-schema-change":
			if fill != nil {
				<-fill
			}
			data := fmt.Sprintf(`{"engine":"sqlite","migrations":[{"kind":"embedded","label":"store.Open","applies":"on_start","reversible":null,"idempotent":true,"cites":[{"type":"hunk","id":%[1]q}]}],"tables":[{"name":"widgets","change":"created","renamed_from":null,"columns":[{"name":"id","change":"added","before":null,"after":"INTEGER PRIMARY KEY","destructive":false,"note":null}],"indexes":[],"cites":[{"type":"hunk","id":%[1]q}]}],"data_changes":[],"readers":[]}`, h.ID)
			return agent.Result{Document: []byte(`{"applies":true,"data":` + data + `}`), Model: "stub-model", Calls: 1, Tokens: 100}, nil, true
		case request.Stage == agent.StageExhibits:
			if exhibits == "" {
				exhibits = "[]"
			}
			exhibits = strings.ReplaceAll(exhibits, "HUNK", h.ID)
			return agent.Result{Document: []byte(`{"exhibits":` + exhibits + `}`), Model: "stub-model", Calls: 1, Tokens: 100}, nil, true
		}
		return agent.Result{}, nil, false
	})
}

// With tile reviews on, a library type the outline selects is filled beside
// the details, shown pending in drafts in its template place and then
// filled in that place, and stored with the composition, with the review's
// related items and served as a custom tile of its built-in definition. An
// exhibit that restates the review is dropped; another is placed.
func TestTilesAnalysisReachesTheComposition(t *testing.T) {
	f := newFixture(t)
	release := make(chan struct{})
	exhibits := `[{"id":"table_order","title":"Order of the steps","adds":"The order the code runs them in.","table":{"columns":["Step","Runs when"],"rows":[["open the widgets store","the daemon starts its first job"],["close the widgets store","the last job of the run has ended"]],"source":[{"type":"hunk","id":"HUNK","lines":null,"path":null}]},"code":null,"prose":null}]`
	f.backend.RunFunc = analysisReplies(t, exhibits, release)
	f.backend.CompleteFunc = keepTemplate(t, "", nil)
	o := tilesOptions(t, f)
	o.Backend = concurrentStub{f.backend}
	o.Budgets.MaxCalls = 0
	var mu sync.Mutex
	var drafts []generate.Draft
	sawPending := false
	o.OnDraft = func(d generate.Draft) {
		mu.Lock()
		defer mu.Unlock()
		drafts = append(drafts, d)
		if c, err := tiles.ParseComposition(d.Composition); err == nil {
			for _, lib := range c.Library {
				if lib.Pending {
					if !sawPending {
						sawPending = true
						close(release)
					}
				}
			}
		}
	}
	_, _, result := publishTiles(t, f, o)
	if !sawPending {
		t.Fatal("no draft showed the library tile pending")
	}
	c := storedComposition(t, result.Composition)
	if len(c.Library) != 1 || c.Library[0].Pending || !strings.Contains(string(c.Library[0].Definition), `"name":"schema_change"`) {
		t.Fatalf("library tiles: %+v", c.Library)
	}
	if len(c.Authored) != 1 || c.Authored[0].ID != "table_order" {
		t.Errorf("authored tiles: %+v", c.Authored)
	}
	report := result.Compose.Tiles
	if report == nil || len(report.Fills) != 1 || report.Fills[0].State != agent.FillReady {
		t.Fatalf("report: %+v", report)
	}
	// The draft layouts keep the library tile's place: pending, then ready.
	places := map[string]bool{}
	for _, d := range drafts {
		c, err := tiles.ParseComposition(d.Composition)
		if err != nil || len(c.Library) == 0 {
			continue
		}
		for i, entry := range c.Layout.Entries {
			if entry.Tile == "schema_change" {
				places[fmt.Sprint(i, entry.Size)] = true
			}
		}
	}
	if len(places) != 1 {
		t.Errorf("the library tile moved between drafts: %v", places)
	}
	served := tileview.Build(result.Document, result.Composition, tiles.Options{})
	var tile *tiles.Tile
	for i := range served.Tiles.Tiles {
		if served.Tiles.Tiles[i].ID == "schema_change" {
			tile = &served.Tiles.Tiles[i]
		}
	}
	if tile == nil || tile.State != tiles.StateReady || tile.Type != tiles.TypeCustom || strings.Contains(string(tile.Definition), "<script") {
		t.Fatalf("served library tile: %+v", tile)
	}
	if result.Outcome.Calls != len(f.backend.Requests)+len(f.backend.Completions) {
		t.Errorf("the run counts %d calls for %d requests and %d completions", result.Outcome.Calls, len(f.backend.Requests), len(f.backend.Completions))
	}
}

// A new version of an unchanged pull request reuses the previous version's
// library and authored tiles without a call: Publish hands the analysis the
// latest version's composition.
func TestTilesAnalysisReusesThePreviousVersion(t *testing.T) {
	f := newFixture(t)
	exhibits := `[{"id":"table_order","title":"Order of the steps","adds":"The order the code runs them in.","table":{"columns":["Step","Runs when"],"rows":[["open the widgets store","the daemon starts its first job"],["close the widgets store","the last job of the run has ended"]],"source":[{"type":"hunk","id":"HUNK","lines":null,"path":null}]},"code":null,"prose":null}]`
	f.backend.RunFunc = analysisReplies(t, exhibits, nil)
	f.backend.CompleteFunc = keepTemplate(t, "", nil)
	o := tilesOptions(t, f)
	o.Backend = concurrentStub{f.backend}
	o.Budgets.MaxCalls = 0
	s, key, first := publishTiles(t, f, o)
	if c := storedComposition(t, first.Composition); len(c.Library) != 1 || len(c.Authored) != 1 {
		t.Fatalf("first version: %d library, %d authored", len(c.Library), len(c.Authored))
	}
	fills := func() int {
		n := 0
		for _, r := range f.backend.Requests {
			if r.Stage == "fill-schema-change" {
				n++
			}
		}
		return n
	}
	before := fills()
	second, err := generate.Publish(context.Background(), s, key, o, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fills() != before {
		t.Errorf("the second version made %d fill calls", fills()-before)
	}
	report := second.Compose.Tiles
	if len(report.Fills) != 1 || !report.Fills[0].Reused || report.Exhibits == nil || len(report.Exhibits.Reused) != 1 {
		t.Fatalf("reuse: %+v / %+v", report.Fills, report.Exhibits)
	}
	if c := storedComposition(t, second.Composition); len(c.Library) != 1 || len(c.Authored) != 1 || c.Authored[0].ID != "table_order" {
		t.Errorf("second version: %+v", c.Authored)
	}
}
