package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// StagedTiles turns on a staged review's tile analysis (StagedRun.Tiles):
// the outline also answers which library types apply, each type that applies
// is filled by a call of its own, and one call may write up to three
// authored tiles (exhibits). They run beside the details and the context and
// never fail the review: a fill or exhibit that fails is dropped.
type StagedTiles struct {
	Prompts TileAnalysisPrompts
	// Library are the types offered, in offer order; none offers no
	// selection and fills nothing.
	Library []library.Entry
	// Exhibits asks one call for authored tiles.
	Exhibits bool
	// Grace bounds how long fills and exhibits may run on once the details
	// and the context have ended; zero is DefaultTilesGrace.
	Grace time.Duration
}

// DefaultTilesGrace is how long fills and exhibits may run on once the
// review's own stages have ended, past which they are stopped and dropped: a
// tile beside the review never holds it up for long.
const DefaultTilesGrace = 45 * time.Second

// TileAnalysisPrompts supplies the texts of a staged review's tile analysis.
type TileAnalysisPrompts interface {
	TileAnalysisInstructions(ctx context.Context, backend string) (TileAnalysisPrompt, error)
}

// TileAnalysisPrompt holds the tile analysis texts for one backend: Select is
// inserted in the outline stage's prompt before its Submitting heading,
// followed by the offered types; Fill opens each fill, before the type's own
// instructions, and FillSubmit (a Submitting heading and the backend's submit
// block) ends it; Exhibits is the exhibits call's whole instructions, its
// submit block included. Digest covers them all.
type TileAnalysisPrompt struct {
	Version, Digest          string
	Select, Fill, FillSubmit string
	Exhibits                 string
}

// Selection is the outline's answer for one offered library type: whether it
// applies, the hunks that show it (all in the diff) and why. A type the
// outline did not answer, or answered with no hunk of the diff, does not
// apply.
type Selection struct {
	Type    string   `json:"type"`
	Applies bool     `json:"applies"`
	Hunks   []string `json:"hunks,omitempty"`
	Why     string   `json:"why,omitempty"`
}

// The states of a fill.
const (
	// FillReady is a fill whose data passed library.CheckFill.
	FillReady = "ready"
	// FillNotApplies is a fill that answered the type does not apply.
	FillNotApplies = "not_applies"
	// FillDropped is a fill that failed: its reason says why.
	FillDropped = "dropped"
	// FillPending is a fill still running, in a draft.
	FillPending = "pending"
)

// FillResult is what the fill of one selected type did.
type FillResult struct {
	Type string `json:"type"`
	// Hunks and Why are the outline's selection.
	Hunks []string `json:"hunks,omitempty"`
	Why   string   `json:"why,omitempty"`
	// State is FillReady, FillNotApplies or FillDropped.
	State string `json:"state"`
	// Reason says why a fill was dropped, in the host's words.
	Reason string `json:"reason,omitempty"`
	// Data is the fill's data, masked, for a ready fill.
	Data     json.RawMessage `json:"data,omitempty"`
	Repaired bool            `json:"repaired,omitempty"`
	Outcome  Outcome         `json:"-"`
	Elapsed  time.Duration   `json:"elapsed"`
}

// ExhibitsResult is what the exhibits call did: the authored tiles it wrote
// that pass the host's checks against the bundle, and those it dropped.
type ExhibitsResult struct {
	Returned int                  `json:"returned"`
	Tiles    []tiles.AuthoredTile `json:"tiles,omitempty"`
	// Adds holds each kept tile's own sentence on what it adds, by ID.
	Adds    map[string]string `json:"adds,omitempty"`
	Dropped []DroppedExhibit  `json:"dropped,omitempty"`
	// Reason says why the call failed, when it did.
	Reason  string        `json:"reason,omitempty"`
	Outcome Outcome       `json:"-"`
	Elapsed time.Duration `json:"elapsed"`
}

// DroppedExhibit is an exhibit the host dropped, with its reason (one of the
// Exhibit* reasons).
type DroppedExhibit struct {
	ID     string `json:"id,omitempty"`
	Reason string `json:"reason"`
}

// The reasons an exhibit is dropped.
const (
	ExhibitShape     = "shape"
	ExhibitCitation  = "citation"
	ExhibitSize      = "size"
	ExhibitCode      = "code_not_in_source"
	ExhibitDuplicate = "duplicate"
	ExhibitLimit     = "limit"
)

// The reasons a fill is dropped, the host's words.
const (
	FillDroppedInvalid   = "the data did not pass its checks after a repair"
	FillDroppedEmpty     = "the fill answered that the type applies but wrote no data"
	FillDroppedFailed    = "the fill call failed"
	FillDroppedStopped   = "the fill was stopped when the review's own stages had ended"
	FillDroppedBudget    = "the call or token budget left nothing for the fill"
	FillDroppedRedaction = "the fill's reply could not be checked for secrets"
)

// The bounds of an authored tile, besides the composition's.
const (
	maxExhibitColumns  = 6
	maxExhibitRows     = 20
	maxExhibitCell     = 200
	maxExhibitCode     = 40
	maxExhibitCodeSize = 4 << 10
	maxExhibitProse    = 600
	maxSelectionHunks  = 64
	maxSelectionWhy    = 200
)

// tileValidator checks fills; it compiles its schemas once.
var tileValidator = sync.OnceValue(tiles.NewValidator)

// tileAnalysis is the state of a staged review's tile analysis.
type tileAnalysis struct {
	config StagedTiles
	prompt TileAnalysisPrompt
	diff   map[string]bundle.Hunk

	mu        sync.Mutex
	selection []Selection
	fills     []FillResult
	exhibits  *ExhibitsResult
	// waited is how long the review waited for the extras once its own
	// stages had ended.
	waited time.Duration
}

// newTileAnalysis prepares a run's tile analysis, or returns nil when the run
// has none or the backend cannot run calls at once: fills and exhibits run
// beside the details and the context, and a backend that runs one call at a
// time would make them wait for each other.
func newTileAnalysis(ctx context.Context, g *Generator, run StagedRun) (*tileAnalysis, error) {
	t := run.Tiles
	if t == nil || run.Outline != nil || (len(t.Library) == 0 && !t.Exhibits) || !concurrentCalls(g.Backend) {
		return nil, nil
	}
	if t.Prompts == nil {
		return nil, errors.New("a staged review's tile analysis needs its prompts")
	}
	prompt, err := t.Prompts.TileAnalysisInstructions(ctx, g.Backend.Name())
	if err != nil {
		return nil, err
	}
	a := &tileAnalysis{config: *t, prompt: prompt, diff: map[string]bundle.Hunk{}}
	for _, h := range run.Bundle.Diff {
		a.diff[h.ID] = h
	}
	return a, nil
}

// outlinePrompt inserts the selection text and the offered types before the
// outline prompt's Submitting heading.
func (a *tileAnalysis) outlinePrompt(outline string) string {
	if a == nil || len(a.config.Library) == 0 {
		return outline
	}
	var b strings.Builder
	b.WriteString(a.prompt.Select)
	for _, e := range a.config.Library {
		fmt.Fprintf(&b, "- `%s` (%s): %s\n", e.Definition.Name, e.Definition.Title, e.Definition.UseWhen)
	}
	b.WriteString("\n")
	at := strings.LastIndex(outline, "## Submitting\n")
	if at < 0 {
		return outline + "\n" + b.String()
	}
	return outline[:at] + b.String() + outline[at:]
}

// outlineSchema adds the library array to the outline's model schema: one
// entry per offered type, in offer order.
func (a *tileAnalysis) outlineSchema(schema []byte) ([]byte, error) {
	if a == nil || len(a.config.Library) == 0 {
		return schema, nil
	}
	var root map[string]any
	if err := json.Unmarshal(schema, &root); err != nil {
		return nil, err
	}
	properties, _ := root["properties"].(map[string]any)
	if properties == nil {
		return nil, errors.New("the outline schema has no properties")
	}
	names := make([]any, 0, len(a.config.Library))
	for _, e := range a.config.Library {
		names = append(names, e.Definition.Name)
	}
	hunk := map[string]any{"type": "string"}
	if defs, ok := root["$defs"].(map[string]any); ok && defs["hunkId"] != nil {
		hunk = map[string]any{"$ref": "#/$defs/hunkId"}
	}
	properties["library"] = map[string]any{
		"type": "array", "minItems": len(names), "maxItems": len(names),
		"description": "One entry per offered library tile type, in the order offered: whether this pull request changes what it shows.",
		"items": map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []any{"type", "applies", "hunks", "why"},
			"properties": map[string]any{
				"type":    map[string]any{"enum": names},
				"applies": map[string]any{"type": "boolean"},
				"hunks":   map[string]any{"type": "array", "maxItems": maxSelectionHunks, "items": hunk},
				"why":     map[string]any{"type": []any{"string", "null"}, "maxLength": maxSelectionWhy},
			},
		},
	}
	required, _ := root["required"].([]any)
	root["required"] = append(required, "library")
	return json.Marshal(root)
}

// splitOutline takes the library array out of an outline reply, so the
// outline is prepared as it is without tiles, and returns the reply without
// it and the array. A reply that is not an object is returned as it is.
func splitOutline(reply []byte) ([]byte, json.RawMessage) {
	var members map[string]json.RawMessage
	if json.Unmarshal(reply, &members) != nil || members == nil {
		return reply, nil
	}
	selection, ok := members["library"]
	if !ok {
		return reply, nil
	}
	delete(members, "library")
	stripped, err := json.Marshal(members)
	if err != nil {
		return reply, nil
	}
	return stripped, selection
}

// selectionOf reads the outline's library answer leniently: one selection
// per offered type in offer order; a type not answered, or answered with no
// hunk of the diff, does not apply; unknown hunks are dropped. A wrong answer
// is never repaired: the outline is the review's and is not sent back for a
// tile.
func (a *tileAnalysis) selectionOf(raw json.RawMessage) []Selection {
	var answers []struct {
		Type    string   `json:"type"`
		Applies bool     `json:"applies"`
		Hunks   []string `json:"hunks"`
		Why     *string  `json:"why"`
	}
	_ = json.Unmarshal(raw, &answers)
	out := make([]Selection, 0, len(a.config.Library))
	for _, e := range a.config.Library {
		s := Selection{Type: e.Definition.Name}
		for _, answer := range answers {
			if answer.Type != s.Type {
				continue
			}
			for _, id := range answer.Hunks {
				if _, ok := a.diff[id]; ok && !slices.Contains(s.Hunks, id) && len(s.Hunks) < maxSelectionHunks {
					s.Hunks = append(s.Hunks, id)
				}
			}
			s.Applies = answer.Applies && len(s.Hunks) > 0
			if !s.Applies {
				s.Hunks = nil
			}
			if answer.Why != nil && s.Applies {
				s.Why = boundRunes(strings.Join(strings.Fields(*answer.Why), " "), maxSelectionWhy)
			}
			break
		}
		out = append(out, s)
	}
	return out
}

func boundRunes(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit])
}

// selected returns the library entries the selection applies, with their
// selections.
func (a *tileAnalysis) selected() []Selection {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []Selection
	for _, s := range a.selection {
		if s.Applies {
			out = append(out, s)
		}
	}
	return out
}

func (a *tileAnalysis) entry(name string) library.Entry {
	for _, e := range a.config.Library {
		if e.Definition.Name == name {
			return e
		}
	}
	return library.Entry{}
}

// reviewChecks is what the details stage is told to check for the types
// selected: each type's review_checks text, under its title.
func (a *tileAnalysis) reviewChecks() string {
	if a == nil {
		return ""
	}
	var b strings.Builder
	for _, s := range a.selected() {
		e := a.entry(s.Type)
		if e.ReviewChecks == "" {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("## Checks for what this pull request changes\n\nThe outline found that this pull request changes the following. Where a check holds, raise it as a risk of the section that holds the code, anchored on the statement.\n")
		}
		fmt.Fprintf(&b, "\n### %s\n\n%s\n", e.Definition.Title, e.ReviewChecks)
	}
	if b.Len() == 0 {
		return ""
	}
	return b.String() + "\n"
}

// extras counts the calls the analysis adds beside the details and context.
func (a *tileAnalysis) extras() int {
	if a == nil {
		return 0
	}
	n := len(a.selected())
	if a.config.Exhibits {
		n++
	}
	return n
}

// extraBudgets splits the context stage's share with the extras when the
// run's call or token budgets are bounded: the details keep theirs, the
// context keeps half of its share and the extras share the other half
// evenly. A bounded share left with nothing is exhausted.
func extraBudgets(run, context Budgets, n int) (Budgets, Budgets) {
	each := context
	if n == 0 {
		return context, each
	}
	split := func(total int) (int, int) {
		if total <= 0 {
			return total, total
		}
		extras := total / 2
		return total - extras, extras / n
	}
	if run.MaxCalls > 0 {
		context.MaxCalls, each.MaxCalls = split(context.MaxCalls)
		if each.MaxCalls < 1 {
			each.MaxCalls = -1
		}
	}
	if run.MaxTokens > 0 {
		context.MaxTokens, each.MaxTokens = split(context.MaxTokens)
		if each.MaxTokens < 1 {
			each.MaxTokens = -1
		}
	}
	return context, each
}

// TilesProgress is the tile analysis so far, as a draft shows it: the
// selection and each selected type's fill, pending until it lands.
type TilesProgress struct {
	Selection []Selection
	Fills     []FillResult
}

// progress returns the analysis so far for a draft.
func (a *tileAnalysis) progress() *TilesProgress {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	p := &TilesProgress{Selection: slices.Clone(a.selection)}
	done := map[string]FillResult{}
	for _, f := range a.fills {
		done[f.Type] = f
	}
	for _, s := range a.selection {
		if !s.Applies {
			continue
		}
		f, ok := done[s.Type]
		if !ok {
			f = FillResult{Type: s.Type, Hunks: s.Hunks, Why: s.Why, State: FillPending}
		}
		p.Fills = append(p.Fills, f)
	}
	return p
}

// fillSchema is a fill's reply schema: whether the type applies and its data
// (the type's fill schema) or null, with the data schema's definitions at
// the root so its references resolve.
func fillSchema(e library.Entry) ([]byte, error) {
	raw, err := e.FillSchema()
	if err != nil {
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	defs := data["$defs"]
	delete(data, "$defs")
	delete(data, "$schema")
	root := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []any{"applies", "data"},
		"properties": map[string]any{
			"applies": map[string]any{"type": "boolean", "description": "false when, having read the code, the type does not apply after all"},
			"data":    map[string]any{"anyOf": []any{data, map[string]any{"type": "null"}}},
		},
	}
	if defs != nil {
		root["$defs"] = defs
	}
	return json.Marshal(root)
}

// fillLabel is a fill task's label: its files in the run directory are
// schema-fill-<type>.json and so on.
func fillLabel(name string) string {
	return "fill-" + strings.ReplaceAll(name, "_", "-")
}

// fillStage fills one selected type: a task with the stage prefix, the fill
// text, the type's instructions, the outline and the selected hunks. Its
// reply is masked as a task's output, then checked (library.CheckFill); one
// repair sends the problems back. A fill that answers the type does not
// apply, fails, or is still invalid after the repair is not a tile.
func (s *stagedReview) fillStage(ctx context.Context, sel Selection, budgets Budgets) {
	started := time.Now()
	a := s.tiles
	e := a.entry(sel.Type)
	result := FillResult{Type: sel.Type, Hunks: sel.Hunks, Why: sel.Why, State: FillDropped}
	defer func() {
		result.Elapsed = time.Since(started)
		a.mu.Lock()
		a.fills = append(a.fills, result)
		a.mu.Unlock()
		s.mu.Lock()
		s.report(StagedProgress{Stage: StageFill, Section: sel.Type, Failed: result.State != FillReady})
		s.mu.Unlock()
	}()
	if budgets.MaxCalls < 0 || budgets.MaxTokens < 0 {
		result.Reason = FillDroppedBudget
		return
	}
	schema, err := fillSchema(e)
	if err != nil {
		result.Reason = FillDroppedFailed
		return
	}
	var why string
	if sel.Why != "" {
		why = "\nThe outline's reason (model-written data): " + sel.Why + "\n"
	}
	text := s.prefix + "\n\n" + a.prompt.Fill + "\n## The tile type: " + e.Definition.Title + " (`" + e.Definition.Name + "`)\n\n" + e.Definition.Fill + "\n" +
		s.outlineBlock() + "\nHunks the outline chose for this tile: " + strings.Join(sel.Hunks, ", ") + "\n" + why + "\n" + a.prompt.FillSubmit
	budgets.MaxRepairAttempts = 1
	task := s.task(fillLabel(sel.Type), schema, budgets)
	var accepted json.RawMessage
	applies := true
	task.PassPrompt = func(pass TaskPass) (string, error) {
		if pass.Number == 1 {
			return text, nil
		}
		return text + s.repair(pass, "answer"), nil
	}
	task.Accept = func(ctx context.Context, pass TaskPass, reply []byte, outcome *Outcome) (Verdict, error) {
		masked, err := RedactTaskOutput(ctx, s.redactor, reply, outcome)
		if err != nil {
			return Verdict{}, errRedaction
		}
		var answer struct {
			Applies *bool           `json:"applies"`
			Data    json.RawMessage `json:"data"`
		}
		if json.Unmarshal(masked, &answer) != nil || answer.Applies == nil {
			return Verdict{Document: masked, Findings: "/: the reply must be an object with applies (true or false) and data"}, nil
		}
		if !*answer.Applies {
			applies = false
			return Verdict{Document: masked}, nil
		}
		data := dropNullLines(answer.Data)
		if len(data) == 0 || string(data) == "null" {
			return Verdict{Document: masked, Findings: "/data: write the tile's data, or answer applies false with data null"}, nil
		}
		problems := library.CheckFill(tileValidator(), e, data, s.run.Bundle.Diff)
		if len(problems) > 0 {
			return Verdict{Document: masked, Findings: strings.Join(problems[:min(len(problems), 12)], "\n")}, nil
		}
		accepted = data
		return Verdict{Document: masked}, nil
	}
	stage, _, err := s.g.RunTask(ctx, task)
	result.Outcome, result.Repaired = stage, stage.ValidationRepairs > 0
	switch {
	case errors.Is(err, errRedaction):
		result.Reason = FillDroppedRedaction
	case err != nil && ctx.Err() != nil:
		result.Reason = FillDroppedStopped
	case err != nil:
		result.Reason = FillDroppedFailed
		if strings.HasPrefix(err.Error(), GenerationBudgetExceededReason("")) {
			result.Reason = FillDroppedBudget
		}
	case !applies:
		result.State = FillNotApplies
	case stage.Reason != "" || accepted == nil:
		result.Reason = FillDroppedInvalid
		if stage.Reason == "" {
			result.Reason = FillDroppedEmpty
		}
	default:
		result.State, result.Data = FillReady, accepted
	}
}

// dropNullLines removes "lines": null from the citations of a fill's data: a
// strict structured output (Codex) writes an optional property as null.
func dropNullLines(data json.RawMessage) json.RawMessage {
	if !strings.Contains(string(data), `"lines":null`) && !strings.Contains(string(data), `"lines": null`) {
		return data
	}
	var value any
	if json.Unmarshal(data, &value) != nil {
		return data
	}
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case []any:
			for _, item := range v {
				walk(item)
			}
		case map[string]any:
			if lines, ok := v["lines"]; ok && lines == nil {
				delete(v, "lines")
			}
			for _, item := range v {
				walk(item)
			}
		}
	}
	walk(value)
	out, err := json.Marshal(value)
	if err != nil {
		return data
	}
	return out
}

// choose records the outline's library answer, its reasons masked as a
// task's output. Masking that fails selects nothing: the analysis never
// fails the review.
func (a *tileAnalysis) choose(ctx context.Context, redactor *bundle.Redactor, raw json.RawMessage) error {
	if a == nil {
		return nil
	}
	if len(raw) > 0 {
		var scratch Outcome
		masked, err := RedactTaskOutput(ctx, redactor, raw, &scratch)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			raw = nil
		} else {
			raw = masked
		}
	}
	selection := a.selectionOf(raw)
	a.mu.Lock()
	a.selection = selection
	a.mu.Unlock()
	return nil
}

// startExtras starts each selected type's fill and the exhibits call beside
// the details and the context, each with budgets. The returned function is
// called once the details and the context have ended: it lets the extras run
// on for the grace period at most, then stops them, and returns once every
// one has ended.
func (s *stagedReview) startExtras(ctx context.Context, budgets Budgets) func() {
	a := s.tiles
	if a == nil {
		return func() {}
	}
	extrasCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	for _, sel := range a.selected() {
		wg.Go(func() { s.fillStage(extrasCtx, sel, budgets) })
	}
	if a.config.Exhibits {
		wg.Go(func() { s.exhibitsStage(extrasCtx, budgets) })
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	return func() {
		grace := a.config.Grace
		if grace <= 0 {
			grace = DefaultTilesGrace
		}
		timer := time.NewTimer(grace)
		defer timer.Stop()
		ended := time.Now()
		select {
		case <-done:
		case <-timer.C:
		case <-ctx.Done():
		}
		cancel()
		<-done
		a.mu.Lock()
		a.waited = time.Since(ended)
		a.mu.Unlock()
	}
}

// tilesOutcome records the analysis on the run's outcome and adds what its
// calls spent, once.
func (s *stagedReview) tilesOutcome() {
	a := s.tiles
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	t := &TilesOutcome{PromptVersion: a.prompt.Version, PromptDigest: a.prompt.Digest, Selection: slices.Clone(a.selection), Waited: a.waited}
	for _, e := range a.config.Library {
		for _, f := range a.fills {
			if f.Type == e.Definition.Name {
				t.Fills = append(t.Fills, f)
			}
		}
	}
	t.Exhibits = a.exhibits
	for _, o := range t.outcomes() {
		s.outcome.add(*o)
	}
	s.outcome.Tiles = t
}

// outcomes are the outcomes of the analysis's calls.
func (t *TilesOutcome) outcomes() []*Outcome {
	if t == nil {
		return nil
	}
	var out []*Outcome
	for i := range t.Fills {
		out = append(out, &t.Fills[i].Outcome)
	}
	if t.Exhibits != nil {
		out = append(out, &t.Exhibits.Outcome)
	}
	return out
}
