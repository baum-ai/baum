package orchestrate

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/baum-ai/baum/reviewv2"
	"github.com/baum-ai/baum/tiles/view"
)

// The replies of the levels, as the model writes them.

type mapReply struct {
	ChangeKind string        `json:"change_kind"`
	Sections   []mapSection  `json:"sections"`
	Tiles      []plannedTile `json:"tiles"`
}

type mapSection struct {
	ID           string        `json:"id"`
	Title        string        `json:"title"`
	Kind         string        `json:"kind"`
	Hunks        []string      `json:"hunks"`
	Gist         string        `json:"gist"`
	Scope        *string       `json:"scope"`
	Level        *int          `json:"level"`
	LevelReasons []reasonReply `json:"level_reasons"`
}

type reasonReply struct {
	Text  string            `json:"text"`
	Cites []json.RawMessage `json:"cites"`
}

type plannedTile struct {
	ID    string   `json:"id"`
	Type  string   `json:"type"`
	Owner string   `json:"owner"`
	Shows string   `json:"shows"`
	Hunks []string `json:"hunks"`
}

type sectionReply struct {
	Body    []blockReply `json:"body"`
	Risks   []riskReply  `json:"risks"`
	Tiles   []tileBrief  `json:"tiles"`
	Propose *proposal    `json:"propose"`
}

// blockReply is one block of a section's body as the model writes it: the
// fields its type does not use are null.
type blockReply struct {
	Type  string       `json:"type"`
	Title *string      `json:"title"`
	Text  *string      `json:"text"`
	Items []pointReply `json:"items"`
	Tile  *string      `json:"tile"`
}

type pointReply struct {
	Text  string            `json:"text"`
	More  *string           `json:"more"`
	Cites []json.RawMessage `json:"cites"`
}

type riskReply struct {
	ID       string            `json:"id"`
	Tier     string            `json:"tier"`
	Category string            `json:"category"`
	Text     string            `json:"text"`
	Cites    []json.RawMessage `json:"cites"`
}

type tileBrief struct {
	ID     string            `json:"id"`
	Shows  string            `json:"shows"`
	Cites  []json.RawMessage `json:"cites"`
	Drop   bool              `json:"drop"`
	Reason *string           `json:"reason"`
}

type proposal struct {
	ID    string            `json:"id"`
	Type  string            `json:"type"`
	Shows string            `json:"shows"`
	Cites []json.RawMessage `json:"cites"`
}

type contextReply struct {
	Area      contextPart `json:"area"`
	ReachedBy contextPart `json:"reached_by"`
	Contracts contextPart `json:"contracts"`
	Related   contextPart `json:"related"`
}

type contextPart struct {
	Text  string   `json:"text"`
	Files []string `json:"files"`
}

type overviewReply struct {
	Summary string `json:"summary"`
	Verdict struct {
		Decision string `json:"decision"`
		Text     string `json:"text"`
		Points   []struct {
			Text    string  `json:"text"`
			Section *string `json:"section"`
		} `json:"points"`
	} `json:"verdict"`
	RiskWhy     string   `json:"risk_why"`
	Screenshots []string `json:"screenshots"`
	Diagram     *string  `json:"diagram"`
	Intent      []struct {
		Session string `json:"session"`
		Prompt  int    `json:"prompt"`
		Asked   string `json:"asked"`
		Got     string `json:"got"`
	} `json:"intent"`
}

var (
	sectionID = regexp.MustCompile(`^sec_[a-z0-9_]{1,60}$`)
	tileID    = regexp.MustCompile(`^tl_[a-z0-9_]{1,60}$`)
	riskID    = regexp.MustCompile(`^rsk_[a-z0-9_]{1,60}$`)
)

// decode reads a reply into its type, or says why it does not.
func decode(reply []byte, into any) string {
	if err := json.Unmarshal(reply, into); err != nil {
		return "/: the reply does not have its schema's shape: " + err.Error()
	}
	return ""
}

// plan is an accepted map.
type plan struct {
	ChangeKind string
	Sections   []reviewv2.Section
	Tiles      []reviewv2.Tile
	// Hunks are the hunks each planned tile rests on, by tile ID.
	Hunks map[string][]string
}

// checkMap checks a map against the pull request and the registry: every
// hunk in a section, ids and owners that exist, the limits of every text.
// It returns the plan, and the findings that send it back.
func checkMap(reply []byte, f *facts, r *Registry, lenient bool) (plan, string) {
	var m mapReply
	if problem := decode(reply, &m); problem != "" {
		return plan{}, problem
	}
	c := &checker{lenient: lenient}
	p := plan{ChangeKind: m.ChangeKind, Hunks: map[string][]string{}}
	if !slices.Contains(reviewv2.ChangeKinds, m.ChangeKind) {
		c.add("/change_kind", "must be one of %s", strings.Join(reviewv2.ChangeKinds, ", "))
	}
	ids := map[string]bool{}
	covered := map[string]bool{}
	mechanical := 0
	for i, s := range m.Sections {
		at := fmt.Sprintf("/sections/%d", i)
		if !sectionID.MatchString(s.ID) {
			c.add(at+"/id", "must be sec_ and a slug of lowercase letters, digits and underscores")
		}
		if ids[s.ID] {
			c.add(at+"/id", "%s is another section's id", s.ID)
		}
		ids[s.ID] = true
		section := reviewv2.Section{ID: s.ID, Kind: s.Kind, Hunks: []string{}, LevelReasons: []reviewv2.LevelReason{}, Body: []reviewv2.Block{}, Risks: []reviewv2.Risk{}}
		section.Title = c.title(at+"/title", s.Title, reviewv2.MaxTitle)
		section.Gist = c.text(at+"/gist", s.Gist, reviewv2.MaxGist, true)
		if s.Scope != nil {
			section.Scope = c.text(at+"/scope", *s.Scope, reviewv2.MaxScope, false)
		}
		for _, h := range s.Hunks {
			if _, ok := f.hunks[h]; !ok {
				c.add(at+"/hunks", "%s is not a hunk of this pull request", h)
				continue
			}
			if !slices.Contains(section.Hunks, h) {
				section.Hunks = append(section.Hunks, h)
			}
			covered[h] = true
		}
		switch s.Kind {
		case "mechanical":
			mechanical++
		case "logical":
			if len(section.Hunks) > 0 {
				if s.Level == nil || *s.Level < 1 || *s.Level > 5 {
					c.add(at+"/level", "a logical section with hunks needs a level from 1 to 5")
				} else {
					level := *s.Level
					section.Level = &level
				}
				if len(s.LevelReasons) == 0 {
					c.add(at+"/level_reasons", "a logical section with hunks needs one to three reasons for its level")
				}
				for j, reason := range s.LevelReasons {
					text := c.text(fmt.Sprintf("%s/level_reasons/%d/text", at, j), reason.Text, reviewv2.MaxLevelReason, true)
					kept, _ := f.cites(reason.Cites)
					section.LevelReasons = append(section.LevelReasons, reviewv2.LevelReason{Text: text, Cites: kept})
				}
			}
		}
		p.Sections = append(p.Sections, section)
	}
	if mechanical > 1 {
		c.add("/sections", "has %d mechanical sections; put every mechanical hunk in one", mechanical)
	}
	var missing []string
	for _, h := range f.order {
		if !covered[h] {
			missing = append(missing, h)
		}
	}
	if len(missing) > 0 {
		shown := missing[:min(len(missing), 30)]
		c.add("/sections", "these hunks are in no section; put each in the section of the behaviour it serves: %s", strings.Join(shown, ", "))
	}
	p.Tiles, p.Hunks = checkPlannedTiles(c, m.Tiles, ids, f, r)
	c.merge(reviewv2.CheckMap(p.ChangeKind, p.Sections, p.Tiles, f.refsFor(r)))
	if len(c.findings) > 0 {
		return p, c.String()
	}
	return p, ""
}

// checkPlannedTiles checks the map's tiles: at most reviewv2.MaxTiles, one owned by
// the overview, MaxSectionTiles per section, types and hunks that exist.
func checkPlannedTiles(c *checker, planned []plannedTile, sections map[string]bool, f *facts, r *Registry) ([]reviewv2.Tile, map[string][]string) {
	out := []reviewv2.Tile{}
	hunks := map[string][]string{}
	if len(planned) > reviewv2.MaxTiles {
		c.add("/tiles", "has %d tiles; plan at most %d, only where a tile shows what text cannot", len(planned), reviewv2.MaxTiles)
	}
	owned := map[string]int{}
	seen := map[string]bool{}
	for i, t := range planned {
		at := fmt.Sprintf("/tiles/%d", i)
		if !tileID.MatchString(t.ID) || seen[t.ID] {
			c.add(at+"/id", "must be tl_ and a slug no other tile has")
		}
		seen[t.ID] = true
		if _, ok := r.Get(t.Type); !ok {
			c.add(at+"/type", "%s is not an offered type", t.Type)
		}
		switch {
		case t.Owner == reviewv2.OwnerOverview || t.Owner == reviewv2.OwnerContext:
		case sections[t.Owner]:
		default:
			c.add(at+"/owner", "must be a section id, overview or context")
		}
		owned[t.Owner]++
		if t.Owner == reviewv2.OwnerOverview && owned[t.Owner] == 2 {
			c.add(at+"/owner", "the overview has one main diagram at most")
		}
		if sections[t.Owner] && owned[t.Owner] == MaxSectionTiles+1 {
			c.add(at+"/owner", "%s has more than %d tiles; keep the ones that explain most", t.Owner, MaxSectionTiles)
		}
		shows := c.text(at+"/shows", t.Shows, reviewv2.MaxShows, true)
		var rests []string
		for _, h := range t.Hunks {
			if _, ok := f.hunks[h]; ok && !slices.Contains(rests, h) {
				rests = append(rests, h)
			}
		}
		if sections[t.Owner] && len(rests) == 0 {
			c.add(at+"/hunks", "a section's tile rests on at least one of the pull request's hunks")
		}
		hunks[t.ID] = rests
		out = append(out, reviewv2.Tile{ID: t.ID, Type: t.Type, Owner: t.Owner, Shows: shows, Cites: hunkAnchors(rests), State: reviewv2.TilePending})
	}
	return out, hunks
}

// sectionResult is a written section: its body and risks and what it asks
// of its tiles.
type sectionResult struct {
	Body    []reviewv2.Block
	Risks   []reviewv2.Risk
	Briefs  map[string]tileBrief
	Propose *reviewv2.Tile
	Dropped map[string]string
}

// checkSection checks a section agent's reply. taken are the IDs of the
// tiles the document already has, which a proposed tile's ID may not repeat
// (it is made unique).
//
// A tile block must name a tile of this section that is kept: planned and
// not dropped by this reply, or the tile it proposes. One that names
// anything else is dropped, and a tile the body places twice is placed
// where it first is.
func checkSection(reply []byte, section reviewv2.Section, planned []reviewv2.Tile, taken map[string]bool, f *facts, r *Registry, lenient bool) (sectionResult, string) {
	var s sectionReply
	if problem := decode(reply, &s); problem != "" {
		return sectionResult{}, problem
	}
	c := &checker{lenient: lenient}
	out := sectionResult{Briefs: map[string]tileBrief{}, Dropped: map[string]string{}, Body: []reviewv2.Block{}, Risks: []reviewv2.Risk{}}
	slug := strings.TrimPrefix(section.ID, "sec_")
	ids := map[string]bool{}
	for i, risk := range s.Risks {
		at := fmt.Sprintf("/risks/%d", i)
		if !riskID.MatchString(risk.ID) || ids[risk.ID] {
			c.add(at+"/id", "must be rsk_ and a slug no other risk of the section has")
		}
		ids[risk.ID] = true
		if !slices.Contains(reviewv2.RiskTiers, risk.Tier) {
			c.add(at+"/tier", "must be one of %s", strings.Join(reviewv2.RiskTiers, ", "))
		}
		body := c.text(at+"/text", risk.Text, reviewv2.MaxRiskText, true)
		kept, _ := f.cites(risk.Cites)
		if len(hunkCites(kept)) == 0 {
			c.add(at+"/cites", "a risk cites at least one hunk of the pull request at the lines responsible")
		}
		out.Risks = append(out.Risks, reviewv2.Risk{ID: risk.ID, Tier: risk.Tier, Category: risk.Category, Text: body, Cites: kept})
	}
	plannedIDs := map[string]reviewv2.Tile{}
	for _, t := range planned {
		plannedIDs[t.ID] = t
	}
	for i, brief := range s.Tiles {
		at := fmt.Sprintf("/tiles/%d", i)
		t, ok := plannedIDs[brief.ID]
		if !ok {
			continue
		}
		if brief.Drop {
			reason := "the section agent dropped it"
			if brief.Reason != nil {
				reason = clean(*brief.Reason)
			}
			out.Dropped[brief.ID] = cut(reason, MaxReasonText)
			continue
		}
		if t.Type == TypeCode {
			brief.Shows = c.title(at+"/shows", brief.Shows, reviewv2.MaxTileTitle)
		} else {
			brief.Shows = c.text(at+"/shows", brief.Shows, reviewv2.MaxShows, true)
		}
		brief.Cites, _ = f.cites(brief.Cites)
		out.Briefs[brief.ID] = brief
	}
	// placeable are the tiles a tile block may name, by the ID the model
	// wrote: the section's planned tiles it did not drop, and its proposal.
	placeable := map[string]string{}
	for _, t := range planned {
		if _, dropped := out.Dropped[t.ID]; !dropped {
			placeable[t.ID] = t.ID
		}
	}
	if p := s.Propose; p != nil {
		t, ok := r.Get(p.Type)
		if !ok {
			c.add("/propose/type", "%s is not an offered type", p.Type)
		} else {
			shows := c.text("/propose/shows", p.Shows, reviewv2.MaxShows, true)
			kept, _ := f.cites(p.Cites)
			if len(hunkCites(kept)) == 0 {
				c.add("/propose/cites", "a proposed tile cites at least one hunk")
			}
			id := p.ID
			if !tileID.MatchString(id) {
				id = "tl_" + slug + "_more"
			}
			final := id
			for n := 2; taken[final] || plannedIDs[final].ID != ""; n++ {
				final = fmt.Sprintf("%s_%d", id, n)
			}
			// A body that names the ID the model gave places the proposal,
			// unless that ID is one of the section's planned tiles.
			if _, isPlanned := plannedIDs[p.ID]; !isPlanned {
				placeable[p.ID] = final
			}
			out.Propose = &reviewv2.Tile{ID: final, Type: t.Name, Owner: section.ID, Shows: shows, Cites: kept, State: reviewv2.TilePending}
		}
	}
	placed := map[string]bool{}
	for i, block := range s.Body {
		at := fmt.Sprintf("/body/%d", i)
		b := reviewv2.Block{Type: block.Type}
		switch block.Type {
		case reviewv2.BlockProse, reviewv2.BlockNote:
			limit := reviewv2.MaxProse
			if block.Type == reviewv2.BlockNote {
				limit = reviewv2.MaxNote
			}
			b.Text = c.inline(at+"/text", deref(block.Text), limit, false)
			if b.Text == "" {
				continue
			}
			b.Title = c.inline(at+"/title", deref(block.Title), reviewv2.MaxBlockTitle, false)
		case reviewv2.BlockPoints:
			b.Title = c.inline(at+"/title", deref(block.Title), reviewv2.MaxBlockTitle, false)
			for j, item := range block.Items {
				pat := fmt.Sprintf("%s/items/%d", at, j)
				point := reviewv2.Point{Text: c.inline(pat+"/text", item.Text, reviewv2.MaxPoint, false), More: c.inline(pat+"/more", deref(item.More), reviewv2.MaxPointDetail, false)}
				if point.Text == "" {
					continue
				}
				point.Cites, _ = f.cites(item.Cites)
				b.Items = append(b.Items, point)
			}
			if len(b.Items) == 0 {
				continue
			}
		case reviewv2.BlockTile:
			id, ok := placeable[deref(block.Tile)]
			if !ok || placed[id] {
				continue
			}
			placed[id] = true
			b.Tile = id
		default:
			continue
		}
		out.Body = append(out.Body, b)
	}
	c.merge(reviewv2.CheckSection(reviewv2.Section{ID: section.ID, Kind: section.Kind, Body: out.Body, Risks: out.Risks}, f.refs))
	if len(c.findings) > 0 {
		return out, c.String()
	}
	// What the checks let through is capped: blocks at MaxBlocks, points at
	// MaxPoints.
	out.Body, _ = reviewv2.CheckBody(out.Body, f.refs)
	return out, ""
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// checkContext checks the context agent's reply: area written, every part
// within its limits and its files in the checkout (others are dropped).
func checkContext(reply []byte, f *facts, lenient bool) (*reviewv2.Context, string) {
	var x contextReply
	if problem := decode(reply, &x); problem != "" {
		return nil, problem
	}
	c := &checker{lenient: lenient}
	part := func(name string, p contextPart, required bool) reviewv2.ContextPart {
		out := reviewv2.ContextPart{Text: c.text("/"+name+"/text", p.Text, reviewv2.MaxContextPart, required), Files: []string{}}
		for _, path := range p.Files {
			path = strings.TrimPrefix(strings.TrimPrefix(path, "head/"), "merge-base/")
			if f.hasPath(path) && !slices.Contains(out.Files, path) && len(out.Files) < reviewv2.MaxContextFiles {
				out.Files = append(out.Files, path)
			}
		}
		if out.Text == "" {
			out.Files = []string{}
		}
		return out
	}
	out := &reviewv2.Context{
		Area: part("area", x.Area, true), ReachedBy: part("reached_by", x.ReachedBy, false),
		Contracts: part("contracts", x.Contracts, false), Related: part("related", x.Related, false),
	}
	c.merge(reviewv2.CheckContext(*out, f.refs))
	if len(c.findings) > 0 {
		return out, c.String()
	}
	return out, ""
}

// checkNative checks a native tile agent's reply (view.CheckDiagram): its
// title and caption, its data, and every text in it for the copy rules.
func checkNative(reply []byte, kind string, f *facts, lenient bool) (reviewv2.Tile, string) {
	var d view.ReplyDiagram
	if problem := decode(reply, &d); problem != "" {
		return reviewv2.Tile{}, problem
	}
	d.Type = kind
	c := &checker{lenient: lenient}
	title, data, _, problem := view.CheckDiagram(d, func(id string) bool { _, ok := f.hunks[id]; return ok })
	if problem != "" {
		c.add("/", "the tile is not drawable: %s", problem)
		return reviewv2.Tile{}, c.String()
	}
	out := reviewv2.Tile{Type: kind, Data: data}
	out.Title = c.title("/title", title, reviewv2.MaxTileTitle)
	if d.Caption != nil {
		out.Caption = c.text("/caption", *d.Caption, reviewv2.MaxCaption, false)
	}
	c.dataStrings("/", data)
	// The data's source anchors are hunk IDs, which reviewv2.CheckTile reads
	// as text naming a hunk: it checks the data without them (the tile's
	// cites carry them).
	c.merge(reviewv2.CheckTile(reviewv2.Tile{ID: "tl_tile", Type: kind, Title: out.Title, Caption: out.Caption, Data: withoutSource(data), State: reviewv2.TileReady}, f.refs))
	var source struct {
		Source []view.Anchor `json:"source"`
	}
	_ = json.Unmarshal(data, &source)
	out.Cites = []json.RawMessage{}
	for _, a := range source.Source {
		encoded, _ := json.Marshal(a)
		out.Cites = append(out.Cites, encoded)
	}
	if len(c.findings) > 0 {
		return out, c.String()
	}
	return out, ""
}

// withoutSource is a native tile's data without its source anchors.
func withoutSource(data json.RawMessage) json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return data
	}
	fields["source"] = json.RawMessage(`[]`)
	out, err := json.Marshal(fields)
	if err != nil {
		return data
	}
	return out
}

// overviewResult is an accepted overview.
type overviewResult struct {
	Text     string
	Verdict  *reviewv2.Verdict
	RiskWhy  string
	Overview reviewv2.Overview
}

// checkOverview checks the overview's reply; ids it names that do not
// exist are dropped (the diagram falls back to the host's choice).
func checkOverview(reply []byte, f *facts, docSections []reviewv2.Section, tiles []reviewv2.Tile, lenient bool) (overviewResult, string) {
	sections, ready := map[string]bool{}, map[string]bool{}
	for _, s := range docSections {
		sections[s.ID] = true
	}
	for _, t := range tiles {
		if t.State == reviewv2.TileReady {
			ready[t.ID] = true
		}
	}
	var o overviewReply
	if problem := decode(reply, &o); problem != "" {
		return overviewResult{}, problem
	}
	c := &checker{lenient: lenient}
	out := overviewResult{Overview: reviewv2.Overview{Screenshots: []string{}, Intent: []reviewv2.Intent{}}}
	out.Text = c.text("/summary", o.Summary, reviewv2.MaxSummary, true)
	if !slices.Contains(reviewv2.Decisions, o.Verdict.Decision) {
		c.add("/verdict/decision", "must be one of %s", strings.Join(reviewv2.Decisions, ", "))
	}
	verdict := &reviewv2.Verdict{Decision: o.Verdict.Decision, Points: []reviewv2.VerdictPoint{}}
	verdict.Text = c.text("/verdict/text", o.Verdict.Text, reviewv2.MaxVerdictText, true)
	for i, p := range o.Verdict.Points {
		point := reviewv2.VerdictPoint{Text: c.text(fmt.Sprintf("/verdict/points/%d/text", i), p.Text, reviewv2.MaxPointText, true)}
		if p.Section != nil && sections[*p.Section] {
			point.Section = *p.Section
		}
		verdict.Points = append(verdict.Points, point)
	}
	out.Verdict = verdict
	out.RiskWhy = c.text("/risk_why", o.RiskWhy, reviewv2.MaxRiskWhy, true)
	for _, id := range o.Screenshots {
		if f.images[id] && !slices.Contains(out.Overview.Screenshots, id) {
			out.Overview.Screenshots = append(out.Overview.Screenshots, id)
		}
	}
	if o.Diagram != nil && ready[*o.Diagram] {
		id := *o.Diagram
		out.Overview.Diagram = &id
	}
	for i, in := range o.Intent {
		if !f.prompts[in.Session][in.Prompt] {
			continue
		}
		at := fmt.Sprintf("/intent/%d", i)
		out.Overview.Intent = append(out.Overview.Intent, reviewv2.Intent{Session: in.Session, Prompt: in.Prompt,
			Asked: c.text(at+"/asked", in.Asked, reviewv2.MaxIntentText, true), Got: c.text(at+"/got", in.Got, reviewv2.MaxIntentText, true)})
	}
	c.merge(reviewv2.CheckOverview(reviewv2.Summary{Text: out.Text, Verdict: out.Verdict, RiskWhy: out.RiskWhy}, out.Overview, docSections, tiles, f.refs))
	if len(c.findings) > 0 {
		return out, c.String()
	}
	return out, ""
}
