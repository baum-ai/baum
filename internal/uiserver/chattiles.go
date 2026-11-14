package uiserver

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/tilechecks"
	"github.com/baum-ai/baum/internal/tiledrafts"
	"github.com/baum-ai/baum/internal/tilelayout"
	"github.com/baum-ai/baum/internal/tilelibrary"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// A tile made in chat is a custom tile in the person's own layout of the
// pull request (an add of kind custom): its definition is kept by its
// digest in tile_definitions and its data, for the version it was made
// for, in tile_fills, the agent's (OriginChat). It is personal, shows at
// once and has an Undo, as every layout edit. The session panel's agent
// makes it in three steps (agentcontext's tile making tools): a pending
// card on the page (tiledrafts), render checks with the pull request's data,
// and the add of a check that passed.

// ChatChecks is the render checker as making a tile in chat uses it;
// *tilechecks.Checker implements it.
type ChatChecks interface {
	Submit(ctx context.Context, candidate tilechecks.Candidate) (tilechecks.Result, error)
	Result(ctx context.Context, id string) (tilelibrary.CheckResult, bool)
}

// chatCheckAge is how long a check stays good for an add.
const chatCheckAge = time.Hour

// maxChatChecks bounds the checks with data kept for an add.
const maxChatChecks = 64

// ChatTiles implements agentcontext.ChatTiles for the daemon.
type ChatTiles struct {
	API    LocalAPI
	Checks ChatChecks
	Drafts *tiledrafts.Store
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	mu sync.Mutex
	// made are the recent checks with data, by check ID, the oldest first.
	made []chatCheck
}

var _ agentcontext.ChatTiles = (*ChatTiles)(nil)

// chatCheck is a check with data for a pull request: what an add of it
// needs.
type chatCheck struct {
	id         string
	pr         string
	version    int
	passed     bool
	definition json.RawMessage
	def        tiles.TypeDefinition
	data       json.RawMessage
	at         time.Time
}

func (t *ChatTiles) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// Start implements agentcontext.ChatTiles.
func (t *ChatTiles) Start(ctx context.Context, owner, repository string, number int, start agentcontext.TileStart) (agentcontext.TileDraft, error) {
	if t.API.Layouts == nil || t.Drafts == nil {
		return agentcontext.TileDraft{}, errors.New("making tiles in chat is not available in this baum")
	}
	c, err := t.API.pullClone(ctx, repository, number)
	if err != nil {
		return agentcontext.TileDraft{}, err
	}
	shown, err := t.API.shownTiles(ctx, c, number, 0)
	if err != nil {
		return agentcontext.TileDraft{}, err
	}
	to, err := placeOn(shown.Document, start.Size, start.To)
	if err != nil {
		return agentcontext.TileDraft{}, err
	}
	d := t.Drafts.Start(owner, tilelayout.Key(c.Repository, number), start.Title, start.Size, to)
	return agentcontext.TileDraft{Tile: d.Tile, Phase: d.Phase}, nil
}

// placeOn checks a size and place on the page shown, and gives a tile with
// no place the one an added tile takes: right after the overview.
func placeOn(doc *tiles.Document, size tiles.Size, to *tiles.Place) (*tiles.Place, error) {
	probe := tiles.Op{Op: tiles.OpAdd, Size: size, To: to, Add: &tiles.Added{Kind: tiles.AddCustom, Name: "probe", Digest: "probe"}}
	if err := tiles.CheckOpShape(probe); err != nil {
		return nil, err
	}
	shown := func(id string) bool {
		return slices.ContainsFunc(doc.Layout.Entries, func(e tiles.Entry) bool { return e.Tile == id && e.Group != tiles.GroupHidden })
	}
	if to == nil {
		if shown(tiles.TypeOverview) {
			return &tiles.Place{After: tiles.TypeOverview}, nil
		}
		return nil, nil
	}
	if anchor := cmp.Or(to.Before, to.After); anchor != "" && !shown(anchor) {
		return nil, fmt.Errorf("No tile %s on the page; tile_layout lists them", anchor)
	}
	return to, nil
}

// Check implements agentcontext.ChatTiles. The data is masked first, so
// what is drawn is what an add stores. Before the check is queued, the
// type's name is refused when it is one of baum's own types' (a tile is
// named by its type). With data, the data's citations must resolve to the
// version the page shows and cite at least one hunk, file, prompt or
// session event of it: those problems join the check's.
func (t *ChatTiles) Check(ctx context.Context, owner, repository string, number int, check agentcontext.TileCheck) (json.RawMessage, error) {
	if t.Checks == nil {
		return nil, errors.New("the render check is not available in this baum")
	}
	c, err := t.API.pullClone(ctx, repository, number)
	if err != nil {
		return nil, err
	}
	pr := tilelayout.Key(c.Repository, number)
	if _, ok := library.Get(check.Definition.Name); ok {
		return answer(tilechecks.Result{Verdict: tilechecks.VerdictFail, Errors: []tilechecks.Problem{chatProblem(tilechecks.CodeDefinitionInvalid, "",
			fmt.Sprintf("%s is the name of one of baum's own tile types; give the tile a name of its own", check.Definition.Name))}})
	}
	samples := make([]library.Sample, 0, len(check.Samples))
	for i, raw := range check.Samples {
		var sample library.Sample
		if err := json.Unmarshal(raw, &sample); err != nil {
			return answer(tilechecks.Result{Verdict: tilechecks.VerdictFail, Errors: []tilechecks.Problem{chatProblem(tilechecks.CodeSampleInvalid, "",
				fmt.Sprintf("sample %d is not a sample: give its name, data, hunks and items", i+1))}})
		}
		samples = append(samples, sample)
	}
	candidate := tilechecks.Candidate{Definition: check.Definition, Samples: samples}
	var extra []tilechecks.Problem
	version := 0
	if len(check.Data) > 0 && string(check.Data) != "null" {
		masked, err := maskData(ctx, check.Data)
		if err != nil {
			return nil, err
		}
		shown, err := t.API.shownTiles(ctx, c, number, 0)
		if err != nil {
			return nil, err
		}
		version = shown.Version
		candidate.Data = masked
		candidate.Context = &tilechecks.Context{Clone: c.ID, PR: number, Version: version}
		extra = citedProblems(shown.Document, check.Definition, masked)
	}
	if t.Drafts != nil {
		_, _ = t.Drafts.Checking(owner, check.Draft)
	}
	result, err := t.Checks.Submit(ctx, candidate)
	if err != nil {
		if t.Drafts != nil {
			_, _ = t.Drafts.Failed(owner, check.Draft, err.Error())
		}
		return nil, err
	}
	if len(extra) > 0 && (result.Verdict == tilechecks.VerdictPass || result.Verdict == tilechecks.VerdictFail) {
		result.Verdict = tilechecks.VerdictFail
		result.Errors = append(result.Errors, extra...)
	}
	if t.Drafts != nil {
		switch result.Verdict {
		case tilechecks.VerdictHang:
			_, _ = t.Drafts.Failed(owner, check.Draft, "The component never finished drawing and was stopped")
		case tilechecks.VerdictUnavailable:
			_, _ = t.Drafts.Failed(owner, check.Draft, result.Reason)
		}
	}
	if result.Check != "" && candidate.Data != nil {
		t.keep(pr, version, candidate, result)
	}
	return answer(result)
}

// citedProblems are the problems of data whose citations do not resolve to
// the version shown, or that cites nothing of it. Data that does not match
// its schema, or a definition that is not valid, has none here: the check
// reports those.
func citedProblems(doc *tiles.Document, def tiles.TypeDefinition, data json.RawMessage) []tilechecks.Problem {
	encoded, err := library.Seal(&def)
	if err != nil {
		return nil
	}
	validator := fillValidator()
	if _, problems := validator.CheckDefinition(encoded); len(problems) > 0 {
		return nil
	}
	if len(validator.CheckData(encoded, data)) > 0 {
		return nil
	}
	problems, grounded := validator.CheckCitedData(doc, encoded, data)
	var out []tilechecks.Problem
	for _, p := range problems {
		out = append(out, chatProblem(tilechecks.CodeUnresolvedCitation, tilechecks.KindData, p))
	}
	if len(problems) == 0 && !grounded {
		out = append(out, chatProblem(tilechecks.CodeUncited, tilechecks.KindData, "the data cites nothing of this pull request's review: a tile made in chat cites at least one hunk, file, prompt or session event of it"))
	}
	return out
}

func chatProblem(code, sample, message string) tilechecks.Problem {
	return tilechecks.Problem{Code: code, Message: message, Hint: tilechecks.Hints[code], Sample: sample}
}

// answer is a check's result as check_tile gives it: without the runs, and
// with what to do next.
func answer(result tilechecks.Result) (json.RawMessage, error) {
	out := map[string]any{"verdict": result.Verdict, "errors": nonNilProblems(result.Errors), "warnings": nonNilProblems(result.Warnings)}
	if result.Check != "" {
		out["check"] = result.Check
	}
	if result.Digest != "" {
		out["digest"] = result.Digest
	}
	if result.Reason != "" {
		out["reason"] = result.Reason
	}
	switch result.Verdict {
	case tilechecks.VerdictPass:
		out["next"] = "Passed. With data for this pull request, add it with add_custom_tile {check}."
	case tilechecks.VerdictFail:
		out["next"] = "Fix each error (the hint says how) and check again."
	case tilechecks.VerdictHang:
		out["next"] = "The component never finished drawing; it was stopped (an endless loop?). Change the code: the same code stays blocked until baum restarts."
	case tilechecks.VerdictUnavailable:
		out["next"] = "Nothing was added. Tell the person: " + result.Reason
	}
	return json.Marshal(out)
}

func nonNilProblems(p []tilechecks.Problem) []tilechecks.Problem {
	if p == nil {
		return []tilechecks.Problem{}
	}
	return p
}

// keep records a check with data for an add: the definition the checker
// checked (with its HTML) and the masked data, for the version shown.
func (t *ChatTiles) keep(pr string, version int, candidate tilechecks.Candidate, result tilechecks.Result) {
	def := candidate.Definition
	var definition json.RawMessage
	if kept, ok := t.Checks.Result(context.Background(), result.Check); ok && len(kept.Definition) > 0 {
		definition = kept.Definition
		_ = json.Unmarshal(definition, &def)
	} else {
		var err error
		if definition, err = library.Seal(&def); err != nil {
			return
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.made = slices.DeleteFunc(t.made, func(m chatCheck) bool { return now.Sub(m.at) >= chatCheckAge })
	t.made = append(t.made, chatCheck{id: result.Check, pr: pr, version: version, passed: result.Verdict == tilechecks.VerdictPass,
		definition: definition, def: def, data: candidate.Data, at: now})
	if len(t.made) > maxChatChecks {
		t.made = slices.Delete(t.made, 0, len(t.made)-maxChatChecks)
	}
}

// Add implements agentcontext.ChatTiles: the type and data a passed check
// with data checked become the version's custom tile, as one batch. The
// data is stored first, with the definition, so the add can name it.
func (t *ChatTiles) Add(ctx context.Context, owner, repository string, number int, add agentcontext.CustomTileAdd) (agentcontext.TileAdded, error) {
	if t.API.Layouts == nil {
		return agentcontext.TileAdded{}, errors.New("making tiles in chat is not available in this baum")
	}
	c, err := t.API.pullClone(ctx, repository, number)
	if err != nil {
		return agentcontext.TileAdded{}, err
	}
	pr := tilelayout.Key(c.Repository, number)
	t.mu.Lock()
	i := slices.IndexFunc(t.made, func(m chatCheck) bool { return m.id == add.Check })
	var made chatCheck
	if i >= 0 {
		made = t.made[i]
	}
	t.mu.Unlock()
	switch {
	case i < 0:
		if t.Checks != nil {
			if _, ok := t.Checks.Result(ctx, add.Check); ok {
				return agentcontext.TileAdded{}, errors.New("That check had no data for this pull request: check the tile again with data, then add that check")
			}
		}
		return agentcontext.TileAdded{}, errors.New("No check " + add.Check + " from the last hour: check the tile again and add that check")
	case t.now().Sub(made.at) >= chatCheckAge:
		return agentcontext.TileAdded{}, errors.New("That check is older than an hour: check the tile again and add that check")
	case made.pr != pr:
		return agentcontext.TileAdded{}, errors.New("That check was for another pull request: check the tile again with this pull request's data")
	case !made.passed:
		return agentcontext.TileAdded{}, errors.New("That check did not pass: fix its errors, check again and add a check that passed")
	}
	shown, err := t.API.shownTiles(ctx, c, number, 0)
	if err != nil {
		return agentcontext.TileAdded{}, err
	}
	if shown.Version != made.version {
		return agentcontext.TileAdded{}, fmt.Errorf("The pull request's review has a new version (%d) since that check: check the tile again with data for it", shown.Version)
	}
	size, to := add.Size, add.To
	draft := add.Draft
	if t.Drafts != nil {
		if d, at, ok := t.Drafts.Current(owner, add.Draft); ok && at == pr {
			draft = d.Tile
			if size == "" {
				size = d.Size
			}
			if to == nil {
				to = d.To
			}
		}
	}
	if size != "" && !slices.Contains(made.def.Sizes, size) {
		size = ""
	}
	if to, err = placeOn(shown.Document, size, to); err != nil {
		return agentcontext.TileAdded{}, err
	}
	id := tiles.LibraryTileID(made.def.Name)
	op := tiles.Op{Op: tiles.OpAdd, Size: size, To: to, Add: &tiles.Added{Kind: tiles.AddCustom, Name: made.def.Name, Digest: made.def.Digest, Title: summaryTitle(made.def.Title)}}
	if reason := tiles.CheckOp(shown.Document, op); reason != "" {
		return agentcontext.TileAdded{}, errors.New(reason)
	}
	// The layout reads the tile's definition from its fill, so the fill is
	// written first and taken back (or the hidden tile's earlier one put back)
	// when the layout refuses the add.
	prior := t.API.Layouts.HeldFill(ctx, pr, id, made.version)
	if err := t.API.Layouts.PutFill(ctx, pr, id, made.version, tilelayout.OriginChat, made.definition, made.data); err != nil {
		return agentcontext.TileAdded{}, err
	}
	change, err := t.API.editLayout(ctx, c, number, "chat", []tiles.Op{op})
	if err != nil {
		t.API.Layouts.RestoreFill(ctx, pr, id, made.version, prior)
		return agentcontext.TileAdded{}, err
	}
	if !change.Saved {
		t.API.Layouts.RestoreFill(ctx, pr, id, made.version, prior)
		reasons := make([]string, len(change.Refused))
		for i, refusal := range change.Refused {
			reasons[i] = refusal.Reason
		}
		return agentcontext.TileAdded{}, errors.New(strings.Join(reasons, "; "))
	}
	if t.Drafts != nil {
		t.Drafts.Added(owner, draft)
	}
	return agentcontext.TileAdded{Tile: id, State: "ready", Change: changeOf(change)}, nil
}

// TurnEnded implements agentcontext.ChatTiles.
func (t *ChatTiles) TurnEnded(owner string) {
	if t.Drafts != nil {
		t.Drafts.TurnEnded(owner)
	}
}

// draftRoutes mounts DELETE /api/tiles/drafts/{tile}?clone=&pr=: the
// page's Remove on a pending card. 404 when there is no such card.
func (a LocalAPI) draftRoutes(mux *http.ServeMux) {
	mux.HandleFunc("DELETE /api/tiles/drafts/{tile}", func(w http.ResponseWriter, r *http.Request) {
		if a.Drafts == nil {
			http.Error(w, "capability unavailable", http.StatusServiceUnavailable)
			return
		}
		c, key, _, ok := a.selector(w, r)
		if !ok {
			return
		}
		if !a.Drafts.Remove(tilelayout.Key(c.Repository, key.Number), r.PathValue("tile")) {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
