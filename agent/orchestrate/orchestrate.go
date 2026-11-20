// Package orchestrate writes a baum.review/2 document in three levels (see
// reviewv2):
//
//  1. The map: one agent call (persistent session) groups the hunks into
//     sections by behaviour, judges each section's level and plans at most
//     eight tiles. A map that fails, after its repair, fails the run.
//  2. The sections: one section agent per logical section and the context
//     agent, at once (at most Run.Concurrency calls; one at a time on a
//     backend that cannot run calls at once). The mechanical section's
//     summary is the host's. A section whose hunks are unchanged since the
//     previous baum.review/2 version keeps its text and tiles without a
//     call.
//  3. The tiles: one tile agent per planned tile, each starting as soon as
//     its owner is written (the overview's main diagram once the map is in).
//     Native types share one tile prompt with each type's part; dedicated
//     (library and saved) types go through the library's fill path
//     (agent.Generator.FillTile); code tiles are {hunk, lines}, filled by
//     the host.
//
// Then the overview, by resuming the map's session (a fresh call with the
// map in its input on a backend that cannot keep sessions). A section, the
// context, a tile or the overview failing never fails the run: the section
// is published with details "failed", the tile with state "failed", the
// context left out, and the overview falls back to the sections' gists.
//
// Every call reads a small input (the pull request's basics, the diff index
// and its own brief) and reads the rest itself: through baum's MCP server
// when the run has one (Run.MCP; the tools are ReviewTools), else in
// bundle.json and the checkout in its directory. Run.Worktree, when set, is
// a git worktree at the head the calls run in, with full history.
//
// The run's call and token budgets and its time limit are split between the
// levels (Shares), with the map given at least MinMapTime; what a level
// leaves flows to the next. Each landed part
// publishes a draft (Run.Draft), completed by Run.Complete
// (reviewv2.Complete) as the final document is.
package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/reviewv2"
	"github.com/baum-ai/baum/tiles/library"
)

// The levels, as outcomes and records name them.
const (
	LevelMap      = "map"
	LevelSections = "sections"
	LevelContext  = "context"
	LevelTiles    = "tiles"
	LevelOverview = "overview"
)

// Levels lists the levels in order.
var Levels = []string{LevelMap, LevelSections, LevelContext, LevelTiles, LevelOverview}

// Shares split a run's call and token budgets and its time limit between the
// map, the sections with the context, the tiles and the overview, in that
// order. What a level leaves flows to the next.
var Shares = [4]float64{0.20, 0.45, 0.25, 0.10}

// MinMapTime is the least time the map gets when the run's time limit
// allows it. The map reads the pull request before it plans, and a large one
// needs longer than its share of a short limit; its repair also runs in this
// time. The map still ends by the end of the sections' share, so the later
// levels keep their deadlines.
const MinMapTime = 4 * time.Minute

// DefaultConcurrency is how many calls run at once when Run.Concurrency is
// zero.
const DefaultConcurrency = 4

// The statuses of a reviewv2.CallRecord.
const (
	StatusRan     = "ran"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
	StatusReused  = "reused"
)

// CompleteFunc completes an authored document into the stored one
// (reviewv2.Complete).
type CompleteFunc func(reviewv2.Document, reviewv2.CompleteInput) (reviewv2.Document, error)

// Worktree is where the calls run: a git worktree of the person's clone at
// the pull request's head, with full history.
type Worktree struct {
	// Dir is the worktree; empty runs the calls in Run.Directory, over the
	// head/ and merge-base/ trees unpacked there.
	Dir string
	// GitDir is the clone's shared Git directory, which the calls may read
	// (git reads its objects there).
	GitDir string
	// Head and MergeBase are the commits the prompts name.
	Head, MergeBase string
}

// Run is one generation.
type Run struct {
	// Generator runs the calls: its Backend, Redactor and Logger.
	Generator *agent.Generator
	Bundle    *bundle.Bundle
	// EncodedBundle is written as bundle.json where the calls run.
	EncodedBundle []byte
	// Directory is the run directory.
	Directory string
	Worktree  Worktree
	Budgets   agent.Budgets
	Endpoint  *agent.Endpoint
	Prompts   agent.ReviewV2Prompts
	// FillPrompts are the library fill texts dedicated tiles are filled
	// with (prompt.TilesPrompts); nil fails every dedicated tile.
	FillPrompts agent.TileAnalysisPrompts
	// Registry is the tile types offered; nil is the native types and
	// library.All.
	Registry *Registry
	// Always are the dedicated types filled whatever the map answers: the
	// saved types pinned to the repository and the library types the person
	// added to the pull request's review. A type the map plans no tile of
	// gets one from the host (mandatoryTiles). A name the registry does not
	// offer as a dedicated type is ignored.
	Always []string
	// MCP is baum's MCP server, with a token scoped to the pull request;
	// nil runs the calls without it.
	MCP *agent.MCPServer
	// Shell lets the calls run commands that read (agent.Request.Shell).
	Shell bool
	// Previous is the previous version's stored document, of either schema:
	// the map keeps its sections' ids and titles, and a baum.review/2 one's
	// unchanged sections are reused (reviewv2.ReusableSections). Nil has
	// none.
	Previous []byte
	NoReuse  bool
	// Concurrency bounds the calls at once; zero is DefaultConcurrency.
	Concurrency int
	// Complete completes drafts and the final document; nil is
	// reviewv2.Complete. CompleteInput is what it is given besides.
	Complete      CompleteFunc
	CompleteInput reviewv2.CompleteInput
	// Draft receives each draft, completed, as parts land; calls come one
	// at a time from a goroutine of the run, and a draft that lands while
	// the last is delivered replaces any still waiting. Nil publishes none.
	Draft func(reviewv2.Document)
	// Step receives the level the run is at, in words ("Mapping the pull
	// request", "Writing 3 sections", "Drawing 4 tiles", "Writing the
	// overview"); calls come from the run's goroutines, one at a time.
	Step func(string)
	// Record receives every call's prompt, input and reply (replay).
	Record func(Call)
}

// Call is one pass of one call, as Run.Record receives it.
type Call struct {
	Level, ID string
	Pass      int
	// Prompt is the instructions, Input the data after them; Reply is the
	// reply, masked; Findings what the host found in it.
	Prompt, Input string
	Reply         []byte
	Findings      string
}

// LevelOutcome is what one level spent.
type LevelOutcome struct {
	agent.Outcome
	// Seconds is the level's wall time, from its first call's start to its
	// last call's end.
	Seconds float64 `json:"seconds"`
	// Ran, Failed, Reused and Skipped count its calls by status.
	Ran, Failed, Reused, Skipped int

	started, ended time.Time
}

// Outcome is what a run did. Its Outcome sums every call's.
type Outcome struct {
	agent.Outcome
	Levels                      map[string]*LevelOutcome
	PromptVersion, PromptDigest string
	Generation                  reviewv2.Generation
	// ShellOff says why the calls had no shell although asked, empty when
	// they had it or were not asked.
	ShellOff string
}

// timeNow is time.Now; tests replace it.
var timeNow = time.Now

// ErrMapFailed is a run whose map failed: the run publishes nothing.
var ErrMapFailed = errors.New("the pull request could not be mapped")

// Generate writes the document. It fails only when the map fails
// (ErrMapFailed, wrapping why) or ctx ends before it is in; afterwards it
// always returns a completed document, whatever failed.
func Generate(ctx context.Context, run Run) (Outcome, reviewv2.Document, error) {
	o := &orchestration{run: run, out: &Outcome{Levels: map[string]*LevelOutcome{}}}
	for _, level := range Levels {
		o.out.Levels[level] = &LevelOutcome{}
	}
	o.out.Generation = reviewv2.Generation{Sections: map[string]reviewv2.CallRecord{}, Tiles: map[string]reviewv2.CallRecord{}}
	if err := o.prepare(ctx); err != nil {
		return *o.out, reviewv2.Document{}, err
	}
	defer o.stopDrafts()
	if err := o.mapLevel(ctx); err != nil {
		o.finish()
		return *o.out, reviewv2.Document{}, err
	}
	o.middleLevels(ctx)
	o.overviewLevel(ctx)
	o.finish()
	doc, err := o.complete(o.document(true))
	return *o.out, doc, err
}

// orchestration is the state of one Generate.
type orchestration struct {
	run      Run
	out      *Outcome
	prompt   agent.ReviewV2Prompt
	registry *Registry
	facts    *facts
	redactor *bundle.Redactor
	dir      string
	tools    string
	started  time.Time
	deadline time.Time
	sem      chan struct{}
	budget   *budgets

	// mu guards everything below and the outcome.
	mu         sync.Mutex
	plan       plan
	sections   map[string]*reviewv2.Section
	tiles      []reviewv2.Tile
	context    *reviewv2.Context
	overview   *overviewResult
	mapSession string
	drafts     chan struct{}
	draftsDone chan struct{}
}

func (o *orchestration) prepare(ctx context.Context) error {
	run := o.run
	if run.Generator == nil || run.Bundle == nil || run.Prompts == nil {
		return errors.New("orchestrate: a run needs its generator, bundle and prompts")
	}
	o.started = time.Now()
	if d, ok := ctx.Deadline(); ok {
		o.deadline = d
	}
	backend := run.Generator.Backend.Name()
	prompt, err := run.Prompts.ReviewV2Instructions(ctx, backend)
	if err != nil {
		return err
	}
	o.prompt = prompt
	o.out.PromptVersion, o.out.PromptDigest = prompt.Version, prompt.Digest
	o.registry = run.Registry
	if o.registry == nil {
		o.registry = NewRegistry(library.All())
	}
	if o.redactor = run.Generator.Redactor; o.redactor == nil {
		if o.redactor, err = bundle.NewRedactor(); err != nil {
			return err
		}
	}
	o.dir = run.Directory
	roots := []string{filepath.Join(run.Directory, "head"), filepath.Join(run.Directory, "merge-base")}
	if run.Worktree.Dir != "" {
		o.dir = run.Worktree.Dir
		roots = []string{run.Worktree.Dir}
	}
	o.facts = newFacts(run.Bundle, roots)
	if len(run.EncodedBundle) > 0 {
		if err := os.WriteFile(filepath.Join(o.dir, "bundle.json"), run.EncodedBundle, 0o400); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	n := run.Concurrency
	if n <= 0 {
		n = DefaultConcurrency
	}
	if !agent.ConcurrentCalls(run.Generator.Backend) {
		n = 1
	}
	o.sem = make(chan struct{}, n)
	o.budget = newBudgets(run.Budgets)
	o.tools = workspaceSection(run.Worktree, run.Shell) + "\n" + toolsSection(run.MCP != nil)
	o.sections = map[string]*reviewv2.Section{}
	if run.Draft != nil {
		o.drafts, o.draftsDone = make(chan struct{}, 1), make(chan struct{})
		go o.deliverDrafts()
	}
	return nil
}

// levelIndex is the budget level of a level: the sections and the context
// share one.
func levelIndex(level string) int {
	switch level {
	case LevelMap:
		return 0
	case LevelSections, LevelContext:
		return 1
	case LevelTiles:
		return 2
	}
	return 3
}

// levelContext bounds a level's calls by its share of the run's time: each
// level must end by the time the shares up to it have passed, so what an
// earlier level leaves flows on. The map gets at least MinMapTime
// (levelEnd).
func (o *orchestration) levelContext(ctx context.Context, level string) (context.Context, context.CancelFunc) {
	if o.deadline.IsZero() {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, levelEnd(o.started, o.deadline, level))
}

// levelEnd is when a level of a run from started to deadline must end: once
// the shares up to it have passed. The map ends no sooner than MinMapTime
// after the start, and no later than the end of the sections' share.
func levelEnd(started, deadline time.Time, level string) time.Time {
	total := deadline.Sub(started)
	end := func(index int) time.Duration {
		cumulative := 0.0
		for i := 0; i <= index; i++ {
			cumulative += Shares[i]
		}
		return time.Duration(float64(total) * cumulative)
	}
	index := levelIndex(level)
	if index != 0 {
		return started.Add(end(index))
	}
	return started.Add(max(end(0), min(MinMapTime, end(1))))
}

func (o *orchestration) step(text string) {
	if o.run.Step != nil {
		o.mu.Lock()
		defer o.mu.Unlock()
		o.run.Step(text)
	}
}

// count is "n thing" or "n things".
func count(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return fmt.Sprintf("%d %ss", n, thing)
}

// call is one call of a level: a task with its instructions, its input and
// its check, which the reply passes or is sent back once with the findings.
type call struct {
	level, id, label string
	instructions     string
	input            string
	schema           []byte
	// check returns the findings of a reply; lenient is its last pass.
	check   func(reply []byte, lenient bool) string
	resume  string
	persist bool
}

// runCall runs a call through the generator, in a concurrency slot, with a
// share of its level's budgets, and returns the accepted reply (nil when it
// failed) and its outcome.
func (o *orchestration) runCall(ctx context.Context, c call) ([]byte, agent.Outcome, error) {
	select {
	case o.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, agent.Outcome{}, ctx.Err()
	}
	defer func() { <-o.sem }()
	level := levelIndex(c.level)
	budgets := o.budget.take(level)
	started := time.Now()
	prompt := o.prompt.Shared + "\n\n" + c.instructions + "\n\n" + o.tools
	if c.resume != "" {
		// A resumed session already holds the shared text and the tools.
		prompt = c.instructions
	}
	input := "\n# Input\n\n" + c.input
	var accepted []byte
	budgets.MaxRepairAttempts = max(budgets.MaxRepairAttempts, 1)
	task := agent.Task{
		Label: c.label, Directory: o.dir, Schema: c.schema, Budgets: budgets, Endpoint: o.run.Endpoint,
		Framing: agent.BackendFraming, PersistSession: c.persist, ResumeSession: c.resume, MCP: o.run.MCP,
		Shell: o.run.Shell, ReadDirs: readDirs(o.run.Worktree),
	}
	task.PassPrompt = func(pass agent.TaskPass) (string, error) {
		text := prompt + input
		if pass.Number > 1 {
			text += agent.RepairReply(o.run.Generator.Backend.Name(), "reply", "host", pass)
		}
		return text, nil
	}
	task.Accept = func(ctx context.Context, pass agent.TaskPass, reply []byte, outcome *agent.Outcome) (agent.Verdict, error) {
		masked, err := agent.RedactTaskOutput(ctx, o.redactor, reply, outcome)
		if err != nil {
			return agent.Verdict{}, err
		}
		findings := c.check(masked, pass.Number > pass.Repairs)
		if o.run.Record != nil {
			o.mu.Lock()
			o.run.Record(Call{Level: c.level, ID: c.id, Pass: pass.Number, Prompt: prompt, Input: input, Reply: masked, Findings: findings})
			o.mu.Unlock()
		}
		if findings == "" {
			accepted = masked
		}
		return agent.Verdict{Document: masked, Findings: findings}, nil
	}
	outcome, _, err := o.run.Generator.RunTask(ctx, task)
	o.budget.give(level, budgets, outcome)
	o.account(c.level, outcome, started)
	if err == nil && outcome.Reason != "" {
		err = fmt.Errorf("%s: %s", outcome.Reason, firstLine(outcome.Findings))
	}
	if err != nil {
		accepted = nil
	}
	return accepted, outcome, err
}

func readDirs(w Worktree) []string {
	if w.GitDir == "" {
		return nil
	}
	return []string{w.GitDir}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return cut(line, 300)
}

// account adds a call's spending to its level's and the run's outcome.
func (o *orchestration) account(level string, outcome agent.Outcome, started time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	l := o.out.Levels[level]
	l.Add(outcome)
	o.out.Add(outcome)
	if l.started.IsZero() || started.Before(l.started) {
		l.started = started
	}
	if now := time.Now(); now.After(l.ended) {
		l.ended = now
	}
}

// record sets a call record and counts it on its level.
func (o *orchestration) record(level, id, status, reason string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	r := reviewv2.CallRecord{Status: status, Reason: cut(reason, 300)}
	switch level {
	case LevelMap:
		o.out.Generation.Map = r
	case LevelContext:
		o.out.Generation.Context = r
	case LevelOverview:
		o.out.Generation.Overview = r
	case LevelSections:
		o.out.Generation.Sections[id] = r
	case LevelTiles:
		o.out.Generation.Tiles[id] = r
	}
	l := o.out.Levels[level]
	switch status {
	case StatusRan:
		l.Ran++
	case StatusFailed:
		l.Failed++
	case StatusReused:
		l.Reused++
	case StatusSkipped:
		l.Skipped++
	}
}

// reason is why a call failed, in the host's words.
func reason(err error, outcome agent.Outcome) string {
	switch {
	case err == nil && outcome.Reason != "":
		return outcome.Reason
	case err == nil:
		return "no reply was accepted"
	case errors.Is(err, context.DeadlineExceeded):
		return "the level's time ran out"
	case errors.Is(err, context.Canceled):
		return "the run was stopped"
	}
	return firstLine(err.Error())
}

// finish sets each level's seconds.
func (o *orchestration) finish() {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, l := range o.out.Levels {
		if !l.started.IsZero() {
			l.Seconds = l.ended.Sub(l.started).Seconds()
		}
	}
	o.out.Generation.Sections = nonEmpty(o.out.Generation.Sections)
	o.out.Generation.Tiles = nonEmpty(o.out.Generation.Tiles)
}

func nonEmpty(m map[string]reviewv2.CallRecord) map[string]reviewv2.CallRecord {
	if len(m) == 0 {
		return nil
	}
	return m
}

// label is a call's file label in its directory: lowercase letters, digits
// and dashes.
func label(prefix, id string) string {
	id = strings.ToLower(strings.ReplaceAll(id, "_", "-"))
	id = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return -1
	}, id)
	id = strings.Trim(id, "-")
	for strings.Contains(id, "--") {
		id = strings.ReplaceAll(id, "--", "-")
	}
	if id == "" {
		return prefix
	}
	return prefix + "-" + id
}

// mapLevel writes the map. Its failure is the run's.
func (o *orchestration) mapLevel(ctx context.Context) error {
	o.step("Mapping the pull request")
	b := o.run.Bundle
	input := basics(b, o.run.Worktree) + "\n" + diffIndex(b) + "\nThe tile types you may plan:\n" + o.registry.Offer()
	var previous *reviewv2.Document
	if len(o.run.Previous) > 0 {
		if doc, err := reviewv2.Read(o.run.Previous); err == nil {
			previous = &doc
		}
	}
	if prev := previousMap(previous, b); prev != "" {
		input += "\n" + prev
	}
	var accepted plan
	c := call{level: LevelMap, id: "map", label: "map", instructions: o.prompt.Map, input: input,
		schema: MapSchema(o.registry.names()), persist: agent.KeepsSessions(o.run.Generator.Backend)}
	c.check = func(reply []byte, lenient bool) string {
		p, findings := checkMap(reply, o.facts, o.registry, lenient)
		if findings == "" {
			accepted = p
		}
		return findings
	}
	levelCtx, cancel := o.levelContext(ctx, LevelMap)
	defer cancel()
	reply, outcome, err := o.runCall(levelCtx, c)
	o.budget.close(0)
	if reply == nil {
		o.record(LevelMap, "map", StatusFailed, reason(err, outcome))
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %s", ErrMapFailed, reason(err, outcome))
	}
	o.record(LevelMap, "map", StatusRan, "")
	o.mu.Lock()
	o.plan, o.mapSession = accepted, outcome.SessionID
	for i := range accepted.Sections {
		s := accepted.Sections[i]
		s.Details = "pending"
		o.sections[s.ID] = &s
	}
	o.tiles = slices.Clone(accepted.Tiles)
	o.mu.Unlock()
	o.publish()
	return nil
}
