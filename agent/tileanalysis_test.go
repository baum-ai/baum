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

	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/compose"
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
	return fmt.Sprintf(`{"engine":"sqlite","migrations":[{"kind":"embedded","label":"store.Open","applies":"on_start","reversible":null,"idempotent":true,"cites":[{"type":"hunk","id":%[1]q,"lines":[1,3]}]}],"tables":[{"name":"%[2]s","change":"created","renamed_from":null,"columns":[{"name":"id","change":"added","before":null,"after":"INTEGER PRIMARY KEY","destructive":false,"note":null}],"indexes":[],"cites":[{"type":"hunk","id":%[1]q}]}],"data_changes":[],"readers":[]}`, testHunk, "widgets")
}

// tileFixture is the staged fixture over a concurrent backend with the tile
// analysis on (the schema change type offered, exhibits asked for). answer
// replies to the analysis's calls (fill or exhibits; the outline's library
// array is selection) before the defaults: the outline selects the schema
// type on the parser's hunk, the fill writes schemaFill and the exhibits
// call writes none.
// withCandidate is a tileFixture selection of the schema type and one
// exhibit candidate, so the exhibits call runs.
func withCandidate() string {
	return fmt.Sprintf(`{"library":[{"type":"schema_change","applies":true,"hunks":[%[1]q],"why":"adds the widgets table"}],"exhibits":[{"kind":"table","gives":"every retry state and what moves it on","hunks":[%[1]q]}]}`, testHunk)
}

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
			part := `"library":` + selection + `,"exhibits":[]`
			if strings.HasPrefix(selection, "{") {
				part = strings.TrimSuffix(strings.TrimPrefix(selection, "{"), "}")
			}
			outline = strings.TrimSuffix(outline, "}") + `,` + part + `}`
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
	generator, run, backend := tileFixture(t, withCandidate(), nil)
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
	// The outline comes under a heading of its own, after the type's
	// instructions, never under the type's heading.
	p := fill[0].Prompt
	typeAt, headingAt, outlineAt := strings.Index(p, "## The tile type: "), strings.Index(p, fillOutlineHeading), strings.Index(p, "Accepted outline")
	if typeAt < 0 || headingAt < typeAt || outlineAt < headingAt {
		t.Error("the outline is not under its own heading after the type's instructions")
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

	generator, off, offBackend := tileFixture(t, "", nil)
	off.Tiles = nil
	outcome, _, err := generator.StagedReview(context.Background(), off)
	if err != nil {
		t.Fatal(err)
	}
	got := callsOf(offBackend, stagedOutline)[0].Prompt
	gotSchema, _ := os.ReadFile(filepath.Join(off.Directory, TaskSchemaFile(StageOutline)))
	if outcome.Tiles != nil || len(callsOf(offBackend, tilesFillText)) > 0 {
		t.Error("a run without tiles ran the analysis")
	}
	// The run directories differ, so compare from the stage text on.
	cut := func(s string) string { _, after, _ := strings.Cut(s, stagedOutline); return after }
	if cut(got) != cut(want) || string(gotSchema) != string(wantSchema) {
		t.Error("the outline's prompt or schema changed without the analysis")
	}
}

// A backend that runs one call at a time still selects and fills, the fills
// after the context, one by one; it is offered no exhibits.
func TestTileAnalysisInTurn(t *testing.T) {
	generator, run, backend := tileFixture(t, withCandidate(), nil)
	generator.Backend = backend // not concurrent
	outcome, _, err := generator.StagedReview(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Tiles == nil || len(outcome.Tiles.Fills) != 1 || outcome.Tiles.Fills[0].State != FillReady || outcome.Tiles.Exhibits != nil {
		t.Fatalf("tiles: %+v", outcome.Tiles)
	}
	var order []string
	for _, call := range backend.calls {
		switch {
		case strings.Contains(call.Prompt, tilesFillText):
			order = append(order, "fill")
		case strings.Contains(call.Prompt, "WRITE-EXHIBITS"):
			order = append(order, "exhibits")
		default:
			order = append(order, stageOf(t, call.Prompt))
		}
	}
	if strings.Join(order, ",") != "outline,details,context,fill" {
		t.Errorf("calls: %v", order)
	}
	schema, _ := os.ReadFile(filepath.Join(run.Directory, TaskSchemaFile(StageOutline)))
	if strings.Contains(string(schema), `"exhibits"`) {
		t.Error("the outline was asked for exhibit candidates")
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

// With call or token budgets the review's stages keep exactly the shares
// they have without tiles; the extras get only what those shares leave, each
// at least a call and a useful token budget, else nothing.
func TestTileAnalysisBudgets(t *testing.T) {
	for total := 1; total <= 24; total++ {
		run := Budgets{MaxCalls: total, MaxTokens: total * 10000}
		calls, tokens := total-1, run.MaxTokens-5000 // what the outline left
		details, context := stageBudgets(run, calls, tokens, true)
		for n := 1; n <= 3; n++ {
			each := extraBudgets(run, details, context, calls, tokens, n)
			leftCalls := calls - details.MaxCalls - context.MaxCalls
			leftTokens := tokens - details.MaxTokens - context.MaxTokens
			if leftCalls/n >= 1 && each.MaxCalls != leftCalls/n || leftCalls/n < 1 && each.MaxCalls != -1 {
				t.Errorf("%d calls, %d extras: each gets %d calls, %d left", total, n, each.MaxCalls, leftCalls)
			}
			if leftTokens/n >= minExtraTokens && each.MaxTokens != leftTokens/n || leftTokens/n < minExtraTokens && each.MaxTokens != -1 {
				t.Errorf("%d tokens, %d extras: each gets %d", tokens, n, each.MaxTokens)
			}
		}
	}
	if e := extraBudgets(Budgets{}, Budgets{}, Budgets{}, 0, 0, 3); e.MaxCalls != 0 || e.MaxTokens != 0 {
		t.Error("unbounded budgets bounded the extras")
	}

	// Through a run: the details and the context make their calls as without
	// tiles, and the fill, left nothing, makes none.
	generator, run, backend := tileFixture(t, "", nil)
	run.Budgets.MaxCalls = 4 // outline 1, details 2, context 1: nothing for the extras
	outcome, _, err := generator.StagedReview(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if len(callsOf(backend, tilesFillText)) != 0 || outcome.Tiles.Fills[0].Reason != FillDroppedBudget {
		t.Errorf("fill with no budget: %+v", outcome.Tiles.Fills)
	}
	if len(callsOf(backend, stagedDetails)) != 1 || len(callsOf(backend, stagedContext)) != 1 || outcome.Calls > 4 {
		t.Errorf("the stages' calls changed: %d calls", outcome.Calls)
	}
}

// A fill still running near the run's deadline is stopped in time for the
// finished review to be published; it never costs the review.
func TestTileAnalysisNearTheDeadline(t *testing.T) {
	generator, run, _ := tileFixture(t, "", func(ctx context.Context, kind string, _ Request, _ int) (Result, error, bool) {
		if kind != "fill" {
			return Result{}, nil, false
		}
		<-ctx.Done()
		return Result{}, ctx.Err(), true
	})
	run.Tiles.Grace = 10 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	outcome, document, err := generator.StagedReview(ctx, run)
	if err != nil || document == nil {
		t.Fatalf("the review was lost to its fill: %v", err)
	}
	if f := outcome.Tiles.Fills[0]; f.State != FillDropped || f.Reason != FillDroppedStopped {
		t.Errorf("fill: %+v", f)
	}
}

// A fill or exhibits call that panics is dropped and the review goes on.
func TestTileAnalysisPanics(t *testing.T) {
	generator, run, _ := tileFixture(t, withCandidate(), func(_ context.Context, kind string, _ Request, _ int) (Result, error, bool) {
		panic("a " + kind + " bug")
	})
	outcome, document, err := generator.StagedReview(context.Background(), run)
	if err != nil || document == nil {
		t.Fatal(err)
	}
	if f := outcome.Tiles.Fills[0]; f.State != FillDropped || f.Reason != FillDroppedPanic {
		t.Errorf("fill: %+v", f)
	}
	if x := outcome.Tiles.Exhibits; x == nil || x.Reason != FillDroppedPanic || len(x.Tiles) != 0 {
		t.Errorf("exhibits: %+v", x)
	}
}

type failingTilePrompts struct{}

func (failingTilePrompts) TileAnalysisInstructions(context.Context, string) (TileAnalysisPrompt, error) {
	return TileAnalysisPrompt{}, errors.New("no texts")
}

// A tile analysis that cannot start leaves the review as it is without one.
func TestTileAnalysisThatCannotStart(t *testing.T) {
	generator, run, backend := tileFixture(t, "", nil)
	run.Tiles.Prompts = failingTilePrompts{}
	outcome, document, err := generator.StagedReview(context.Background(), run)
	if err != nil || document == nil || outcome.Tiles != nil || len(callsOf(backend, tilesFillText)) != 0 {
		t.Fatalf("%v %+v", err, outcome.Tiles)
	}
}

// Cutting the tile parts out of the outline keeps the rest's bytes, so the
// details prompt is the same with tiles on and off but for the checks.
func TestTileAnalysisKeepsTheOutlineBytes(t *testing.T) {
	reply := []byte(`{"library":[1], "schema":"baum.review/1","a":"<b>","exhibits":[] ,"z":{"y":1}}`)
	got, parts := splitOutline(reply)
	if string(got) != `{ "schema":"baum.review/1","a":"<b>" ,"z":{"y":1}}` || !strings.Contains(string(parts), `"library":[1]`) {
		t.Errorf("split: %s / %s", got, parts)
	}
	if got, _ := splitOutline([]byte(`{"a":1}`)); string(got) != `{"a":1}` {
		t.Errorf("an outline without tile parts changed: %s", got)
	}

	plain, off, offBackend := stagedFixture(t, nil, nil)
	if _, _, err := plain.StagedReview(context.Background(), off); err != nil {
		t.Fatal(err)
	}
	generator, on, onBackend := tileFixture(t, `[{"type":"schema_change","applies":false,"hunks":[],"why":null}]`, nil)
	on.Tiles.Exhibits = false
	if _, _, err := generator.StagedReview(context.Background(), on); err != nil {
		t.Fatal(err)
	}
	cut := func(s string) string { _, after, _ := strings.Cut(s, stagedDetails); return after }
	if cut(callsOf(onBackend, stagedDetails)[0].Prompt) != cut(callsOf(offBackend, stagedDetails)[0].Prompt) {
		t.Error("the details prompt differs with tiles on")
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
	// Three candidates leave room for three exhibits.
	three := strings.Replace(withCandidate(), `"exhibits":[`, `"exhibits":[{"kind":"code","gives":"the call that opens the store","hunks":["`+testHunk+`"]},{"kind":"prose","gives":"the order a rollout must follow","hunks":["`+testHunk+`"]},`, 1)
	generator, run, _ := tileFixture(t, three, func(_ context.Context, kind string, _ Request, _ int) (Result, error, bool) {
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

// Without a candidate from the outline there is no exhibits call; with one
// the call gets the outline, the candidate and its hunks, not the bundle.
func TestTileAnalysisNoExhibits(t *testing.T) {
	generator, run, backend := tileFixture(t, "", nil)
	outcome, _, err := generator.StagedReview(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	x := outcome.Tiles.Exhibits
	if x == nil || x.Called || x.Candidates != 0 || len(callsOf(backend, "WRITE-EXHIBITS")) != 0 {
		t.Errorf("exhibits without a candidate: %+v", x)
	}
	generator, run, backend = tileFixture(t, withCandidate(), nil)
	if outcome, _, err = generator.StagedReview(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if x := outcome.Tiles.Exhibits; !x.Called || x.Candidates != 1 || x.Returned != 0 {
		t.Errorf("exhibits: %+v", x)
	}
	calls := callsOf(backend, "WRITE-EXHIBITS")
	if len(calls) != 1 || calls[0].Stage != StageExhibits || !strings.Contains(calls[0].Prompt, "Accepted outline") || !strings.Contains(calls[0].Prompt, "1. table: every retry state") {
		t.Fatal("the exhibits call was not made with the outline and its candidate")
	}
	if strings.Contains(calls[0].Prompt, "Input bundle") || strings.Index(calls[0].Prompt, "Accepted outline") > strings.Index(calls[0].Prompt, "## Submitting") {
		t.Error("the exhibits call carries the bundle, or the outline after its Submitting heading")
	}
	if strings.Contains(callsOf(backend, tilesFillText)[0].Prompt, "Input bundle") {
		t.Error("a fill carries the whole bundle")
	}
}

// A previous version's fill and authored tile are reused without a call
// when the code they cite is unchanged; a fill citing a hunk this diff no
// longer has is written again.
func TestTileAnalysisReuse(t *testing.T) {
	entry, _ := library.Get("schema_change")
	previous := func(data string) *tiles.Composition {
		return &tiles.Composition{Schema: tiles.CompositionName,
			Library: []tiles.LibraryTile{{Definition: entry.Encoded, Data: json.RawMessage(data)}},
			Authored: []tiles.AuthoredTile{{ID: "table_states", Title: "States", Data: json.RawMessage(
				`{"columns":["a","b"],"rows":[["x","y"]],"source":[{"type":"hunk","id":"` + testHunk + `"}]}`)}}}
	}
	withRelated := strings.TrimSuffix(schemaFill(), "}") + `,"related":[{"type":"risk","id":"rsk_old"}]}`
	generator, run, backend := tileFixture(t, "", nil)
	run.Tiles.Previous = previous(withRelated)
	outcome, _, err := generator.StagedReview(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	f := outcome.Tiles.Fills[0]
	if !f.Reused || f.State != FillReady || strings.Contains(string(f.Data), "related") || len(callsOf(backend, tilesFillText)) != 0 {
		t.Errorf("fill not reused: %+v", f)
	}
	x := outcome.Tiles.Exhibits
	if x.Called || len(x.Tiles) != 1 || x.Tiles[0].ID != "table_states" || len(x.Reused) != 1 {
		t.Errorf("authored tile not kept: %+v", x)
	}

	generator, run, backend = tileFixture(t, "", nil)
	run.Tiles.Previous = previous(strings.ReplaceAll(schemaFill(), testHunk, "h_000000000000"))
	run.Tiles.Previous.Authored[0].Data = json.RawMessage(strings.ReplaceAll(string(run.Tiles.Previous.Authored[0].Data), testHunk, "h_000000000000"))
	outcome, _, err = generator.StagedReview(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if f := outcome.Tiles.Fills[0]; f.Reused || f.State != FillReady || len(callsOf(backend, tilesFillText)) != 1 {
		t.Errorf("changed code was not filled again: %+v", f)
	}
	if x := outcome.Tiles.Exhibits; len(x.Tiles) != 0 {
		t.Errorf("an authored tile citing changed code was kept: %+v", x)
	}
}

// An exhibit's file citations name files of the diff only: a path that
// leaves the checkout, an absolute one, a directory, a symbolic link and a
// checkout file the diff does not touch are dropped.
func TestTileAnalysisExhibitFiles(t *testing.T) {
	file := func(path string) string {
		return fmt.Sprintf(`{"type":"file","id":null,"lines":null,"path":%q}`, path)
	}
	cites := strings.Join([]string{file("../bundle.json"), file("/etc/passwd"), file("dir"), file("link.go"), file("other.go"), file("main.go"),
		`{"type":"hunk","id":"` + testHunk + `","lines":null,"path":null}`}, ",")
	exhibit := `{"id":"prose_files","title":"Files","adds":"x","table":null,"code":null,"prose":{"text":"The store opens first.","anchors":[` + cites + `]}}`
	generator, run, _ := tileFixture(t, withCandidate(), func(_ context.Context, kind string, _ Request, _ int) (Result, error, bool) {
		if kind != "exhibits" {
			return Result{}, nil, false
		}
		return stagedResult(`{"exhibits":[`+exhibit+`]}`, "s"), nil, true
	})
	head := filepath.Join(run.Directory, "head")
	if err := os.WriteFile(filepath.Join(head, "other.go"), []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(head, "dir"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(run.Directory, "bundle.json"), filepath.Join(head, "link.go")); err != nil {
		t.Fatal(err)
	}
	outcome, _, err := generator.StagedReview(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	x := outcome.Tiles.Exhibits
	if len(x.Tiles) != 1 {
		t.Fatalf("exhibits: %+v", x)
	}
	data := string(x.Tiles[0].Data)
	for _, bad := range []string{"bundle.json", "/etc/passwd", `"dir"`, "link.go", "other.go"} {
		if strings.Contains(data, bad) {
			t.Errorf("a citation of %s was kept: %s", bad, data)
		}
	}
	if !strings.Contains(data, `"path":"main.go"`) {
		t.Errorf("the checkout's file was dropped: %s", data)
	}
}

// The composition places only built-in library types' tiles.
func TestComposePlacesOnlyBuiltinLibraryTiles(t *testing.T) {
	entry, _ := library.Get("schema_change")
	var def map[string]any
	_ = json.Unmarshal(entry.Encoded, &def)
	def["title"] = "Not quite"
	other, _ := json.Marshal(def)
	r := ComposeRequest{Fills: []compose.Fill{
		{Type: "schema_change", Tile: &tiles.LibraryTile{Definition: entry.Encoded, Data: json.RawMessage(schemaFill())}},
		{Type: "schema_change", Tile: &tiles.LibraryTile{Definition: other, Data: json.RawMessage(schemaFill())}},
	}}
	if lib, _ := r.extras(); len(lib) != 1 || string(lib[0].Definition) != string(entry.Encoded) {
		t.Errorf("library tiles: %d", len(lib))
	}
}

// savedLibraryEntry is the schema change type saved under another name,
// with each text at its longest, so it is not built in.
func savedLibraryEntry(t *testing.T, name string) library.Entry {
	t.Helper()
	entry, _ := library.Get("schema_change")
	def := entry.Definition
	def.Name = name
	def.Title = strings.Repeat("T", 400)
	def.UseWhen = strings.Repeat("use when ", 300)
	encoded, err := library.Seal(&def)
	if err != nil {
		t.Fatal(err)
	}
	entry.Definition, entry.Encoded = def, encoded
	entry.Glance = strings.Repeat("g", library.MaxGlance)
	return entry
}

// The outline lists a saved type marked as the person's, with its glance,
// each text bounded: six saved types add less than 4 KB to the outline's
// prompt, whatever their texts. A built-in type's line is unchanged.
func TestOfferedTypesAreBounded(t *testing.T) {
	builtin, _ := library.Get("schema_change")
	a := &tileAnalysis{config: StagedTiles{Library: []library.Entry{builtin}}, prompt: TileAnalysisPrompt{Select: tilesSelectText}}
	base := a.outlinePrompt("OUTLINE\n## Submitting\n")
	if !strings.Contains(base, "- `schema_change` (Schema changes): "+builtin.Definition.UseWhen+"\n") {
		t.Errorf("the built-in type's line changed: %s", base)
	}
	for i := range 6 {
		a.config.Library = append(a.config.Library, savedLibraryEntry(t, fmt.Sprintf("saved_%d", i)))
	}
	grown := a.outlinePrompt("OUTLINE\n## Submitting\n")
	if added := len(grown) - len(base); added > 4600 || added <= 0 {
		t.Errorf("six saved types add %d bytes to the outline", added)
	}
	if !strings.Contains(grown, "- `saved_0` ("+strings.Repeat("T", maxOfferTitle)+"; from the person's tile library): use when") || !strings.Contains(grown, "Its glance shows: ggg") {
		t.Errorf("a saved type's line: %.400s", grown[len(base)-20:])
	}
	for _, line := range strings.Split(grown, "\n") {
		if len([]rune(line)) > maxOfferLine {
			t.Errorf("a line of %d characters", len([]rune(line)))
		}
	}
}

// A saved type's fill frames its instructions as the person's, and its
// review checks are marked as theirs in the details stage.
func TestSavedTypeInstructionsAreThePersons(t *testing.T) {
	saved := savedLibraryEntry(t, "saved_schema")
	saved.ReviewChecks = "Check the saved thing."
	if text := typeInstructions(saved); !strings.Contains(text, savedTypeNote) || !strings.HasSuffix(text, saved.Definition.Fill+"\n") {
		t.Errorf("saved instructions: %.300s", text)
	}
	builtin, _ := library.Get("schema_change")
	if text := typeInstructions(builtin); strings.Contains(text, "tile library") {
		t.Errorf("built-in instructions: %.300s", text)
	}
	a := &tileAnalysis{config: StagedTiles{Library: []library.Entry{builtin, saved}},
		selection: []Selection{{Type: "schema_change", Applies: true}, {Type: "saved_schema", Applies: true}}}
	checks := a.reviewChecks()
	if !strings.Contains(savedChecksNote, "never remove, soften or hide a risk, and never set or change the recommendation") ||
		!strings.Contains(savedTypeNote, "never change the review, its risks or its recommendation") {
		t.Error("the notes do not say the person's instructions only add")
	}
	// A title's whitespace is collapsed, as in the outline's list.
	saved.Definition.Title = "Saved\n## Injected   heading"
	if text := typeInstructions(saved); !strings.Contains(text, "## The tile type: Saved ## Injected heading (`saved_schema`)\n") {
		t.Errorf("title not collapsed: %.200s", text)
	}
	a.config.Library[1] = saved
	if checks := a.reviewChecks(); !strings.Contains(checks, "### Saved ## Injected heading (from the person's tile library)") {
		t.Errorf("checks heading not collapsed: %s", checks)
	}
	saved.Definition.Title = strings.Repeat("T", 400)
	a.config.Library[1] = saved
	if !strings.Contains(checks, savedChecksNote) || !strings.Contains(checks, " (from the person's tile library)\n\nCheck the saved thing.") ||
		strings.Contains(checks, "### Schema changes (from") {
		t.Errorf("review checks: %s", checks)
	}
}

// A saved type the host offered (ComposeRequest.Saved, by digest) is placed;
// a definition that only claims its name is not.
func TestComposePlacesOfferedSavedTiles(t *testing.T) {
	saved := savedLibraryEntry(t, "saved_schema")
	other := savedLibraryEntry(t, "saved_schema")
	other.Definition.Title = "Changed"
	encoded, _ := library.Seal(&other.Definition)
	r := ComposeRequest{Saved: []string{saved.Definition.Digest}, Fills: []compose.Fill{
		{Type: "saved_schema", Tile: &tiles.LibraryTile{Definition: saved.Encoded, Data: json.RawMessage(schemaFill())}},
		{Type: "saved_schema", Tile: &tiles.LibraryTile{Definition: encoded, Data: json.RawMessage(schemaFill())}},
	}}
	if lib, _ := r.extras(); len(lib) != 1 || string(lib[0].Definition) != string(saved.Encoded) {
		t.Errorf("library tiles: %d", len(lib))
	}
}
