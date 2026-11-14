package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/tiles/library"
)

// The tile analysis test texts: markers the stub reads back.
const (
	tilesSelectText   = "SELECT-LIBRARY-TYPES\n"
	tilesFillText     = "FILL-A-LIBRARY-TILE\n"
	tilesExhibitsText = "WRITE-EXHIBITS\n## Submitting\nsubmit the exhibits\n"
)

type staticTilePrompts struct{}

func (staticTilePrompts) TileAnalysisInstructions(context.Context, string) (TileAnalysisPrompt, error) {
	return TileAnalysisPrompt{Version: "tiles-test-v1", Digest: "d", Select: tilesSelectText, Fill: tilesFillText,
		FillSubmit: "## Submitting\nsubmit the answer\n", Exhibits: tilesExhibitsText}, nil
}

// The schema change data the fills write: one table created, cited on the
// parser's hunk.
func schemaFill() string {
	return fmt.Sprintf(`{"engine":"sqlite","summary":"Adds the %[2]s table.","migrations":[{"kind":"embedded","label":"store.Open","applies":"on_start","reversible":null,"idempotent":true,"cites":[{"type":"hunk","id":%[1]q,"lines":[1,3]}]}],"tables":[{"name":"%[2]s","change":"created","renamed_from":null,"columns":[{"name":"id","change":"added","before":null,"after":"INTEGER PRIMARY KEY","destructive":false,"note":null}],"indexes":[],"cites":[{"type":"hunk","id":%[1]q}]}],"data_changes":[],"readers":[]}`, testHunk, "widgets")
}

// tileFixture is the staged fixture over a concurrent backend with the tile
// analysis on (the schema change type offered, exhibits asked for). answer
// replies to the analysis's calls (fill or exhibits; the outline's library
// array is selection) before the defaults: the outline selects the schema
// type on the parser's hunk, the fill writes schemaFill and the exhibits
// call writes none.
func tileFixture(t *testing.T, selection string, answer func(ctx context.Context, kind string, request Request, n int) (Result, error, bool)) (*Generator, StagedRun, *stubBackend) {
	t.Helper()
	generator, run, backend := stagedFixture(t, nil, nil)
	run.Budgets.MaxCalls, run.Budgets.MaxTokens = 0, 0
	if err := os.WriteFile(filepath.Join(run.Directory, "head", "main.go"), []byte("package main\n\nfunc main() {\n\topen()\n}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	entry, ok := library.Get("schema_change")
	if !ok {
		t.Fatal("no schema_change type")
	}
	run.Tiles = &StagedTiles{Prompts: staticTilePrompts{}, Library: []library.Entry{entry}, Exhibits: true, Grace: 5 * time.Second}
	if selection == "" {
		selection = fmt.Sprintf(`[{"type":"schema_change","applies":true,"hunks":[%q],"why":"adds the widgets table"}]`, testHunk)
	}
	backend.run = func(ctx context.Context, request Request, n int) (Result, error) {
		kind := ""
		switch {
		case strings.Contains(request.Prompt, tilesFillText):
			kind = "fill"
		case strings.Contains(request.Prompt, "WRITE-EXHIBITS"):
			kind = "exhibits"
		}
		if answer != nil && kind != "" {
			if result, err, answered := answer(ctx, kind, request, n); answered {
				return result, err
			}
		}
		switch kind {
		case "fill":
			return stagedResult(`{"applies":true,"data":`+schemaFill()+`}`, "session-fill"), nil
		case "exhibits":
			return stagedResult(`{"exhibits":[]}`, "session-exhibits"), nil
		}
		stage := stageOf(t, request.Prompt)
		if stage == StageOutline {
			outline := string(stagedOutlineDocument())
			outline = strings.TrimSuffix(outline, "}") + `,"library":` + selection + `}`
			return stagedResult(outline, "session-outline"), nil
		}
		return stagedReply(t, stage, request), nil
	}
	generator.Backend = concurrentBackend{backend}
	return generator, run, backend
}

func callsOf(backend *stubBackend, marker string) []Request {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	var out []Request
	for _, call := range backend.calls {
		if strings.Contains(call.Prompt, marker) {
			out = append(out, call)
		}
	}
	return out
}

// With the analysis on, the outline is asked about the library types before
// its Submitting heading, with a library array in its schema, which is taken
// out before the outline is prepared; the selected type is filled beside the
// details and the context, the details are told the type's checks, and the
// outcome counts every call once.
func TestTileAnalysisSelectsAndFills(t *testing.T) {
	generator, run, backend := tileFixture(t, "", nil)
	outcome, document, err := generator.StagedReview(context.Background(), run)
	if err != nil || outcome.Reason != "" {
		t.Fatalf("%v %+v", err, outcome.Outcome)
	}
	completesStaged(t, run, document)
	outline := callsOf(backend, stagedOutline)[0]
	if !strings.Contains(outline.Prompt, tilesSelectText) || !strings.Contains(outline.Prompt, "- `schema_change` (Schema changes): ") {
		t.Errorf("the outline prompt does not ask about the library: %.300s", outline.Prompt)
	}
	schema, err := os.ReadFile(filepath.Join(run.Directory, TaskSchemaFile(StageOutline)))
	if err != nil || !strings.Contains(string(schema), `"library"`) {
		t.Errorf("the outline schema has no library array: %v", err)
	}
	if strings.Contains(string(document), `"library"`) {
		t.Error("the review holds the library selection")
	}
	tiles := outcome.Tiles
	if tiles == nil || len(tiles.Selection) != 1 || !tiles.Selection[0].Applies || tiles.Selection[0].Why != "adds the widgets table" {
		t.Fatalf("selection: %+v", tiles)
	}
	if len(tiles.Fills) != 1 || tiles.Fills[0].State != FillReady || !json.Valid(tiles.Fills[0].Data) {
		t.Fatalf("fills: %+v", tiles.Fills)
	}
	if tiles.Exhibits == nil || tiles.Exhibits.Returned != 0 {
		t.Errorf("exhibits: %+v", tiles.Exhibits)
	}
	details := callsOf(backend, stagedDetails)
	if len(details) != 1 || !strings.Contains(details[0].Prompt, "## Checks for what this pull request changes") || !strings.Contains(details[0].Prompt, "### Schema changes") {
		t.Error("the details stage was not told the selected type's checks")
	}
	fill := callsOf(backend, tilesFillText)
	if len(fill) != 1 || fill[0].Stage != "fill-schema-change" || !strings.Contains(fill[0].Prompt, "Hunks the outline chose for this tile: "+testHunk) {
		t.Errorf("fill call: %+v", fill)
	}
	if outcome.Calls != 5 {
		t.Errorf("the run counts %d calls, want 5 (outline, details, context, fill, exhibits)", outcome.Calls)
	}
}

// Without the analysis, and for a backend that runs one call at a time, the
// outline's prompt and schema are exactly as before and nothing else runs.
func TestTileAnalysisOffLeavesTheOutlineAlone(t *testing.T) {
	plain, run, backend := stagedFixture(t, nil, nil)
	if _, _, err := plain.StagedReview(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	want := callsOf(backend, stagedOutline)[0].Prompt
	wantSchema, _ := os.ReadFile(filepath.Join(run.Directory, TaskSchemaFile(StageOutline)))

	generator, tiled, tiledBackend := tileFixture(t, "", nil)
	generator.Backend = tiledBackend // one call at a time: no analysis
	outcome, _, err := generator.StagedReview(context.Background(), tiled)
	if err != nil {
		t.Fatal(err)
	}
	got := callsOf(tiledBackend, stagedOutline)[0].Prompt
	gotSchema, _ := os.ReadFile(filepath.Join(tiled.Directory, TaskSchemaFile(StageOutline)))
	if outcome.Tiles != nil || len(callsOf(tiledBackend, tilesFillText)) > 0 {
		t.Error("a backend that runs one call at a time ran the analysis")
	}
	// The run directories differ, so compare from the stage text on.
	cut := func(s string) string { _, after, _ := strings.Cut(s, stagedOutline); return after }
	if cut(got) != cut(want) || string(gotSchema) != string(wantSchema) {
		t.Error("the outline's prompt or schema changed without the analysis")
	}
}

// A fill whose call fails, or whose data stays invalid after its one repair,
// is dropped; one repaired in time is kept; one that answers the type does
// not apply is not a tile. The review succeeds whatever the fills do.
func TestTileAnalysisFillOutcomes(t *testing.T) {
	cases := map[string]struct {
		answer func(n int) (Result, error)
		state  string
		reason string
		calls  int
	}{
		"provider failure": {func(int) (Result, error) { return Result{}, errors.New("boom") }, FillDropped, FillDroppedFailed, 0},
		"invalid twice": {func(int) (Result, error) {
			return stagedResult(`{"applies":true,"data":{"engine":"sqlite"}}`, "s"), nil
		}, FillDropped, FillDroppedInvalid, 2},
		"not applies": {func(int) (Result, error) { return stagedResult(`{"applies":false,"data":null}`, "s"), nil }, FillNotApplies, "", 1},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			generator, run, backend := tileFixture(t, "", func(_ context.Context, kind string, _ Request, n int) (Result, error, bool) {
				if kind != "fill" {
					return Result{}, nil, false
				}
				result, err := c.answer(n)
				return result, err, true
			})
			outcome, document, err := generator.StagedReview(context.Background(), run)
			if err != nil || outcome.Reason != "" || document == nil {
				t.Fatalf("the review failed with its fill: %v %+v", err, outcome.Outcome)
			}
			f := outcome.Tiles.Fills[0]
			if f.State != c.state || f.Reason != c.reason || f.Data != nil {
				t.Errorf("fill: %+v", f)
			}
			if c.calls > 0 && len(callsOf(backend, tilesFillText)) != c.calls {
				t.Errorf("fill calls: %d, want %d", len(callsOf(backend, tilesFillText)), c.calls)
			}
		})
	}
	t.Run("repaired", func(t *testing.T) {
		generator, run, backend := tileFixture(t, "", func(_ context.Context, kind string, request Request, n int) (Result, error, bool) {
			if kind != "fill" || repairing(request.Prompt) {
				return Result{}, nil, false
			}
			// A hunk that is not in the diff.
			return stagedResult(`{"applies":true,"data":`+strings.ReplaceAll(schemaFill(), testHunk, "h_000000000000")+`}`, "s"), nil, true
		})
		outcome, _, err := generator.StagedReview(context.Background(), run)
		if err != nil {
			t.Fatal(err)
		}
		f := outcome.Tiles.Fills[0]
		if f.State != FillReady || !f.Repaired {
			t.Errorf("fill: %+v", f)
		}
		repair := callsOf(backend, tilesFillText)[1].Prompt
		if !strings.Contains(repair, "names a hunk that is not in the diff") {
			t.Error("the repair does not carry the host's problems")
		}
	})
}

// A selection names only offered types and hunks of the diff: a type
// answered with no hunk of the diff, or not answered, does not apply and
// gets no fill.
func TestTileAnalysisSelectionIsLenient(t *testing.T) {
	generator, run, backend := tileFixture(t, `[{"type":"schema_change","applies":true,"hunks":["h_000000000000"],"why":"x"}]`, nil)
	outcome, _, err := generator.StagedReview(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if s := outcome.Tiles.Selection; len(s) != 1 || s[0].Applies || s[0].Hunks != nil || s[0].Why != "" {
		t.Errorf("selection: %+v", s)
	}
	if len(callsOf(backend, tilesFillText)) != 0 || len(outcome.Tiles.Fills) != 0 {
		t.Error("an unselected type was filled")
	}
	if strings.Contains(callsOf(backend, stagedDetails)[0].Prompt, "## Checks for what") {
		t.Error("the details were told the checks of a type that does not apply")
	}
}

// Fills run beside the details and the context; once those end, a fill
// still running gets the grace period and is then stopped and dropped,
// without failing or holding up the review further.
func TestTileAnalysisStopsSlowFills(t *testing.T) {
	var stopped atomic.Bool
	generator, run, _ := tileFixture(t, "", func(ctx context.Context, kind string, request Request, _ int) (Result, error, bool) {
		if kind != "fill" {
			return Result{}, nil, false
		}
		<-ctx.Done()
		stopped.Store(true)
		return Result{}, context.Canceled, true
	})
	run.Tiles.Grace = 50 * time.Millisecond
	started := time.Now()
	outcome, document, err := generator.StagedReview(context.Background(), run)
	if err != nil || document == nil {
		t.Fatal(err)
	}
	if time.Since(started) > 5*time.Second || !stopped.Load() {
		t.Error("the slow fill held the review up")
	}
	if f := outcome.Tiles.Fills[0]; f.State != FillDropped || f.Reason != FillDroppedStopped {
		t.Errorf("fill: %+v", f)
	}
}

// Cancelling the run cancels the fills too, and the run returns its error.
func TestTileAnalysisCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	generator, run, _ := tileFixture(t, "", func(ctx context.Context, kind string, request Request, _ int) (Result, error, bool) {
		if kind != "fill" {
			return Result{}, nil, false
		}
		cancel()
		<-ctx.Done()
		return Result{}, context.Canceled, true
	})
	if _, _, err := generator.StagedReview(ctx, run); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
}

// With call budgets, the details keep their share; the context's is split
// with the extras, and an extra left with nothing makes no call.
func TestTileAnalysisBudgets(t *testing.T) {
	contextShare, each := extraBudgets(Budgets{MaxCalls: 12}, Budgets{MaxCalls: 4, MaxTokens: 0}, 2)
	if contextShare.MaxCalls != 2 || each.MaxCalls != 1 {
		t.Errorf("split: context %d, each %d", contextShare.MaxCalls, each.MaxCalls)
	}
	contextShare, each = extraBudgets(Budgets{MaxCalls: 12}, Budgets{MaxCalls: 1}, 2)
	if contextShare.MaxCalls != 1 || each.MaxCalls != -1 {
		t.Errorf("exhausted split: context %d, each %d", contextShare.MaxCalls, each.MaxCalls)
	}
	if c, e := extraBudgets(Budgets{}, Budgets{}, 3); c.MaxCalls != 0 || e.MaxCalls != 0 {
		t.Error("unbounded budgets were split")
	}

	generator, run, backend := tileFixture(t, "", nil)
	run.Budgets.MaxCalls = 4 // outline 1, details 2, context 1: nothing for the extras
	outcome, _, err := generator.StagedReview(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if len(callsOf(backend, tilesFillText)) != 0 || outcome.Tiles.Fills[0].Reason != FillDroppedBudget {
		t.Errorf("fill with no budget: %+v", outcome.Tiles.Fills)
	}
	if outcome.Calls > 4 {
		t.Errorf("the run made %d calls on a budget of 4", outcome.Calls)
	}
}

// A strict reply's null lines are dropped before the data is checked.
func TestTileAnalysisStrictNullLines(t *testing.T) {
	strict := strings.Replace(schemaFill(), `"id":"`+testHunk+`"}`, `"id":"`+testHunk+`","lines":null}`, 1)
	if got := string(dropNullLines(json.RawMessage(strict))); strings.Contains(got, "null}") && strings.Contains(got, `"lines":null`) {
		t.Errorf("null lines kept: %s", got)
	}
	generator, run, _ := tileFixture(t, "", func(_ context.Context, kind string, _ Request, _ int) (Result, error, bool) {
		if kind != "fill" {
			return Result{}, nil, false
		}
		return stagedResult(`{"applies":true,"data":`+strict+`}`, "s"), nil, true
	})
	outcome, _, err := generator.StagedReview(context.Background(), run)
	if err != nil || outcome.Tiles.Fills[0].State != FillReady {
		t.Fatalf("%v %+v", err, outcome.Tiles.Fills)
	}
}

// The exhibits call's tiles pass the host's checks against the bundle or are
// dropped with their reason: a hunk not in the diff, code that is not the
// cited code, a shape other than the ID's, a restatement of an earlier
// exhibit, and any past three. Lines outside the hunk are dropped, citing
// the whole hunk.
func TestTileAnalysisExhibits(t *testing.T) {
	cite := func(id string, lines string) string {
		return fmt.Sprintf(`{"type":"hunk","id":%q,"lines":%s,"path":null}`, id, lines)
	}
	table := func(id, cell string, source string) string {
		return fmt.Sprintf(`{"id":%q,"title":"Retry states","adds":"Every state and what moves it on.","table":{"columns":["State","On failure"],"rows":[["idle","%s"],["running","waits ten seconds before it tries again"]],"source":[%s]},"code":null,"prose":null}`, id, cell, source)
	}
	exhibits := []string{
		table("table_states", "starts a run", cite(testHunk, "[1,3]")),
		`{"id":"code_open","title":"Where the store opens","adds":"The call that opens it.","table":null,"code":{"language":"go","content":"func main() {\n\t// …\n\topen()\n}","caption":"The store opens first.","source":[` + cite(testHunk, "[40,50]") + `]},"prose":null}`,
		`{"id":"prose_order","title":"Rollout order","adds":"The order.","table":null,"code":null,"prose":{"text":"Deploy the reader before the writer.","anchors":[` + cite(testHunk, "null") + `]}}`,
		table("table_ghost", "starts a run", cite("h_000000000000", "null")),
		`{"id":"code_made_up","title":"Code","adds":"x","table":null,"code":{"language":"go","content":"func invented() {\n\treturn nil\n}","caption":"","source":[` + cite(testHunk, "null") + `]},"prose":null}`,
		`{"id":"table_wrong","title":"Wrong","adds":"x","table":null,"code":null,"prose":{"text":"Hello.","anchors":[` + cite(testHunk, "null") + `]}}`,
		table("table_again", "starts a run", cite(testHunk, "null")),
		`{"id":"table_fourth","title":"Client versions","adds":"x","table":{"columns":["Client","Works"],"rows":[["desktop seven","yes"],["browser extension two","no"]],"source":[` + cite(testHunk, "null") + `]},"code":null,"prose":null}`,
	}
	generator, run, _ := tileFixture(t, "", func(_ context.Context, kind string, _ Request, _ int) (Result, error, bool) {
		if kind != "exhibits" {
			return Result{}, nil, false
		}
		return stagedResult(`{"exhibits":[`+strings.Join(exhibits, ",")+`]}`, "s"), nil, true
	})
	outcome, _, err := generator.StagedReview(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	x := outcome.Tiles.Exhibits
	var kept []string
	for _, tile := range x.Tiles {
		kept = append(kept, tile.ID)
	}
	if x.Returned != 8 || strings.Join(kept, ",") != "table_states,code_open,prose_order" {
		t.Fatalf("returned %d, kept %v", x.Returned, kept)
	}
	want := []DroppedExhibit{{"table_ghost", ExhibitCitation}, {"code_made_up", ExhibitCode}, {"table_wrong", ExhibitShape}, {"table_again", ExhibitDuplicate}, {"table_fourth", ExhibitLimit}}
	if fmt.Sprint(x.Dropped) != fmt.Sprint(want) {
		t.Errorf("dropped %v, want %v", x.Dropped, want)
	}
	if !strings.Contains(string(x.Tiles[0].Data), `"lines":[1,3]`) || strings.Contains(string(x.Tiles[1].Data), `"lines"`) {
		t.Errorf("citations: %s / %s", x.Tiles[0].Data, x.Tiles[1].Data)
	}
	if x.Adds["table_states"] != "Every state and what moves it on." {
		t.Errorf("adds: %v", x.Adds)
	}
}

// An exhibits call that returns none is the common answer: nothing is kept,
// nothing dropped.
func TestTileAnalysisNoExhibits(t *testing.T) {
	generator, run, backend := tileFixture(t, "", nil)
	outcome, _, err := generator.StagedReview(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	x := outcome.Tiles.Exhibits
	if x.Returned != 0 || len(x.Tiles) != 0 || len(x.Dropped) != 0 || x.Reason != "" {
		t.Errorf("exhibits: %+v", x)
	}
	calls := callsOf(backend, "WRITE-EXHIBITS")
	if len(calls) != 1 || calls[0].Stage != StageExhibits || !strings.Contains(calls[0].Prompt, "Accepted outline") {
		t.Error("the exhibits call was not made with the outline")
	}
	if strings.Index(calls[0].Prompt, "Accepted outline") > strings.Index(calls[0].Prompt, "## Submitting") {
		t.Error("the outline comes after the exhibits call's Submitting heading")
	}
}
