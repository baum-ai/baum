package uiserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/addedtiles"
	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/tilechecks"
	"github.com/baum-ai/baum/internal/tiledrafts"
	"github.com/baum-ai/baum/internal/tilelibrary"
	"github.com/baum-ai/baum/reviewv2"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
	"github.com/baum-ai/baum/tiles/view"
)

// A tile made in chat is a tile the person added to the pull request
// (internal/addedtiles, of kind custom): its definition is kept by its
// digest in tile_definitions and its data, for the version it was made
// for, in tile_fills, the agent's (OriginChat). It is personal, shows at
// once on its section and has Remove on its menu, as every added tile. The
// session panel's agent makes it in three steps (agentcontext's tile making
// tools): a pending card on the page (tiledrafts), render checks with the
// pull request's data, and the add of a check that passed.

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
	if t.API.Added == nil || t.Drafts == nil {
		return agentcontext.TileDraft{}, errors.New("making tiles in chat is not available in this baum")
	}
	c, err := t.API.pullClone(ctx, repository, number)
	if err != nil {
		return agentcontext.TileDraft{}, err
	}
	shown, err := t.API.latestShown(ctx, c, number)
	if err != nil {
		return agentcontext.TileDraft{}, err
	}
	section, err := shown.asked(start.Section)
	if err != nil {
		return agentcontext.TileDraft{}, err
	}
	d := t.Drafts.Start(owner, addedtiles.Key(c.Repository, number), start.Title, section)
	return agentcontext.TileDraft{Tile: d.Tile, Phase: d.Phase}, nil
}

// asked is a section an add tool names, checked against the shown review:
// a current section or context, or empty.
func (s shownVersion) asked(section string) (string, error) {
	if section == "" || section == view.ContextSection {
		return section, nil
	}
	if got := reviewv2.OwnerFor(s.Review, section, nil); got != section {
		return "", fmt.Errorf("The review has no section %s; name one of its sections, or context for Where this fits", section)
	}
	return section, nil
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
	pr := addedtiles.Key(c.Repository, number)
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
		shown, err := t.API.latestShown(ctx, c, number)
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
// with data checked become a custom tile the person added, on the section
// asked for, else the pending card's, else the one whose hunks the data
// cites most. The data is stored first, with the definition, so the added
// tile can name it.
func (t *ChatTiles) Add(ctx context.Context, owner, repository string, number int, add agentcontext.CustomTileAdd) (agentcontext.TileAdded, error) {
	if t.API.Added == nil {
		return agentcontext.TileAdded{}, errors.New("making tiles in chat is not available in this baum")
	}
	c, err := t.API.pullClone(ctx, repository, number)
	if err != nil {
		return agentcontext.TileAdded{}, err
	}
	pr := addedtiles.Key(c.Repository, number)
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
	shown, err := t.API.latestShown(ctx, c, number)
	if err != nil {
		return agentcontext.TileAdded{}, err
	}
	if shown.Version != made.version {
		return agentcontext.TileAdded{}, fmt.Errorf("The pull request's review has a new version (%d) since that check: check the tile again with data for it", shown.Version)
	}
	section, err := shown.asked(add.Section)
	if err != nil {
		return agentcontext.TileAdded{}, err
	}
	draft := add.Draft
	if t.Drafts != nil {
		if d, at, ok := t.Drafts.Current(owner, add.Draft); ok && at == pr {
			draft = d.Tile
			if section == "" {
				section = d.Section
			}
		}
	}
	section = shown.section(section, made.data)
	id := tiles.LibraryTileID(made.def.Name)
	if err := t.API.roomFor(ctx, pr, id); err != nil {
		return agentcontext.TileAdded{}, err
	}
	// The added tile reads its definition from its fill, so the fill is
	// written first and taken back (or the earlier one put back) when the
	// add is not saved.
	prior := t.API.Added.HeldFill(ctx, pr, id, made.version)
	if err := t.API.Added.PutFill(ctx, pr, id, made.version, addedtiles.OriginChat, made.definition, made.data); err != nil {
		return agentcontext.TileAdded{}, err
	}
	if err := t.API.Added.Add(ctx, pr, addedtiles.Added{Tile: id, Kind: addedtiles.KindCustom, Name: made.def.Name, Digest: made.def.Digest, Section: section, By: "chat"}); err != nil {
		t.API.Added.RestoreFill(ctx, pr, id, made.version, prior)
		return agentcontext.TileAdded{}, err
	}
	if t.Drafts != nil {
		t.Drafts.Added(owner, draft)
	}
	return agentcontext.TileAdded{Tile: id, State: "ready", Section: section}, nil
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
		if !a.Drafts.Remove(addedtiles.Key(c.Repository, key.Number), r.PathValue("tile")) {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
