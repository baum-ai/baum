package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/reviewv2"
)

const (
	hunkA = "h_aaaaaaaaaaaa"
	hunkB = "h_bbbbbbbbbbbb"
	hunkC = "h_cccccccccccc"
	hunkD = "h_dddddddddddd"
)

func testBundle() *bundle.Bundle {
	return &bundle.Bundle{
		Repository:  "acme/jobs",
		PullRequest: bundle.PullRequest{Number: 7, Title: "Retry failed jobs", Body: "Adds retries."},
		Diff: []bundle.Hunk{
			{ID: hunkA, Path: "queue/retry.go", Role: "code", Change: "modified", StartLine: 10, LineCount: 20},
			{ID: hunkB, Path: "queue/deadletter.go", Role: "code", Change: "added", StartLine: 1, LineCount: 40},
			{ID: hunkC, Path: "api/requeue.go", Role: "code", Change: "modified", StartLine: 5, LineCount: 10},
			{ID: hunkD, Path: "go.sum", Role: "generated", Change: "modified", StartLine: 1, LineCount: 3},
		},
	}
}

// fake is a backend whose replies are chosen by the call's label (its
// Stage), recording when each call ran and how many ran at once.
type fake struct {
	mu         sync.Mutex
	replies    map[string]func(pass int) string
	delay      time.Duration
	concurrent bool
	sessions   bool
	inFlight   atomic.Int32
	maxFlight  atomic.Int32
	starts     map[string]time.Time
	ends       map[string]time.Time
	requests   map[string]agent.Request
}

func newFake() *fake {
	return &fake{replies: map[string]func(int) string{}, concurrent: true, sessions: true,
		starts: map[string]time.Time{}, ends: map[string]time.Time{}, requests: map[string]agent.Request{}}
}

func (f *fake) Name() string          { return agent.BackendClaude }
func (f *fake) ConcurrentCalls() bool { return f.concurrent }
func (f *fake) KeepsSessions() bool   { return f.sessions }

func (f *fake) Run(ctx context.Context, r agent.Request) (agent.Result, error) {
	n := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		m := f.maxFlight.Load()
		if n <= m || f.maxFlight.CompareAndSwap(m, n) {
			break
		}
	}
	f.mu.Lock()
	if _, ok := f.starts[r.Stage]; !ok {
		f.starts[r.Stage] = time.Now()
	}
	f.requests[r.Stage] = r
	reply := f.replies[r.Stage]
	f.mu.Unlock()
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		return agent.Result{}, ctx.Err()
	}
	defer func() {
		f.mu.Lock()
		f.ends[r.Stage] = time.Now()
		f.mu.Unlock()
	}()
	if reply == nil {
		return agent.Result{Calls: 1}, errors.New("no reply for " + r.Stage)
	}
	return agent.Result{Document: []byte(reply(r.Pass)), Calls: 1, Tokens: 100, SessionID: "11111111-2222-3333-4444-555555555555"}, nil
}

func (f *fake) reply(stage, text string) { f.replies[stage] = func(int) string { return text } }

type prompts struct{}

func (prompts) ReviewV2Instructions(context.Context, string) (agent.ReviewV2Prompt, error) {
	return agent.ReviewV2Prompt{Version: "test", Shared: "SHARED", Map: "MAP", Section: "SECTION", Context: "CONTEXT", Tile: "TILE",
		Natives: map[string]string{"flow": "FLOW", "table": "TABLE", "columns": "COLUMNS", "sequence": "SEQUENCE"}, Overview: "OVERVIEW"}, nil
}

const mapReplyJSON = `{"change_kind":"feature","sections":[
 {"id":"sec_retry","title":"Retry failed jobs","kind":"logical","hunks":["` + hunkA + `","` + hunkB + `"],"gist":"Failed jobs retry with a growing wait.","scope":null,"level":3,
  "level_reasons":[{"text":"Every job goes through the retry path.","cites":[{"type":"hunk","id":"` + hunkA + `","lines":null}]}]},
 {"id":"sec_api","title":"Who may requeue","kind":"logical","hunks":["` + hunkC + `"],"gist":"A new endpoint requeues dead jobs.","scope":null,"level":4,
  "level_reasons":[{"text":"The endpoint reads the role from a header.","cites":[{"type":"hunk","id":"` + hunkC + `","lines":null}]}]},
 {"id":"sec_deps","title":"Dependency sums","kind":"mechanical","hunks":["` + hunkD + `"],"gist":"Updated sums.","scope":null,"level":null,"level_reasons":[]}],
 "tiles":[
 {"id":"tl_path","type":"flow","owner":"overview","shows":"The path of a failed job.","hunks":["` + hunkA + `"]},
 {"id":"tl_backoff","type":"table","owner":"sec_retry","shows":"How long a job waits.","hunks":["` + hunkA + `"]},
 {"id":"tl_check","type":"code","owner":"sec_api","shows":"The role check.","hunks":["` + hunkC + `"]}]}`

func sectionReplyJSON(tiles string) string {
	return `{"summary":"Failed jobs now retry with a growing wait. After the last attempt they move to a table of dead jobs.",
 "steps":[{"title":"The worker catches a failure","text":"A failed handler schedules a retry.","cites":[{"type":"hunk","id":"` + hunkA + `","lines":null}]},
  {"title":"The wait doubles","text":"Each attempt waits twice as long as the last.","cites":[]}],
 "risks":[{"id":"rsk_unbounded","tier":"medium","category":"correctness","text":"If the cap is zero the wait never grows, so jobs retry at once.","cites":[{"type":"hunk","id":"` + hunkA + `","lines":[12,14]}]}],
 "tiles":` + tiles + `,"propose":null}`
}

const flowReply = `{"type":"flow","title":"What happens to a failed job","caption":null,"source":["` + hunkA + `"],
 "lanes":[{"label":null,"steps":[{"title":"Worker runs the job","note":null,"tone":null,"join":null},{"title":"Wait and retry","note":null,"tone":"new","join":"arrow"}]}]}`

const tableReply = `{"type":"table","title":"How long a job waits","caption":"The wait doubles.","source":["` + hunkA + `"],
 "columns":["Attempt","Wait"],"rows":[["1","Base"],["2","Twice the base"]]}`

const contextReplyJSON = `{"area":{"text":"The job queue runs background work.","files":["queue/retry.go"]},"reached_by":{"text":"","files":[]},"contracts":{"text":"","files":[]},"related":{"text":"","files":[]}}`

const overviewReplyJSON = `{"summary":"Failed jobs now retry and land in a dead-letter table. A new endpoint requeues them.",
 "verdict":{"decision":"look_first","text":"Check who may requeue before merging.","points":[{"text":"The endpoint trusts a header any caller sets.","section":"sec_api"}]},
 "risk_why":"Every job goes through the new retry path.","screenshots":["nope"],"diagram":"tl_path","intent":[]}`

func fullFake() *fake {
	f := newFake()
	f.reply("map", mapReplyJSON)
	f.reply("section-sec-retry", sectionReplyJSON(`[{"id":"tl_backoff","shows":"How long each attempt waits.","cites":[{"type":"hunk","id":"`+hunkA+`","lines":null}],"drop":false,"reason":null}]`))
	f.reply("section-sec-api", sectionReplyJSON(`[{"id":"tl_check","shows":"The role check","cites":[{"type":"hunk","id":"`+hunkC+`","lines":[6,9]}],"drop":false,"reason":null}]`))
	f.reply("context", contextReplyJSON)
	f.reply("tile-tl-path", flowReply)
	f.reply("tile-tl-backoff", tableReply)
	f.reply("overview", overviewReplyJSON)
	return f
}

func testRun(t *testing.T, f *fake) Run {
	t.Helper()
	return Run{
		Generator: &agent.Generator{Backend: f}, Bundle: testBundle(), Directory: t.TempDir(), Prompts: prompts{},
		Budgets:  agent.Budgets{MaxProviderAttempts: 1, MaxRepairAttempts: 1},
		Registry: NewRegistry(nil),
	}
}

func TestGenerateWritesEveryLevel(t *testing.T) {
	f := fullFake()
	f.delay = 5 * time.Millisecond
	run := testRun(t, f)
	var steps []string
	run.Step = func(s string) { steps = append(steps, s) }
	var drafts atomic.Int32
	run.Draft = func(reviewv2.Document) { drafts.Add(1) }
	var calls []Call
	run.Record = func(c Call) { calls = append(calls, c) }
	out, doc, err := Generate(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Schema != reviewv2.Name || doc.Summary.ChangeKind != "feature" || doc.Summary.Title != "Retry failed jobs" {
		t.Fatalf("header: %+v", doc.Summary)
	}
	if len(doc.Sections) != 3 {
		t.Fatalf("sections: %d", len(doc.Sections))
	}
	for _, s := range doc.Sections {
		if s.Details != "" || s.Summary == "" {
			t.Errorf("section %s: details %q summary %q", s.ID, s.Details, s.Summary)
		}
	}
	if s := doc.Sections[2]; s.Kind != "mechanical" || !strings.Contains(s.Summary, "lockfiles") {
		t.Errorf("mechanical summary: %q", s.Summary)
	}
	if doc.Sections[0].Steps[0].ID != "stp_retry_1" {
		t.Errorf("step id: %q", doc.Sections[0].Steps[0].ID)
	}
	byID := map[string]reviewv2.Tile{}
	for _, tile := range doc.Tiles {
		byID[tile.ID] = tile
		if tile.State != reviewv2.TileReady {
			t.Errorf("tile %s: %s", tile.ID, tile.State)
		}
	}
	if code := byID["tl_check"]; string(code.Data) != `{"hunk":"`+hunkC+`","lines":[6,9]}` || code.Title != "The role check" {
		t.Errorf("code tile: %s %q", code.Data, code.Title)
	}
	if byID["tl_backoff"].Shows != "How long each attempt waits." {
		t.Errorf("the section did not sharpen the brief: %q", byID["tl_backoff"].Shows)
	}
	if doc.Summary.Verdict == nil || len(doc.Summary.Verdict.Points) != 1 || doc.Overview.Diagram == nil || *doc.Overview.Diagram != "tl_path" {
		t.Errorf("overview: %+v %+v", doc.Summary.Verdict, doc.Overview)
	}
	if len(doc.Overview.Screenshots) != 0 {
		t.Errorf("an unknown screenshot was kept: %v", doc.Overview.Screenshots)
	}
	if doc.Context == nil || doc.Context.Area.Text == "" {
		t.Errorf("context: %+v", doc.Context)
	}
	if out.Generation.Map.Status != StatusRan || out.Generation.Overview.Status != StatusRan || out.Generation.Sections["sec_deps"].Status != StatusSkipped {
		t.Errorf("generation: %+v", out.Generation)
	}
	if want := []string{"Mapping the pull request", "Writing 2 sections", "Writing the overview"}; !slices.Equal(steps[:2], want[:2]) || steps[len(steps)-1] != want[2] {
		t.Errorf("steps: %v", steps)
	}
	if out.Levels[LevelMap].Calls != 1 || out.Levels[LevelSections].Calls != 2 || out.Levels[LevelTiles].Calls != 2 || out.Levels[LevelOverview].Calls != 1 || out.Calls != 7 {
		t.Errorf("calls by level: map %d sections %d tiles %d overview %d, total %d", out.Levels[LevelMap].Calls, out.Levels[LevelSections].Calls,
			out.Levels[LevelTiles].Calls, out.Levels[LevelOverview].Calls, out.Calls)
	}
	if drafts.Load() == 0 {
		t.Error("no draft was published")
	}
	if len(calls) != 7 {
		t.Errorf("%d calls recorded", len(calls))
	}
	// The order: each tile after its owner, the overview after every tile,
	// and the overview resumes the map's session.
	if !f.starts["tile-tl-backoff"].After(f.ends["section-sec-retry"]) {
		t.Error("the section's tile started before its section was written")
	}
	if !f.starts["tile-tl-path"].Before(f.ends["section-sec-retry"]) {
		t.Error("the overview's tile waited for a section")
	}
	for _, stage := range []string{"tile-tl-path", "tile-tl-backoff", "section-sec-retry", "section-sec-api", "context"} {
		if !f.starts["overview"].After(f.ends[stage]) {
			t.Errorf("the overview started before %s ended", stage)
		}
	}
	if !f.requests["map"].PersistSession || f.requests["overview"].ResumeSession == "" {
		t.Error("the overview did not resume the map's session")
	}
	if strings.Contains(f.requests["section-sec-retry"].Prompt, "patch") && strings.Contains(f.requests["section-sec-retry"].Prompt, "@@") {
		t.Error("a section's input carries patches")
	}
}

func TestConcurrencyIsBounded(t *testing.T) {
	f := fullFake()
	f.delay = 20 * time.Millisecond
	run := testRun(t, f)
	run.Concurrency = 2
	if _, _, err := Generate(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if got := f.maxFlight.Load(); got != 2 {
		t.Errorf("at most %d calls ran at once, want 2", got)
	}
	f = fullFake()
	f.concurrent = false
	f.delay = 5 * time.Millisecond
	run = testRun(t, f)
	if _, _, err := Generate(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if got := f.maxFlight.Load(); got != 1 {
		t.Errorf("a backend that runs one call at a time ran %d", got)
	}
}

func TestMapFailureFailsTheRun(t *testing.T) {
	f := fullFake()
	f.reply("map", `{"change_kind":"feature","sections":[{"id":"sec_x","title":"Only one","kind":"logical","hunks":["`+hunkA+`"],"gist":"x","scope":null,"level":2,"level_reasons":[{"text":"x","cites":[]}]}],"tiles":[]}`)
	_, _, err := Generate(context.Background(), testRun(t, f))
	if !errors.Is(err, ErrMapFailed) || !strings.Contains(err.Error(), "validation") {
		t.Fatalf("err = %v", err)
	}
	if f.requests["map"].Pass != 2 {
		t.Error("the map was not repaired once")
	}
	if _, ok := f.requests["context"]; ok {
		t.Error("a later level ran after the map failed")
	}
}

func TestSectionAndOverviewFailuresDoNotFailTheRun(t *testing.T) {
	f := fullFake()
	delete(f.replies, "section-sec-retry")
	delete(f.replies, "overview")
	delete(f.replies, "context")
	out, doc, err := Generate(context.Background(), testRun(t, f))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Sections[0].Details != "failed" || out.Generation.Sections["sec_retry"].Status != StatusFailed {
		t.Errorf("failed section: %q %+v", doc.Sections[0].Details, out.Generation.Sections["sec_retry"])
	}
	// Its tile is still drawn, from the map's brief.
	for _, tile := range doc.Tiles {
		if tile.ID == "tl_backoff" && (tile.State != reviewv2.TileReady || tile.Shows != "How long a job waits.") {
			t.Errorf("the failed section's tile: %+v", tile)
		}
	}
	if doc.Context != nil {
		t.Error("a failed context is published")
	}
	if doc.Summary.Text != "Failed jobs retry with a growing wait. A new endpoint requeues dead jobs." {
		t.Errorf("fallback lede: %q", doc.Summary.Text)
	}
	if v := doc.Summary.Verdict; v == nil || v.Decision != "look_first" || len(v.Points) != 0 {
		t.Errorf("fallback verdict: %+v", v)
	}
	if doc.Summary.RiskWhy != "The endpoint reads the role from a header." {
		t.Errorf("fallback risk why: %q", doc.Summary.RiskWhy)
	}
	if doc.Overview.Diagram == nil || *doc.Overview.Diagram != "tl_path" {
		t.Errorf("fallback diagram: %v", doc.Overview.Diagram)
	}
}

func TestOverviewWithoutSessionsIsAFreshCall(t *testing.T) {
	f := fullFake()
	f.sessions = false
	if _, _, err := Generate(context.Background(), testRun(t, f)); err != nil {
		t.Fatal(err)
	}
	if f.requests["map"].PersistSession || f.requests["overview"].ResumeSession != "" {
		t.Error("a backend without sessions was asked to keep one")
	}
	if !strings.Contains(f.requests["overview"].Prompt, "The map (model-written data") {
		t.Error("the fresh overview does not read the map")
	}
}

func TestUnchangedSectionsAreReused(t *testing.T) {
	f := fullFake()
	delete(f.replies, "section-sec-retry")
	delete(f.replies, "tile-tl-backoff")
	run := testRun(t, f)
	level := 3
	run.Previous = &reviewv2.Document{Schema: reviewv2.Name,
		Sections: []reviewv2.Section{{ID: "sec_retry", Kind: "logical", Hunks: []string{hunkB, hunkA}, Level: &level, Summary: "Before.",
			Steps: []reviewv2.Step{{ID: "stp_retry_1", Title: "Old", Text: "Old."}},
			Risks: []reviewv2.Risk{{ID: "rsk_old", Tier: "low", Text: "Old.", State: "active"}, {ID: "rsk_fixed", Tier: "low", Text: "Fixed.", State: "mitigated"}}}},
		Tiles: []reviewv2.Tile{{ID: "tl_old", Type: "flow", Owner: "sec_retry", State: reviewv2.TileReady, Title: "Old tile"}},
	}
	out, doc, err := Generate(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if _, called := f.requests["section-sec-retry"]; called {
		t.Error("an unchanged section was written again")
	}
	s := doc.Sections[0]
	if s.Summary != "Before." || len(s.Risks) != 1 || s.Risks[0].ID != "rsk_old" || s.Risks[0].State != "" || s.Details != "" {
		t.Errorf("reused section: %+v", s)
	}
	ids := []string{}
	for _, tile := range doc.Tiles {
		ids = append(ids, tile.ID)
	}
	if !slices.Contains(ids, "tl_old") || slices.Contains(ids, "tl_backoff") {
		t.Errorf("tiles: %v", ids)
	}
	if out.Generation.Sections["sec_retry"].Status != StatusReused || out.Generation.Tiles["tl_old"].Status != StatusReused {
		t.Errorf("generation: %+v", out.Generation)
	}
}

func TestDroppedAndProposedTiles(t *testing.T) {
	f := fullFake()
	f.reply("section-sec-retry", strings.Replace(sectionReplyJSON(`[{"id":"tl_backoff","shows":"x","cites":[],"drop":true,"reason":"It restates the steps."}]`),
		`"propose":null`, `"propose":{"type":"columns","shows":"Before and after the retry.","cites":[{"type":"hunk","id":"`+hunkB+`","lines":null}]}`, 1))
	f.reply("tile-tl-retry-more", `{"type":"columns","title":"Before and after","caption":null,"source":["`+hunkB+`"],"columns":[{"label":"Before","tone":null,"items":[{"title":"Dropped","note":null}]},{"label":"After","tone":"new","items":[{"title":"Retried","note":null}]}]}`)
	out, doc, err := Generate(context.Background(), testRun(t, f))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, tile := range doc.Tiles {
		ids = append(ids, tile.ID+":"+tile.State)
	}
	if slices.Contains(ids, "tl_backoff:ready") || !slices.Contains(ids, "tl_retry_more:ready") {
		t.Errorf("tiles: %v", ids)
	}
	if r := out.Generation.Tiles["tl_backoff"]; r.Status != StatusSkipped || !strings.Contains(r.Reason, "restates") {
		t.Errorf("dropped tile record: %+v", r)
	}
}

func TestLevelTimeShares(t *testing.T) {
	f := fullFake()
	f.replies["section-sec-retry"] = nil
	run := testRun(t, f)
	// A section that never answers is stopped at its level's share of the
	// time, and the overview still runs.
	f.delay = 0
	slow := &slowFake{fake: f, slow: "section-sec-retry"}
	run.Generator = &agent.Generator{Backend: slow}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	out, doc, err := Generate(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(started); took > 1600*time.Millisecond {
		t.Errorf("the run took %s; the sections' share ends at 65%%", took)
	}
	if doc.Sections[0].Details != "failed" || out.Generation.Overview.Status != StatusRan {
		t.Errorf("section %q, overview %+v", doc.Sections[0].Details, out.Generation.Overview)
	}
}

type slowFake struct {
	*fake
	slow string
}

func (s *slowFake) Run(ctx context.Context, r agent.Request) (agent.Result, error) {
	if r.Stage == s.slow {
		<-ctx.Done()
		return agent.Result{}, ctx.Err()
	}
	return s.fake.Run(ctx, r)
}

func TestBudgetsSplitAndFlow(t *testing.T) {
	b := newBudgets(agent.Budgets{MaxCalls: 100, MaxTokens: 1000})
	if calls, tokens := b.left(0); calls != 20 || tokens != 200 {
		t.Fatalf("map share: %d %d", calls, tokens)
	}
	took := b.take(0)
	if took.MaxCalls != 20 {
		t.Fatalf("map took %d", took.MaxCalls)
	}
	b.give(0, took, agent.Outcome{Calls: 5, Tokens: 50})
	b.close(0)
	if calls, _ := b.left(1); calls != 45+15 {
		t.Fatalf("the map's leftover did not flow: %d", calls)
	}
	b.expect(1, 3)
	first := b.take(1)
	if first.MaxCalls != 20 {
		t.Fatalf("first of three took %d", first.MaxCalls)
	}
	unlimited := newBudgets(agent.Budgets{})
	if got := unlimited.take(2); got.MaxCalls != 0 || got.MaxTokens != 0 {
		t.Fatalf("an unlimited budget became %+v", got)
	}
	spent := newBudgets(agent.Budgets{MaxCalls: 1})
	if got := spent.take(2); got.MaxCalls != -1 {
		t.Fatalf("a level with nothing left allows %d calls", got.MaxCalls)
	}
}

func TestLabels(t *testing.T) {
	for in, want := range map[string]string{"sec_retry": "section-sec-retry", "sec__x_": "section-sec-x", "": "section"} {
		if got := label("section", in); got != want {
			t.Errorf("label(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCopyRules(t *testing.T) {
	c := &checker{}
	c.text("/a", "The evidence shows it works.", 100, 0, true)
	c.text("/b", "As #123 did before.", 100, 0, true)
	c.text("/c", "It changes 40 lines.", 100, 0, true)
	c.text("/d", "Cites h_0123456789ab in text.", 100, 0, true)
	c.text("/e", "One. Two. Three.", 100, 2, true)
	c.text("/f", strings.Repeat("x", 200), 100, 0, true)
	c.title("/g", "Pairing, and revoking", 60, 5)
	c.title("/h", "A title of far too many plain words here", 60, 5)
	if len(c.findings) != 8 {
		t.Fatalf("findings: %v", c.findings)
	}
	c = &checker{}
	// A text a quarter over its limit at most is kept whole.
	if got := c.text("/ok", "A **plain** [link](http://x) text that is a bit long", 32, 1, true); len(c.findings) != 0 || got != "A plain link text that is a bit long" {
		t.Fatalf("clean: %q %v", got, c.findings)
	}
	// The last pass lets style through and cuts what is far too long.
	c = &checker{lenient: true}
	got := c.text("/last", "The evidence. Two. Three. "+strings.Repeat("y", 200), 100, 1, true)
	if len(c.findings) != 0 || !strings.HasSuffix(got, "…") {
		t.Fatalf("lenient: %q %v", got, c.findings)
	}
}

func TestCitesResolve(t *testing.T) {
	f := newFacts(testBundle(), nil)
	raw := []json.RawMessage{
		json.RawMessage(`{"type":"hunk","id":"` + hunkA + `","lines":[12,14]}`),
		json.RawMessage(`{"type":"hunk","id":"` + hunkA + `","lines":[1,99]}`),
		json.RawMessage(`{"type":"hunk","id":"h_000000000000","lines":null}`),
		json.RawMessage(`{"type":"file","path":"queue/retry.go"}`),
		json.RawMessage(`{"type":"pr_body"}`),
		json.RawMessage(`{"type":"check","id":"nope"}`),
	}
	kept, dropped := f.cites(raw)
	want := []string{`{"id":"` + hunkA + `","lines":[12,14],"type":"hunk"}`, `{"id":"` + hunkA + `","type":"hunk"}`, `{"path":"queue/retry.go","type":"file"}`, `{"type":"pr_body"}`}
	var got []string
	for _, k := range kept {
		got = append(got, string(k))
	}
	if !slices.Equal(got, want) || dropped != 2 {
		t.Fatalf("kept %v, dropped %d", got, dropped)
	}
}

func TestToolsSection(t *testing.T) {
	with := toolsSection(true)
	for _, tool := range ReviewTools {
		if !strings.Contains(with, "`"+tool.Name+"`") {
			t.Errorf("the tools section lacks %s", tool.Name)
		}
	}
	if without := toolsSection(false); strings.Contains(without, "MCP") || !strings.Contains(without, "bundle.json") {
		t.Errorf("without MCP: %s", without)
	}
	if w := workspaceSection(Worktree{Dir: "/x", Head: "abcdef0123456789", MergeBase: "0123456789abcdef"}, true); !strings.Contains(w, "git diff 0123456789ab..HEAD") || !strings.Contains(w, "shell") {
		t.Errorf("workspace: %s", w)
	}
	if w := workspaceSection(Worktree{}, false); !strings.Contains(w, "head/") || strings.Contains(w, "shell") {
		t.Errorf("workspace without a worktree: %s", w)
	}
}

func TestBudgetSpentLevelFailsItsCallsOnly(t *testing.T) {
	f := fullFake()
	run := testRun(t, f)
	run.Budgets.MaxCalls = 7
	out, doc, err := Generate(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	// 7 calls: the map 1, sections and context 3, tiles 1, overview 2.
	if out.Calls > 7 {
		t.Errorf("the run made %d calls over its budget of 7", out.Calls)
	}
	if doc.Schema != reviewv2.Name {
		t.Error("no document")
	}
	_ = fmt.Sprint(out.Generation)
}
