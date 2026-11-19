package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/reviewv2"
	"github.com/baum-ai/baum/tiles/view"
)

// middleLevels runs the section level and the tile level: the section
// agents and the context agent at once, the mechanical section's summary
// by the host, the reused sections without calls, and each tile as soon as
// its owner is written (the overview's main diagram at once).
func (o *orchestration) middleLevels(ctx context.Context) {
	o.mu.Lock()
	sections := slices.Clone(o.plan.Sections)
	planned := slices.Clone(o.plan.Tiles)
	o.mu.Unlock()
	reused := o.reuse(sections)
	var toWrite []reviewv2.Section
	for _, s := range sections {
		switch {
		case reused[s.ID]:
		case s.Kind == "mechanical":
			o.mechanical(s)
		case len(s.Hunks) == 0:
			o.setSection(s.ID, func(sec *reviewv2.Section) { sec.Details = "" })
			o.record(LevelSections, s.ID, StatusSkipped, "the section has no hunks")
		default:
			toWrite = append(toWrite, s)
		}
	}
	mandatory := o.mandatoryTiles(sections)
	tileCalls := 0
	for _, t := range planned {
		if !reused[t.Owner] && t.Type != TypeCode {
			tileCalls++
		}
	}
	for _, list := range mandatory {
		tileCalls += len(list)
	}
	o.budget.expect(1, len(toWrite)+1)
	o.budget.expect(2, tileCalls)
	o.step("Writing " + count(len(toWrite), "section"))

	var tiles, middle sync.WaitGroup
	ownedBy := func(owner string) []reviewv2.Tile {
		var out []reviewv2.Tile
		for _, t := range planned {
			if t.Owner == owner {
				out = append(out, t)
			}
		}
		return out
	}
	startTiles := func(owner string, list []reviewv2.Tile) {
		for _, t := range list {
			tiles.Go(func() { o.tileLevel(ctx, t, owner) })
		}
	}
	// The overview's main diagram needs only the map.
	startTiles(reviewv2.OwnerOverview, ownedBy(reviewv2.OwnerOverview))
	for _, s := range sections {
		if s.Kind == "mechanical" && !reused[s.ID] {
			startTiles(s.ID, ownedBy(s.ID))
		}
		// A reused section's text is in: its mandatory tiles start now.
		if reused[s.ID] {
			startTiles(s.ID, mandatory[s.ID])
		}
	}
	// A written section's mandatory tiles start once its text is in, as its
	// own do, but never pass through it: a section cannot drop them.
	for _, s := range toWrite {
		middle.Go(func() {
			list := o.sectionLevel(ctx, s, ownedBy(s.ID))
			startTiles(s.ID, append(list, mandatory[s.ID]...))
		})
	}
	middle.Go(func() {
		o.contextLevel(ctx)
		startTiles(reviewv2.OwnerContext, append(ownedBy(reviewv2.OwnerContext), mandatory[reviewv2.OwnerContext]...))
	})
	middle.Wait()
	o.budget.close(1)
	if n := o.pendingTiles(); n > 0 {
		o.step("Drawing " + count(n, "tile"))
	}
	tiles.Wait()
	o.budget.close(2)
}

// mandatoryTiles adds a pending tile for each type of Run.Always that no
// tile of the plan or of a reused section has, by owner. Each stands on
// the logical section with the most hunks, resting on its hunks, or on the
// context, resting on every hunk, when there is no such section. The map's
// own tile of the type, where it planned one, is placed and briefed as the
// map chose; these are the ones it left out.
func (o *orchestration) mandatoryTiles(sections []reviewv2.Section) map[string][]reviewv2.Tile {
	if len(o.run.Always) == 0 {
		return nil
	}
	owner, hunks := reviewv2.OwnerContext, []string(nil)
	for _, s := range sections {
		if s.Kind == reviewv2.KindLogical && len(s.Hunks) > len(hunks) {
			owner, hunks = s.ID, s.Hunks
		}
	}
	if owner == reviewv2.OwnerContext {
		hunks = o.facts.refs.HunkIDs()
	}
	o.mu.Lock()
	planned := map[string]bool{}
	for _, t := range o.tiles {
		planned[t.Type] = true
	}
	o.mu.Unlock()
	taken := o.tileIDs()
	out := map[string][]reviewv2.Tile{}
	for _, name := range o.run.Always {
		kind, ok := o.registry.Get(name)
		if !ok || !kind.Dedicated || planned[name] {
			continue
		}
		planned[name] = true
		t := reviewv2.Tile{ID: mandatoryTileID(name, taken), Type: name, Owner: owner, Cites: hunkAnchors(hunks), State: reviewv2.TilePending,
			Shows: cut(kind.Title+" of this pull request, a tile filled on every review of it", MaxShows)}
		taken[t.ID] = true
		o.setTile(t)
		out[owner] = append(out[owner], t)
	}
	if len(out) > 0 {
		o.publish()
	}
	return out
}

// mandatoryTileID is tl_ and the type's name as a slug, made unique among
// taken.
func mandatoryTileID(name string, taken map[string]bool) string {
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 'a' - 'A'
		}
		return '_'
	}, name)
	if len(slug) > 50 {
		slug = slug[:50]
	}
	if slug == "" {
		slug = "tile"
	}
	id := "tl_" + slug
	for n := 2; taken[id]; n++ {
		id = fmt.Sprintf("tl_%s_%d", slug, n)
	}
	return id
}

// setSection changes a section under the lock.
func (o *orchestration) setSection(id string, change func(*reviewv2.Section)) {
	o.mu.Lock()
	if s := o.sections[id]; s != nil {
		change(s)
	}
	o.mu.Unlock()
}

// pendingTiles counts the tiles still being drawn.
func (o *orchestration) pendingTiles() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := 0
	for _, t := range o.tiles {
		if t.State == reviewv2.TilePending {
			n++
		}
	}
	return n
}

// setTile changes a tile under the lock, adding it when it is new.
func (o *orchestration) setTile(t reviewv2.Tile) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for i := range o.tiles {
		if o.tiles[i].ID == t.ID {
			o.tiles[i] = t
			return
		}
	}
	o.tiles = append(o.tiles, t)
}

// dropTile removes a tile.
func (o *orchestration) dropTile(id string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.tiles = slices.DeleteFunc(o.tiles, func(t reviewv2.Tile) bool { return t.ID == id })
}

// reuse keeps the previous version's text and tiles of each section whose
// hunks are unchanged and whose text was complete (reviewv2.
// ReusableSections). The map's tiles of a reused section give way to the
// previous ones.
func (o *orchestration) reuse(sections []reviewv2.Section) map[string]bool {
	reused := map[string]bool{}
	if len(o.run.Previous) == 0 || o.run.NoReuse {
		return reused
	}
	var withHunks []reviewv2.Section
	for _, s := range sections {
		if len(s.Hunks) > 0 && s.Kind == reviewv2.KindLogical {
			withHunks = append(withHunks, s)
		}
	}
	for id, r := range reviewv2.ReusableSections(o.run.Previous, withHunks) {
		if len(r.Section.Body) == 0 {
			continue
		}
		reused[id] = true
		o.setSection(id, func(sec *reviewv2.Section) {
			sec.Body, sec.Risks, sec.Details = r.Section.Body, r.Section.Risks, ""
		})
		o.record(LevelSections, id, StatusReused, "")
		o.mu.Lock()
		o.tiles = slices.DeleteFunc(o.tiles, func(t reviewv2.Tile) bool { return t.Owner == id })
		o.mu.Unlock()
		for _, t := range r.Tiles {
			o.setTile(t)
			o.record(LevelTiles, t.ID, StatusReused, "")
		}
	}
	if len(reused) > 0 {
		o.publish()
	}
	return reused
}

// mechanical writes the mechanical section's one prose block from its
// hunks' roles: no call, no risks.
func (o *orchestration) mechanical(s reviewv2.Section) {
	roles := map[string]bool{}
	locks := false
	for _, id := range s.Hunks {
		h := o.facts.hunks[id]
		roles[h.Role] = true
		base := h.Path[strings.LastIndex(h.Path, "/")+1:]
		if strings.HasSuffix(base, ".lock") || strings.HasSuffix(base, "-lock.json") || strings.HasSuffix(base, "-lock.yaml") || base == "go.sum" {
			locks = true
		}
	}
	var parts []string
	if roles["generated"] {
		parts = append(parts, "regenerates generated code")
	}
	if locks {
		parts = append(parts, "updates the lockfiles")
	}
	if roles["vendored"] {
		parts = append(parts, "updates vendored code")
	}
	if len(parts) == 0 {
		parts = append(parts, "renames, moves and reformats code without changing what it does")
	}
	summary := "This part " + joinWords(parts) + "."
	o.setSection(s.ID, func(sec *reviewv2.Section) {
		sec.Body, sec.Details, sec.Risks = []reviewv2.Block{{Type: reviewv2.BlockProse, Text: summary}}, "", []reviewv2.Risk{}
	})
	o.record(LevelSections, s.ID, StatusSkipped, "the host summarised the mechanical section")
	o.publish()
}

func joinWords(parts []string) string {
	switch len(parts) {
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// sectionLevel writes one section and returns its tiles, sharpened, for the
// tile level. A section that fails keeps the map's briefs for its tiles.
func (o *orchestration) sectionLevel(ctx context.Context, s reviewv2.Section, planned []reviewv2.Tile) []reviewv2.Tile {
	o.mu.Lock()
	p := o.plan
	o.mu.Unlock()
	input := basics(o.run.Bundle, o.run.Worktree) + "\n" + diffIndex(o.run.Bundle) + "\n" + mapBlock(p) + "\n" + sectionBrief(s, planned, p.Hunks)
	var result sectionResult
	c := call{level: LevelSections, id: s.ID, label: label("section", s.ID), input: input, schema: SectionSchema(o.registry.names()),
		instructions: o.prompt.Section + "\nYour reply's schema file: `" + agent.TaskSchemaFile(label("section", s.ID)) + "`.\n"}
	c.check = func(reply []byte, lenient bool) string {
		r, findings := checkSection(reply, s, planned, o.tileIDs(), o.facts, o.registry, lenient)
		if findings == "" {
			result = r
		}
		return findings
	}
	levelCtx, cancel := o.levelContext(ctx, LevelSections)
	defer cancel()
	reply, outcome, err := o.runCall(levelCtx, c)
	if reply == nil {
		o.setSection(s.ID, func(sec *reviewv2.Section) { sec.Details = "failed" })
		o.record(LevelSections, s.ID, StatusFailed, reason(err, outcome))
		o.publish()
		return planned
	}
	o.claimProposal(&result)
	o.setSection(s.ID, func(sec *reviewv2.Section) {
		sec.Body, sec.Risks, sec.Details = result.Body, result.Risks, ""
	})
	o.record(LevelSections, s.ID, StatusRan, "")
	var out []reviewv2.Tile
	for _, t := range planned {
		if why, dropped := result.Dropped[t.ID]; dropped {
			o.dropTile(t.ID)
			o.record(LevelTiles, t.ID, StatusSkipped, "dropped by its section: "+why)
			o.budget.expect(2, -1)
			continue
		}
		if brief, ok := result.Briefs[t.ID]; ok {
			t.Shows = brief.Shows
			if len(hunkCites(brief.Cites)) > 0 {
				t.Cites = brief.Cites
			}
		}
		o.setTile(t)
		out = append(out, t)
	}
	if extra := result.Propose; extra != nil {
		o.budget.expect(2, 1)
		out = append(out, *extra)
	}
	o.publish()
	return out
}

// tileIDs are the IDs of every tile the map planned or a section proposed.
func (o *orchestration) tileIDs() map[string]bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	ids := map[string]bool{}
	for _, t := range o.plan.Tiles {
		ids[t.ID] = true
	}
	for _, t := range o.tiles {
		ids[t.ID] = true
	}
	return ids
}

// claimProposal gives a section's proposed tile an ID no tile has now (a
// section written at the same time may have taken the one it checked), and
// the body's tile blocks that place it the new ID. The ID check, the
// MaxTiles check and the insert happen under one hold of o.mu, so sections
// running together cannot take the same ID or exceed the cap. A proposal
// over the cap is dropped from result.
func (o *orchestration) claimProposal(result *sectionResult) {
	extra := result.Propose
	if extra == nil {
		return
	}
	id := extra.ID
	o.mu.Lock()
	taken := map[string]bool{}
	for _, t := range o.plan.Tiles {
		taken[t.ID] = true
	}
	for _, t := range o.tiles {
		taken[t.ID] = true
	}
	for n := 2; taken[extra.ID]; n++ {
		extra.ID = fmt.Sprintf("%s_%d", id, n)
	}
	if len(o.tiles) >= MaxTiles {
		o.mu.Unlock()
		result.Propose = nil
		return
	}
	o.tiles = append(o.tiles, *extra)
	o.mu.Unlock()
	if extra.ID == id {
		return
	}
	for i := range result.Body {
		if result.Body[i].Type == reviewv2.BlockTile && result.Body[i].Tile == id {
			result.Body[i].Tile = extra.ID
		}
	}
}

// contextLevel writes "Where this fits". A context that fails is left out.
func (o *orchestration) contextLevel(ctx context.Context) {
	o.mu.Lock()
	p := o.plan
	o.mu.Unlock()
	var accepted *reviewv2.Context
	c := call{level: LevelContext, id: "context", label: "context", instructions: o.prompt.Context, schema: ContextSchema(),
		input: basics(o.run.Bundle, o.run.Worktree) + "\n" + diffIndex(o.run.Bundle) + "\n" + mapBlock(p)}
	c.check = func(reply []byte, lenient bool) string {
		x, findings := checkContext(reply, o.facts, lenient)
		if findings == "" {
			accepted = x
		}
		return findings
	}
	levelCtx, cancel := o.levelContext(ctx, LevelContext)
	defer cancel()
	reply, outcome, err := o.runCall(levelCtx, c)
	if reply == nil {
		o.record(LevelContext, "context", StatusFailed, reason(err, outcome))
		return
	}
	o.mu.Lock()
	o.context = accepted
	o.mu.Unlock()
	o.record(LevelContext, "context", StatusRan, "")
	o.publish()
}

// tileLevel fills one tile: the host's code excerpt, a library type's fill,
// or a native tile's agent.
func (o *orchestration) tileLevel(ctx context.Context, t reviewv2.Tile, owner string) {
	kind, ok := o.registry.Get(t.Type)
	switch {
	case !ok:
		t.State = reviewv2.TileFailed
		o.setTile(t)
		o.record(LevelTiles, t.ID, StatusFailed, "its type is not offered")
	case kind.Host:
		o.codeTile(t)
	case kind.Dedicated:
		o.dedicatedTile(ctx, t, kind, owner)
	default:
		o.nativeTile(ctx, t, kind, owner)
	}
	o.publish()
}

// codeTile is a code tile: its first hunk cite, with the lines the section
// chose, and its brief as its title. The host fills the text.
func (o *orchestration) codeTile(t reviewv2.Tile) {
	var hunk string
	var lines []int
	for _, raw := range t.Cites {
		var a anchor
		if json.Unmarshal(raw, &a) == nil && a.Type == "hunk" {
			hunk, lines = a.ID, a.Lines
			break
		}
	}
	if hunk == "" {
		t.State = reviewv2.TileFailed
		o.setTile(t)
		o.record(LevelTiles, t.ID, StatusFailed, "a code tile needs a hunk")
		return
	}
	data := reviewv2.CodeData{Hunk: hunk}
	if len(lines) == 2 {
		data.Lines = lines
	}
	t.Data, _ = json.Marshal(data)
	t.Title = cut(strings.TrimRight(clean(t.Shows), "."), MaxTileTitle)
	t.State = reviewv2.TileReady
	o.setTile(t)
	o.record(LevelTiles, t.ID, StatusSkipped, "the host fills a code tile")
}

// ownerOf is the text a tile stands beside, and the map for the overview's.
func (o *orchestration) ownerOf(owner string) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	switch owner {
	case reviewv2.OwnerOverview:
		return mapBlock(o.plan)
	case reviewv2.OwnerContext:
		return ownerText(nil, o.context)
	}
	if s := o.sections[owner]; s != nil {
		copied := *s
		return ownerText(&copied, nil)
	}
	return ""
}

// nativeTile draws a native tile through the shared tile prompt with its
// type's part and schema.
func (o *orchestration) nativeTile(ctx context.Context, t reviewv2.Tile, kind TileType, owner string) {
	brief := map[string]any{"id": t.ID, "type": t.Type, "shows": t.Shows, "hunks": hunkCites(t.Cites)}
	input := basics(o.run.Bundle, o.run.Worktree) + "\n" + diffIndex(o.run.Bundle) + "\nThe tile (model-written brief): " + jsonText(brief) + "\n\nWhat it stands beside:\n" + o.ownerOf(owner) + "\n"
	instructions := o.prompt.Tile + "\n" + kind.Fill(o.prompt) + "\nYour reply's schema file: `" + agent.TaskSchemaFile(label("tile", t.ID)) + "`.\n"
	var drawn reviewv2.Tile
	c := call{level: LevelTiles, id: t.ID, label: label("tile", t.ID), instructions: instructions, input: input, schema: kind.Schema}
	c.check = func(reply []byte, lenient bool) string {
		x, findings := checkNative(reply, kind.Name, o.facts, lenient)
		if findings == "" {
			drawn = x
		}
		return findings
	}
	levelCtx, cancel := o.levelContext(ctx, LevelTiles)
	defer cancel()
	reply, outcome, err := o.runCall(levelCtx, c)
	if reply == nil {
		t.State = reviewv2.TileFailed
		o.setTile(t)
		o.record(LevelTiles, t.ID, StatusFailed, reason(err, outcome))
		return
	}
	t.Title, t.Caption, t.Data, t.State = drawn.Title, drawn.Caption, drawn.Data, reviewv2.TileReady
	if len(drawn.Cites) > 0 {
		t.Cites = drawn.Cites
	}
	o.setTile(t)
	o.record(LevelTiles, t.ID, StatusRan, "")
}

// dedicatedTile fills a library or saved type through the library's fill
// path, with the tile's brief and owner.
func (o *orchestration) dedicatedTile(ctx context.Context, t reviewv2.Tile, kind TileType, owner string) {
	levelCtx, cancel := o.levelContext(ctx, LevelTiles)
	defer cancel()
	select {
	case o.sem <- struct{}{}:
	case <-levelCtx.Done():
		t.State = reviewv2.TileFailed
		o.setTile(t)
		o.record(LevelTiles, t.ID, StatusFailed, "the level's time ran out")
		return
	}
	budgets := o.budget.take(2)
	started := timeNow()
	prompts := o.run.FillPrompts
	if prompts == nil {
		prompts = fillPrompts{}
	}
	result := o.run.Generator.FillTile(levelCtx, agent.FillTileRun{
		Bundle: o.run.Bundle, Directory: o.dir, Entry: *kind.Entry, Prompts: prompts, Budgets: budgets, Redactor: o.redactor,
		Endpoint: o.run.Endpoint, Hunks: hunkCites(t.Cites), Brief: t.Shows, Owner: o.ownerOf(owner), Label: label("tile", t.ID),
		MCP: o.run.MCP, Shell: o.run.Shell, ReadDirs: readDirs(o.run.Worktree), Tools: o.tools,
	})
	<-o.sem
	o.budget.give(2, budgets, result.Outcome)
	o.account(LevelTiles, result.Outcome, started)
	if o.run.Record != nil {
		o.mu.Lock()
		o.run.Record(Call{Level: LevelTiles, ID: t.ID, Pass: 1, Prompt: "library fill of " + kind.Name, Input: t.Shows, Reply: result.Data, Findings: result.Reason})
		o.mu.Unlock()
	}
	switch result.State {
	case agent.FillReady:
		t.Title, t.Data, t.Definition, t.State = kind.Entry.Definition.Title, result.Data, kind.Entry.Encoded, reviewv2.TileReady
		o.setTile(t)
		o.record(LevelTiles, t.ID, StatusRan, "")
	case agent.FillNotApplies:
		o.dropTile(t.ID)
		o.record(LevelTiles, t.ID, StatusSkipped, "the type does not apply")
	default:
		t.State = reviewv2.TileFailed
		o.setTile(t)
		o.record(LevelTiles, t.ID, StatusFailed, result.Reason)
	}
}

// overviewLevel writes the overview by resuming the map's session, or in a
// fresh call with the map in its input. A failed overview falls back.
func (o *orchestration) overviewLevel(ctx context.Context) {
	o.step("Writing the overview")
	o.mu.Lock()
	p := o.plan
	sections := o.orderedSections()
	tiles := slices.Clone(o.tiles)
	session := o.mapSession
	o.mu.Unlock()
	results := overviewInput(sections, tiles, o.run.Bundle)
	o.budget.expect(3, 1)
	var accepted overviewResult
	check := func(reply []byte, lenient bool) string {
		r, findings := checkOverview(reply, o.facts, sections, tiles, lenient)
		if findings == "" {
			accepted = r
		}
		return findings
	}
	levelCtx, cancel := o.levelContext(ctx, LevelOverview)
	defer cancel()
	var reply []byte
	var outcome agent.Outcome
	var err error
	if session != "" && agent.KeepsSessions(o.run.Generator.Backend) {
		c := call{level: LevelOverview, id: "overview", label: "overview", resume: session, schema: OverviewSchema(), check: check,
			instructions: "You wrote the map of this pull request earlier in this session; the other calls are done.\n\n" + o.prompt.Overview,
			input:        results}
		reply, outcome, err = o.runCall(levelCtx, c)
	}
	if reply == nil && levelCtx.Err() == nil {
		c := call{level: LevelOverview, id: "overview", label: "overview", schema: OverviewSchema(), check: check,
			instructions: o.prompt.Overview,
			input:        basics(o.run.Bundle, o.run.Worktree) + "\n" + diffIndex(o.run.Bundle) + "\n" + mapBlock(p) + "\n" + results}
		reply, outcome, err = o.runCall(levelCtx, c)
	}
	if reply == nil {
		o.record(LevelOverview, "overview", StatusFailed, reason(err, outcome))
		return
	}
	o.mu.Lock()
	o.overview = &accepted
	o.mu.Unlock()
	o.record(LevelOverview, "overview", StatusRan, "")
}

// orderedSections are the sections in the map's order. o.mu is held.
func (o *orchestration) orderedSections() []reviewv2.Section {
	out := make([]reviewv2.Section, 0, len(o.plan.Sections))
	for _, s := range o.plan.Sections {
		if sec := o.sections[s.ID]; sec != nil {
			out = append(out, *sec)
		}
	}
	return out
}

// document is the authored document so far; final settles what is still
// pending (failed) and falls back for an overview that failed.
func (o *orchestration) document(final bool) reviewv2.Document {
	o.mu.Lock()
	defer o.mu.Unlock()
	doc := reviewv2.Document{Schema: reviewv2.Name, Sections: o.orderedSections(), Tiles: slices.Clone(o.tiles), Context: o.context,
		Overview: reviewv2.Overview{Screenshots: []string{}, Intent: []reviewv2.Intent{}}}
	doc.Summary = reviewv2.Summary{Title: o.run.Bundle.PullRequest.Title, ChangeKind: o.plan.ChangeKind}
	if final {
		for i := range doc.Sections {
			if doc.Sections[i].Details == "pending" {
				doc.Sections[i].Details = "failed"
			}
		}
		for i := range doc.Tiles {
			if doc.Tiles[i].State == reviewv2.TilePending {
				doc.Tiles[i].State = reviewv2.TileFailed
			}
		}
	}
	for i := range doc.Sections {
		s := &doc.Sections[i]
		if s.Body == nil {
			s.Body = []reviewv2.Block{}
		}
		if s.Risks == nil {
			s.Risks = []reviewv2.Risk{}
		}
	}
	switch {
	case o.overview != nil:
		doc.Summary.Text, doc.Summary.Verdict, doc.Summary.RiskWhy = o.overview.Text, o.overview.Verdict, o.overview.RiskWhy
		doc.Overview = o.overview.Overview
	case final:
		doc.Summary.Text, doc.Summary.Verdict, doc.Summary.RiskWhy = fallbackOverview(doc.Sections)
	}
	if final && doc.Overview.Diagram == nil {
		doc.Overview.Diagram = mainDiagram(doc.Tiles)
	}
	gen := o.out.Generation
	gen.Sections, gen.Tiles = cloneRecords(gen.Sections), cloneRecords(gen.Tiles)
	doc.Generation = &gen
	return doc
}

func cloneRecords(m map[string]reviewv2.CallRecord) map[string]reviewv2.CallRecord {
	if m == nil {
		return nil
	}
	out := make(map[string]reviewv2.CallRecord, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// fallbackOverview is the overview of a run whose overview failed: the
// sections' gists as the lede, look first without points, the reason of
// the highest level as why.
func fallbackOverview(sections []reviewv2.Section) (string, *reviewv2.Verdict, string) {
	var gists []string
	why, best := "", 0
	for _, s := range sections {
		if s.Gist != "" && s.Kind != "mechanical" {
			gists = append(gists, strings.TrimRight(s.Gist, ".")+".")
		}
		if s.Level != nil && *s.Level > best && len(s.LevelReasons) > 0 {
			best, why = *s.Level, s.LevelReasons[0].Text
		}
	}
	if len(gists) == 0 {
		for _, s := range sections {
			if s.Gist != "" {
				gists = append(gists, strings.TrimRight(s.Gist, ".")+".")
			}
		}
	}
	return strings.Join(gists, " "), &reviewv2.Verdict{Decision: "look_first", Points: []reviewv2.VerdictPoint{}}, why
}

// mainDiagram is the overview's diagram when the overview names none: its
// own tile when ready, else the first ready native section tile, else none.
func mainDiagram(tiles []reviewv2.Tile) *string {
	for _, t := range tiles {
		if t.Owner == reviewv2.OwnerOverview && t.State == reviewv2.TileReady {
			id := t.ID
			return &id
		}
	}
	for _, t := range tiles {
		if t.State == reviewv2.TileReady && slices.Contains(view.NativeTypes, t.Type) && t.Owner != reviewv2.OwnerContext {
			id := t.ID
			return &id
		}
	}
	return nil
}

// complete completes a document (Run.Complete or reviewv2.Complete).
func (o *orchestration) complete(doc reviewv2.Document) (reviewv2.Document, error) {
	complete := o.run.Complete
	if complete == nil {
		complete = reviewv2.Complete
	}
	return complete(doc, o.run.CompleteInput)
}

// publish asks for a draft of the document so far; the drafts goroutine
// coalesces requests that land while one is delivered.
func (o *orchestration) publish() {
	if o.drafts == nil {
		return
	}
	select {
	case o.drafts <- struct{}{}:
	default:
	}
}

func (o *orchestration) deliverDrafts() {
	defer close(o.draftsDone)
	for range o.drafts {
		func() {
			defer func() { _ = recover() }()
			if doc, err := o.complete(o.document(false)); err == nil {
				o.run.Draft(doc)
			}
		}()
	}
}

func (o *orchestration) stopDrafts() {
	if o.drafts == nil {
		return
	}
	close(o.drafts)
	<-o.draftsDone
	o.drafts = nil
}

// workspaceSection says where the calls run: a git worktree at the head,
// with the merge base named, or the unpacked trees; and whether they may run
// commands that read.
func workspaceSection(w Worktree, shell bool) string {
	var b strings.Builder
	b.WriteString("## Your workspace\n\n")
	if w.Dir != "" {
		fmt.Fprintf(&b, "Your working directory is a git worktree of the repository at the pull request's head (`%s`), with its full history. The merge base is `%s`: `git diff %s..HEAD` is the pull request's change, and `git log`, `git show` and `git blame` work as in any clone. `bundle.json` beside the code holds the pull request as one JSON document.\n", short(w.Head), short(w.MergeBase), short(w.MergeBase))
	} else {
		b.WriteString("Your working directory holds `head/`, the repository at the pull request's head, `merge-base/`, the repository at its merge base (both without git history), and `bundle.json`, the pull request as one JSON document.\n")
	}
	if shell {
		b.WriteString("\nYou may use git and the shell to read freely (git, rg, ls, cat, find and the like), as much as the work needs. Commands run in a sandbox that writes nowhere outside the working directory and has no network; do not run tests or builds.\n")
	}
	return b.String()
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// fillPrompts stand in for a run without Run.FillPrompts: a dedicated tile
// then fails, and the run goes on.
type fillPrompts struct{}

func (fillPrompts) TileAnalysisInstructions(context.Context, string) (agent.TileAnalysisPrompt, error) {
	return agent.TileAnalysisPrompt{}, fmt.Errorf("no library fill prompt")
}
