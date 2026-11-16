package screens_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/replay"
	"github.com/baum-ai/baum/internal/replay/screens"
)

// fakeRun writes a run directory with pictures of n cases, as just
// replay-screens leaves it: the manifest, each case's tile document and
// compose record, screens.json and per case first-screen.json and pictures
// (files whose bytes name them). The composition of every case but the
// last differs from the template.
func fakeRun(t *testing.T, root, name, model string, n int) string {
	t.Helper()
	dir := filepath.Join(root, name)
	write := func(path string, v any) {
		t.Helper()
		raw, ok := v.([]byte)
		if !ok {
			var err error
			if raw, err = json.Marshal(v); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tileDoc, err := os.ReadFile(filepath.Join("..", "..", "..", "tiles", "testdata", "example.tiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	sizes := []string{"xs", "s", "m", "l", "xl"}
	var ids []string
	for i := range n {
		id := "case-" + string(rune('a'+i))
		ids = append(ids, id)
		write(filepath.Join(dir, "cases", id, "tiles.json"), tileDoc)
		write(filepath.Join(dir, "cases", id, "document.json"), replay.ComposeRecord{Schema: replay.ComposeRecordName, DiffersFromTemplate: i < n-1})
		recall := 1.0
		first := func(variant string) map[string]any {
			return map[string]any{"width": 1440, "height": 900, "pageHeight": 1800 + i, "landingTiles": 6, "belowTiles": 2, "wastedUnits": i % 2,
				"tiles":           []map[string]any{{"id": "overview", "type": "overview", "size": "half", "visible": "full"}, {"id": "risks", "type": "risks", "size": "half", "visible": "partial"}},
				"recommendation":  map[string]any{"given": true, "visible": variant == screens.Composed},
				"severeRisks":     []map[string]any{{"id": "r1", "visible": true}},
				"namedChanges":    []map[string]any{},
				"breaking":        map[string]any{"given": false, "visible": false},
				"missingEvidence": map[string]any{"count": 2, "visible": true},
				"recall":          recall}
		}
		write(filepath.Join(dir, screens.Dir, id, "first-screen.json"), map[string]any{"case": id, "size": sizes[i%len(sizes)], "version": 2,
			"variants": map[string]any{screens.Composed: first(screens.Composed), screens.Template: first(screens.Template)},
			"phone":    map[string]any{screens.Composed: first(screens.Composed), screens.Template: first(screens.Template)},
			"issues":   []map[string]any{{"where": "template-1440-light", "what": "a <notice>"}}})
		for _, variant := range []string{screens.Composed, screens.Template} {
			for _, width := range []string{"1440", "900", "390"} {
				base := filepath.Join(dir, screens.Dir, id, variant+"-"+width+"-light")
				write(base+".png", []byte(name+"/"+variant))
				write(base+"-full.png", []byte(name+"/"+variant+"/full"))
			}
		}
	}
	write(filepath.Join(dir, "manifest.json"), replay.Manifest{Run: name, Model: model, Generator: "compose", CaseIDs: ids})
	write(filepath.Join(dir, screens.Dir, "screens.json"), map[string]any{"cases": ids, "problems": []map[string]string{{"id": "case-z", "problem": "no history"}}})
	return dir
}

func load(t *testing.T, dir string) *screens.Run {
	t.Helper()
	run, err := screens.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestLoadReadsThePicturesFacts(t *testing.T) {
	dir := fakeRun(t, t.TempDir(), "run-a", "sonnet", 3)
	run := load(t, dir)
	if len(run.Cases) != 3 || len(run.Problems) != 1 || run.Name() != "run-a" {
		t.Fatalf("%+v", run)
	}
	c, ok := run.Case("case-b")
	if !ok || c.Size != "s" || c.Variants[screens.Composed].PageHeight != 1801 || len(c.Variants[screens.Template].Tiles) != 2 || *c.Phone[screens.Composed].Recall != 1 {
		t.Fatalf("%+v", c)
	}
	if got := run.Picture("case-b", screens.Template, 390, true); filepath.Base(got) != "template-390-light-full.png" {
		t.Errorf("full picture %s", got)
	}
	if _, err := screens.Load(t.TempDir()); err == nil {
		t.Error("a directory that is not a run loaded")
	}
	bare := t.TempDir()
	raw, _ := json.Marshal(replay.Manifest{Run: "x"})
	_ = os.WriteFile(filepath.Join(bare, "manifest.json"), raw, 0o644)
	if _, err := screens.Load(bare); err == nil || !strings.Contains(err.Error(), "just replay-screens") {
		t.Errorf("a run without pictures: %v", err)
	}
}

func TestSheetShowsBothLayoutsWithTheirFacts(t *testing.T) {
	root := t.TempDir()
	run := load(t, fakeRun(t, root, "run-a", "sonnet", 3))
	p := screens.Pairs(run, nil)
	path := filepath.Join(run.Dir, screens.Dir, screens.SheetName(p))
	verdicts := map[string]screens.PairVerdict{"case-a": {Case: "case-a", Winner: screens.WinnerX, OrdersAgree: true, Reasons: []string{"leads <with> the risk"}}}
	if err := screens.WriteSheet(p, path, verdicts); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sheet := string(raw)
	for _, want := range []string{
		`src="case-a/composed-1440-light.png"`, `href="case-a/template-1440-light-full.png"`, `src="case-c/template-390-light.png"`,
		"composed (sonnet) better", "leads &lt;with&gt; the risk", "a &lt;notice&gt;", "3 of 3 (100%)", "0 of 3 (0%)", "X wins / ties / Y wins",
	} {
		if !strings.Contains(sheet, want) {
			t.Errorf("the sheet lacks %q", want)
		}
	}
	if strings.Contains(sheet, "<notice>") || strings.Contains(sheet, "<with>") {
		t.Error("text from the run is not escaped")
	}

	// Against another run: the other run's pictures by relative paths.
	other := load(t, fakeRun(t, root, "run-b", "opus", 2))
	p = screens.Pairs(run, other)
	path = filepath.Join(run.Dir, screens.Dir, screens.SheetName(p))
	if filepath.Base(path) != "sheet-vs-run-b.html" {
		t.Fatalf("sheet name %s", path)
	}
	if err := screens.WriteSheet(p, path, nil); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if !strings.Contains(string(raw), `src="../../run-b/screens/case-b/composed-1440-light.png"`) || strings.Contains(string(raw), "case-c/") {
		t.Errorf("the sheet against another run is wrong")
	}
}

// pictureJudge answers from the pictures it is shown: it prefers the
// layout named prefer, or always answers first when prefer is empty (a
// position bias), and fails on the case named failOn.
type pictureJudge struct {
	prefer, failOn string
	calls          atomic.Int32
}

func (j *pictureJudge) Complete(_ context.Context, r agent.CompleteRequest) (agent.Completion, error) {
	j.calls.Add(1)
	if len(r.Images) != 4 || r.System != screens.Rubric || !strings.Contains(r.User, "Open critical risks") {
		return agent.Completion{}, errors.New("the call lacks the pictures or the facts")
	}
	if j.failOn != "" && strings.Contains(r.Images[0].Path, j.failOn) {
		return agent.Completion{}, errors.New("overloaded")
	}
	a, _ := os.ReadFile(r.Images[0].Path)
	verdict := "A"
	if j.prefer != "" && !strings.HasSuffix(string(a), j.prefer) {
		verdict = "B"
	}
	reply, _ := json.Marshal(map[string]any{"verdict": verdict, "reason": "because"})
	return agent.Completion{Document: reply, Model: "judge-model", Usage: agent.Usage{Input: 1000, Output: 100}}, nil
}

func TestJudgeAsksBothOrdersAndCountsVerdicts(t *testing.T) {
	run := load(t, fakeRun(t, t.TempDir(), "run-a", "sonnet", 5))
	p := screens.Pairs(run, nil)
	price := func(string, agent.Usage) *float64 { v := 0.01; return &v }

	judge := &pictureJudge{prefer: screens.Composed}
	report, err := screens.Judge(context.Background(), p, screens.JudgeOptions{Completer: judge, Backend: "claude", Model: "opus", Concurrency: 3, Phone: true, Price: price,
		Repeat: 1, Seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	s := report.Summary
	if judge.calls.Load() != 20 || s.Pairs != 5 || s.WinsX != 5 || s.Ties != 0 || s.OrdersAgree != 5 || report.Repeats != 10 || report.RepeatsAgree != 10 {
		t.Fatalf("%+v %+v", s, report)
	}
	if math.Abs(s.SignP-0.0625) > 1e-9 || math.Abs(report.CostUSD-0.2) > 1e-9 || s.BySize["xs"] != [3]int{1, 0, 0} {
		t.Errorf("%+v cost %v", s, report.CostUSD)
	}

	// A judge that always prefers the first layout shown is cancelled by the
	// two orders: every pair is a tie.
	report, err = screens.Judge(context.Background(), p, screens.JudgeOptions{Completer: &pictureJudge{}, Phone: true})
	if err != nil {
		t.Fatal(err)
	}
	if s := report.Summary; s.Ties != 5 || s.OrdersAgree != 0 || s.SignP != 1 {
		t.Fatalf("%+v", s)
	}

	// Only the cases whose layouts differ: the last case's are the same.
	report, err = screens.Judge(context.Background(), p, screens.JudgeOptions{Completer: &pictureJudge{prefer: screens.Composed}, Phone: true, Differing: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := report.Pairs["case-e"]; ok || report.Summary.Pairs != 4 {
		t.Fatalf("%+v", report.Summary)
	}

	// A pair whose call failed is left out, and the run goes on.
	report, err = screens.Judge(context.Background(), p, screens.JudgeOptions{Completer: &pictureJudge{prefer: screens.Template, failOn: "case-b"}, Phone: true})
	if err != nil {
		t.Fatal(err)
	}
	if s := report.Summary; s.Pairs != 4 || s.WinsY != 4 || report.IncompletePair != 1 || report.FailedPairs[0] != "case-b" {
		t.Fatalf("%+v %+v", s, report)
	}
}

func TestSignTestAndWilson(t *testing.T) {
	for _, c := range []struct {
		wins, losses int
		want         float64
	}{{10, 0, 2.0 / 1024}, {5, 5, 1}, {0, 0, 1}, {7, 3, 0.34375}} {
		if got := screens.SignTest(c.wins, c.losses); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("SignTest(%d, %d) = %v, want %v", c.wins, c.losses, got, c.want)
		}
	}
	if low, high := screens.Wilson(8, 10); math.Abs(low-0.4902) > 1e-3 || math.Abs(high-0.9433) > 1e-3 {
		t.Errorf("Wilson(8, 10) = %v, %v", low, high)
	}
}

func TestLabelSetAndAgreement(t *testing.T) {
	run := load(t, fakeRun(t, t.TempDir(), "run-a", "sonnet", 6))
	p := screens.Pairs(run, nil)
	dir := t.TempDir()
	// Six asked, five differ: the set holds the five and says so.
	if short, err := screens.LabelSet(p, 6, 1, t.TempDir()); err != nil || len(short.Pairs) != 5 || short.Short != 1 {
		t.Errorf("asked for more pairs than differ: %v %+v", err, short)
	}
	// Another run of the same reviews adds nothing new; a run of other
	// reviews (composed from another run) does.
	root := t.TempDir()
	again := load(t, fakeRun(t, root, "run-b", "opus", 6))
	other := load(t, fakeRun(t, root, "run-c", "opus", 6))
	other.Manifest.GeneratorSettings = map[string]any{"from_run": "generated"}
	pooled, err := screens.LabelSet(p, 12, 1, t.TempDir(), screens.Pairs(again, nil), screens.Pairs(other, nil))
	if err != nil || len(pooled.Pairs) != 10 || pooled.Pairs[0].XRun == "" {
		t.Errorf("pooled: %v %+v", err, pooled)
	}
	key, err := screens.LabelSet(p, 4, 1, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(key.Pairs) != 4 || key.Pairs[0].Pair != "P01" {
		t.Fatalf("%+v", key)
	}
	seen := map[string]bool{}
	for _, pair := range key.Pairs {
		if pair.Case == "case-f" || seen[pair.Case] {
			t.Errorf("pair %+v: the same layouts, or twice", pair)
		}
		seen[pair.Case] = true
	}
	page, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil || !strings.Contains(string(page), "data:image/png;base64,") || !strings.Contains(string(page), `href="full/P01-A-1440.png"`) {
		t.Fatalf("the page is not self-contained: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "full", "P04-B-390.png")); err != nil {
		t.Error(err)
	}
	// The page names no layout, outside its style sheet and embedded
	// pictures.
	text := regexp.MustCompile(`(?s)<style>.*?</style>|data:image/png;base64,[A-Za-z0-9+/=]*`).ReplaceAllString(string(page), "")
	for _, word := range []string{"composed", "template", "run-a", "sonnet"} {
		if strings.Contains(text, word) {
			t.Errorf("the page says %q", word)
		}
	}
	readKey, err := screens.ReadKey(filepath.Join(dir, "key.json"))
	if err != nil || len(readKey.Pairs) != 4 {
		t.Fatalf("%v %+v", err, readKey)
	}

	// The owner prefers the composition each time, whichever side it was.
	var labels strings.Builder
	labels.WriteString("# labels\n\n")
	for _, pair := range key.Pairs {
		verdict := "A"
		if pair.A == screens.WinnerY {
			verdict = "b"
		}
		labels.WriteString(pair.Pair + " " + verdict + " looks right\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "labels.txt"), []byte(labels.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	// A second make keeps the owner's labels.
	if _, err := screens.LabelSet(p, 4, 1, dir); err != nil {
		t.Fatal(err)
	}
	read, err := screens.ReadLabels(filepath.Join(dir, "labels.txt"))
	if err != nil || len(read) != 4 || read["P01"].Note != "looks right" {
		t.Fatalf("%v %+v", err, read)
	}
	report := screens.Report{XRun: key.XRun, YRun: key.YRun, Pairs: map[string]screens.PairVerdict{}}
	for i, pair := range key.Pairs {
		winner := screens.WinnerX
		if i == 0 {
			winner = screens.WinnerTie
		}
		report.Pairs[pair.Case] = screens.PairVerdict{Case: pair.Case, Winner: winner}
	}
	a := screens.AgreeLabels(key, read, report)
	if a.Labelled != 4 || a.Same != 3 || a.Decisive != 3 || a.DecisiveSame != 3 {
		t.Fatalf("%+v", a)
	}
	bad := filepath.Join(t.TempDir(), "labels.txt")
	_ = os.WriteFile(bad, []byte("P01 maybe\n"), 0o600)
	if _, err := screens.ReadLabels(bad); err == nil {
		t.Error("a verdict that is not A, B or tie was read")
	}
}

// A composition the host replaced with the template when it stored it
// serves the template's page: its pair does not differ, whatever the compose
// record says.
func TestPairsWhosePagesMeasureTheSameDoNotDiffer(t *testing.T) {
	dir := fakeRun(t, t.TempDir(), "run-a", "sonnet", 3)
	path := filepath.Join(dir, screens.Dir, "case-a", "first-screen.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c map[string]any
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	c["variants"].(map[string]any)[screens.Composed] = c["variants"].(map[string]any)[screens.Template]
	c["phone"].(map[string]any)[screens.Composed] = c["phone"].(map[string]any)[screens.Template]
	raw, _ = json.Marshal(c)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	p := screens.Pairs(load(t, dir), nil)
	if p.Differ("case-a") || !p.Differ("case-b") {
		t.Errorf("case-a differs %v, case-b %v", p.Differ("case-a"), p.Differ("case-b"))
	}
}

// addSupportingTiles gives a case's tile document an authored table and a
// library tile after its overview and risks, as a composition with extras
// does, and a promoted table, which is the review's own block.
func addSupportingTiles(t *testing.T, dir, id string) {
	t.Helper()
	path := filepath.Join(dir, "cases", id, "tiles.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	extras := []map[string]any{
		{"id": "table_retry_delays", "type": "table", "title": "Retry delays by attempt", "state": "ready",
			"data": map[string]any{"columns": []string{"Attempt", "Delay"}, "rows": [][]string{{"1", "1s"}, {"2", "4s"}}, "source": []any{}}},
		{"id": "api_change", "type": "custom", "title": "Interface changes", "state": "ready",
			"data": map[string]any{"endpoints": []map[string]any{{"name": "/api/retry", "breaking": true}}, "consumers": []any{}, "related": []map[string]any{{"type": "risk", "id": "r1"}}}},
		{"id": "table_promoted", "type": "table", "title": "Promoted block", "state": "ready",
			"data": map[string]any{"columns": []string{"a"}, "rows": [][]string{}, "source": []any{}, "origin": map[string]any{"change": "sec_api", "block": 0}}},
	}
	layout := doc["layout"].(map[string]any)
	entries := layout["entries"].([]any)
	var added []any
	for _, extra := range extras {
		doc["tiles"] = append(doc["tiles"].([]any), extra)
		added = append(added, map[string]any{"tile": extra["id"], "size": "half"})
	}
	layout["entries"] = append(append(slices.Clone(entries[:2]), added...), entries[2:]...)
	if raw, err = json.Marshal(doc); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// The with-tiles rubric shows each layout's first screen and whole page, no
// phone pictures, and lists the supporting tiles each layout shows, those
// only one shows apart, each with a line on what it shows; layout-judge-v1
// stays as it was.
func TestWithTilesRubricShowsTheWholePageAndTheSupportingTiles(t *testing.T) {
	root := t.TempDir()
	with := load(t, func() string {
		dir := fakeRun(t, root, "with", "opus", 2)
		addSupportingTiles(t, dir, "case-a")
		return dir
	}())
	without := load(t, fakeRun(t, root, "without", "opus", 2))
	p := screens.Pairs(with, without)

	v1, err := screens.RubricNamed("")
	if err != nil || v1.Version != screens.RubricVersion || v1.WholePage || v1.Digest() != "702f2b9cb03115a76eb1812d9b262eaba52ae26b3b2e7c14f7553b75461bdb30" {
		t.Fatalf("layout-judge-v1 changed: %+v %v", v1.Version, err)
	}
	rubric, err := screens.RubricNamed(screens.RubricWithTilesVersion)
	if err != nil || rubric.Digest() == v1.Digest() || !json.Valid(rubric.Schema) {
		t.Fatalf("the with-tiles rubric: %v", err)
	}
	if _, err := screens.RubricNamed("layout-judge-v0"); err == nil {
		t.Error("an unknown rubric was accepted")
	}

	r, err := screens.Request(rubric, p.X, p.Y, "case-a", true)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, image := range r.Images {
		paths = append(paths, filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(image.Path))))+"/"+filepath.Base(image.Path))
	}
	want := []string{"with/composed-1440-light.png", "without/composed-1440-light.png", "with/composed-1440-light-full.png", "without/composed-1440-light-full.png"}
	if !slices.Equal(paths, want) || r.System != screens.RubricWithTiles || string(r.Schema) != string(screens.AnswerSchemaWithTiles) {
		t.Errorf("images %v", paths)
	}
	for _, text := range []string{"Image 3 is A's whole page", "the same analysis in A and B", "Open critical risks",
		"Supporting tiles only A shows (2):", "Retry delays by attempt: a table of 2 rows, columns: Attempt, Delay",
		"Interface changes: lists 1 endpoints (/api/retry); one of them is destructive or breaking"} {
		if !strings.Contains(r.User, text) {
			t.Errorf("the message lacks %q:\n%s", text, r.User)
		}
	}
	if strings.Contains(r.User, "Promoted block") || strings.Contains(r.User, "only B shows") || strings.Contains(r.User, "phone") {
		t.Errorf("the message lists a promoted block, a tile B lacks or the phone:\n%s", r.User)
	}
	// B shown first: the same tiles are B's.
	if r, err := screens.Request(rubric, p.Y, p.X, "case-a", false); err != nil || !strings.Contains(r.User, "Supporting tiles only B shows (2):") {
		t.Errorf("swapped: %v\n%s", err, r.User)
	}
	// Neither adds any.
	if r, err := screens.Request(rubric, p.X, p.Y, "case-b", false); err != nil || !strings.Contains(r.User, "neither layout adds any") {
		t.Errorf("case-b: %v\n%s", err, r.User)
	}

	// layout-judge-v1: first screens and phones, no supporting tiles.
	r, err = screens.Request(v1, p.X, p.Y, "case-a", true)
	if err != nil || len(r.Images) != 4 || !strings.HasSuffix(r.Images[2].Path, "composed-390-light.png") || strings.Contains(r.User, "Supporting") || r.System != screens.Rubric {
		t.Errorf("v1: %v %v\n%s", err, r.Images, r.User)
	}
}

// A judge run limited to listed cases judges only those, under the rubric
// it names, and the report says which.
func TestJudgeOnlyTheListedCases(t *testing.T) {
	run := load(t, fakeRun(t, t.TempDir(), "run-a", "sonnet", 4))
	p := screens.Pairs(run, nil)
	o := screens.JudgeOptions{Completer: &rubricJudge{}, Cases: []string{"case-b", "case-d", "case-x"}, Rubric: screens.RubricWithTilesVersion}
	if got := screens.Selected(p, o); !slices.Equal(got, []string{"case-b", "case-d"}) {
		t.Fatalf("selected %v", got)
	}
	report, err := screens.Judge(context.Background(), p, o)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.Pairs != 2 || report.RubricVersion != screens.RubricWithTilesVersion || report.IncompletePair != 0 {
		t.Errorf("%+v %s", report.Summary, report.RubricVersion)
	}
	if _, err := screens.Judge(context.Background(), p, screens.JudgeOptions{Completer: &rubricJudge{}, Rubric: "nope"}); err == nil {
		t.Error("an unknown rubric ran")
	}
}

// rubricJudge answers tie to a with-tiles call that shows the whole pages.
type rubricJudge struct{}

func (rubricJudge) Complete(_ context.Context, r agent.CompleteRequest) (agent.Completion, error) {
	if r.System != screens.RubricWithTiles || len(r.Images) != 4 || !strings.HasSuffix(r.Images[3].Path, "-full.png") {
		return agent.Completion{}, errors.New("not a with-tiles call")
	}
	return agent.Completion{Document: json.RawMessage(`{"verdict":"tie","reason":"same","added_tiles":[]}`)}, nil
}
