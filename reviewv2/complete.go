package reviewv2

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/schema"
)

// CompleteInput is what the host knows besides the authored document.
type CompleteInput struct {
	// Bundle is the assembled bundle the run read (bundle.json bytes), from
	// which the header, index and stats are built as baum.review/1's are.
	Bundle []byte
	// Previous is the previous version's stored document (baum.review/1 or
	// baum.review/2), or nil, for the sections' state and the risks' history.
	// Without it, the bundle's own previous version is read, if any.
	Previous []byte
	// Version is the number of the version being written.
	Version int
	// Generator is the review header's generator object (model, prompt version and the rest), as baum.review/1's.
	// Its model and prompt_version are kept; the host writes the rest.
	Generator []byte
	// Now is when the version is generated (review.generated_at); zero is
	// the time Complete runs.
	Now time.Time
}

// Complete turns an authored document (what the levels wrote, with each
// section's details and each tile's state as they ended) into the stored
// one: the header, index and stats from the bundle; the risk score (rubric
// risk-v1 over the sections' levels and the risks' tiers); the reading time;
// each section's derived facts and the risks' history across versions; the
// text of code tiles from the diff. It never calls a model.
//
// The document layer implements it (oss/reviewv2); the orchestration calls it
// once at the end of a run and on every draft it publishes.
//
// The risks' history is baum.review/1's (reconcileAuthoredHistory): a risk
// keeps the version it was introduced in; a previous risk the section no
// longer names is kept, mitigated in this version, unless the section's
// details are pending or failed, when it is kept as it was. A section of a
// new ID over a previous baum.review/2 section's code (RenamedSections)
// continues that section's risks as a section of its ID would; any other
// previous section the map no longer has is not carried. Each section's derived
// state compares its hunks with the previous version's section of the same
// ID, or the one it continues (new, unchanged or changed), and its review
// score is baum.review/1's.
// A tile without a state is ready when it has data and pending otherwise; a
// code tile whose hunk the diff does not have is unavailable.
func Complete(authored Document, in CompleteInput) (Document, error) {
	var b bundle.Bundle
	if err := json.Unmarshal(in.Bundle, &b); err != nil {
		return Document{}, fmt.Errorf("reviewv2: reading the bundle: %w", err)
	}
	if in.Version < 1 {
		return Document{}, errors.New("reviewv2: the version must be at least 1")
	}
	d := clone(authored)
	d.Schema = Name
	prior, err := previousOf(in.Previous, b.Previous)
	if err != nil {
		return Document{}, err
	}
	// A section renamed over a previous section's code continues its risks.
	var renamed map[string]string
	if IsV2(in.Previous) {
		renamed = RenamedSections(in.Previous, d.Sections, RefsOf(&b))
	}
	handedOff, handedIn := handOvers(prior, d.Sections, renamed)
	for i := range d.Sections {
		s := &d.Sections[i]
		if s.Hunks == nil {
			s.Hunks = []string{}
		}
		if s.LevelReasons == nil {
			s.LevelReasons = []LevelReason{}
		}
		from := s.ID
		if old, ok := renamed[s.ID]; ok {
			from = old
		}
		old, found := prior.sections[from]
		s.Risks = reconcileRisks(old.Risks, s.Risks, found, prior.version, in.Version, s.Details != "" || len(s.Hunks) == 0, handedOff[from], handedIn[s.ID])
	}
	completeTiles(&d, &b)
	EnsureBodies(&d)

	var generator struct {
		Model         string `json:"model"`
		PromptVersion string `json:"prompt_version"`
	}
	if len(in.Generator) > 0 {
		if err := json.Unmarshal(in.Generator, &generator); err != nil {
			return Document{}, fmt.Errorf("reviewv2: reading the generator: %w", err)
		}
	}
	revision, err := schema.Review2Revision()
	if err != nil {
		return Document{}, err
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	host := review.HostInput{Version: in.Version, PreviousVersion: prior.version, PreviousHunks: map[string][]string{}, Model: generator.Model, PromptVersion: generator.PromptVersion, SchemaRevision: revision, Now: now}
	if prior.version == 0 && in.Version > 1 && len(prior.sections) > 0 {
		host.PreviousVersion = in.Version - 1
	}
	for id, s := range prior.sections {
		host.PreviousHunks[id] = s.Hunks
	}
	// A renamed section's state compares its hunks with the section it
	// continues.
	for next, old := range renamed {
		if s, ok := prior.sections[old]; ok {
			host.PreviousHunks[next] = s.Hunks
		}
	}
	var sections []review.HostSection
	for _, s := range d.Sections {
		hs := review.HostSection{ID: s.ID, Kind: s.Kind, Hunks: s.Hunks, Level: s.Level, Details: s.Details}
		for _, r := range s.LevelReasons {
			hs.Reasons = append(hs.Reasons, r.Text)
		}
		for _, r := range s.Risks {
			hs.Risks = append(hs.Risks, review.HostRisk{ID: r.ID, Tier: r.Tier, Text: r.Text, State: r.State, Anchors: r.Cites})
		}
		sections = append(sections, hs)
	}
	parts, err := review.HostParts(&b, sections, host)
	if err != nil {
		return Document{}, err
	}
	d.Review, d.Index, d.Stats = parts.Review, parts.Index, parts.Stats
	d.Summary.Title = b.PullRequest.Title
	d.Summary.RiskScore = parts.RiskScore
	for i := range d.Sections {
		d.Sections[i].Derived = parts.Derived[d.Sections[i].ID]
	}
	if d.Generation == nil {
		// A generated document always says how its levels went; one
		// converted from baum.review/1 never does.
		d.Generation = &Generation{}
	}
	minutes := ReviewMinutes(d)
	d.Summary.ReviewMinutes = &minutes
	normalizeEmpty(&d)
	return d, nil
}

// clone copies a document deeply enough that completing it leaves the
// authored one as it was.
func clone(d Document) Document {
	encoded, err := json.Marshal(d)
	if err != nil {
		return d
	}
	var out Document
	if json.Unmarshal(encoded, &out) != nil {
		return d
	}
	return out
}

// normalizeEmpty gives the lists a document always has their empty value.
func normalizeEmpty(d *Document) {
	if d.Sections == nil {
		d.Sections = []Section{}
	}
	if d.Tiles == nil {
		d.Tiles = []Tile{}
	}
	if d.Overview.Screenshots == nil {
		d.Overview.Screenshots = []string{}
	}
	if d.Overview.Intent == nil {
		d.Overview.Intent = []Intent{}
	}
	if v := d.Summary.Verdict; v != nil && v.Points == nil {
		v.Points = []VerdictPoint{}
	}
	if c := d.Context; c != nil {
		for _, part := range []*ContextPart{&c.Area, &c.ReachedBy, &c.Contracts, &c.Related} {
			if part.Files == nil {
				part.Files = []string{}
			}
		}
	}
	for i := range d.Sections {
		s := &d.Sections[i]
		if s.Risks == nil {
			s.Risks = []Risk{}
		}
		for j := range s.Body {
			for k := range s.Body[j].Items {
				if s.Body[j].Items[k].Cites == nil {
					s.Body[j].Items[k].Cites = []Anchor{}
				}
			}
		}
		for j := range s.Risks {
			if s.Risks[j].Cites == nil {
				s.Risks[j].Cites = []Anchor{}
			}
		}
		for j := range s.LevelReasons {
			if s.LevelReasons[j].Cites == nil {
				s.LevelReasons[j].Cites = []Anchor{}
			}
		}
	}
	for i := range d.Tiles {
		if d.Tiles[i].Cites == nil {
			d.Tiles[i].Cites = []Anchor{}
		}
	}
}

// previous is what completion reads of the previous version: its number and
// each section's hunks and risks, by ID.
type previous struct {
	version  int
	sections map[string]Section
}

// previousOf reads the previous version from its stored document, either
// schema, or else from the bundle's previous version.
func previousOf(document []byte, fromBundle *bundle.PreviousVersion) (previous, error) {
	out := previous{sections: map[string]Section{}}
	if len(document) > 0 {
		d, err := Read(document)
		if err != nil {
			return out, fmt.Errorf("reviewv2: reading the previous version: %w", err)
		}
		var header struct {
			Version int `json:"version"`
		}
		_ = json.Unmarshal(d.Review, &header)
		out.version = header.Version
		for _, s := range d.Sections {
			out.sections[s.ID] = s
		}
		return out, nil
	}
	if fromBundle == nil {
		return out, nil
	}
	out.version = fromBundle.Version
	for _, s := range fromBundle.Sections {
		section := Section{ID: s.ID, Title: s.Title, Kind: s.Kind, Hunks: s.HunkContentHashes}
		var content struct {
			Risks []v1Risk `json:"risks"`
		}
		if len(s.Content) > 0 && json.Unmarshal(s.Content, &content) == nil {
			for _, r := range content.Risks {
				section.Risks = append(section.Risks, r.v2())
			}
		}
		out.sections[s.ID] = section
	}
	return out, nil
}

// handOvers are the previous active risks that follow their code to another
// section, by the section they leave (handedOff, risk IDs) and the one they
// join (handedIn, the previous risk by ID): a section of the new version
// names the risk's ID citing a hunk the previous risk cited, and every hunk
// it cited has left its old section (HandedOver); when its hunks split, only
// the section that has the first of them (heir) inherits it. Any other risk
// of the same ID in another section is a different risk. A previous section
// a section of a new ID continues (renamed, by the new ID) is that
// section's: its risks stay in it, and leave it as they leave a section of
// the same ID.
//
// Risk IDs are unique only within a section, so the section a risk joins may
// have had a mitigated risk of the same ID, which its agent is not shown.
// The joining risk then takes the next ID no previous risk had (<id>_N, in
// sections, which it changes) so that each keeps its own history.
func handOvers(prior previous, sections []Section, renamed map[string]string) (map[string]map[string]bool, map[string]map[string]Risk) {
	off, in := map[string]map[string]bool{}, map[string]map[string]Risk{}
	hunks, owner := map[string][]string{}, map[string]string{}
	priorIDs := map[string]bool{}
	for _, s := range prior.sections {
		for _, r := range s.Risks {
			priorIDs[r.ID] = true
		}
	}
	for _, s := range sections {
		hunks[s.ID] = s.Hunks
		for _, h := range s.Hunks {
			if _, taken := owner[h]; !taken {
				owner[h] = s.ID
			}
		}
	}
	successor := map[string]string{}
	for next, old := range renamed {
		successor[old] = next
	}
	ids := make([]string, 0, len(prior.sections))
	for id := range prior.sections {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, from := range ids {
		next := from
		if s, ok := successor[from]; ok {
			next = s
		}
		for _, old := range prior.sections[from].Risks {
			if old.State == "mitigated" || off[from][old.ID] {
				continue
			}
			cited := CitedHunks(old.Cites)
			to := heir(cited, owner)
			for k := range sections {
				s := &sections[k]
				if s.ID != to || s.ID == next || in[s.ID][old.ID].ID != "" {
					continue
				}
				i := slices.IndexFunc(s.Risks, func(r Risk) bool { return r.ID == old.ID })
				if i < 0 || !HandedOver(cited, hunks[next], func(h string) bool { return slices.Contains(CitedHunks(s.Risks[i].Cites), h) }) {
					continue
				}
				pred := s.ID
				if p, ok := renamed[s.ID]; ok {
					pred = p
				}
				id := old.ID
				if slices.ContainsFunc(prior.sections[pred].Risks, func(r Risk) bool { return r.ID == old.ID && r.State == "mitigated" }) {
					for n := 2; priorIDs[id] || slices.ContainsFunc(s.Risks, func(r Risk) bool { return r.ID == id }); n++ {
						id = fmt.Sprintf("%s_%d", old.ID, n)
					}
					s.Risks[i].ID = id
				}
				if off[from] == nil {
					off[from] = map[string]bool{}
				}
				if in[s.ID] == nil {
					in[s.ID] = map[string]Risk{}
				}
				off[from][old.ID], in[s.ID][id] = true, old
				break
			}
		}
	}
	return off, in
}

// reconcileRisks gives a section's risks their history: the previous
// section's risks first, each active again when the section names it,
// mitigated in this version when it no longer does (or kept as it was when
// retained: the section's details are not written, or it has no hunks), then
// the section's new risks, introduced in this version. A previous risk
// handed off to another section with its code (handOvers) is left to it,
// and dropped here even when the section still names it; a
// new risk handed in from another previous section keeps the version it was
// introduced in.
func reconcileRisks(prior, current []Risk, found bool, priorVersion, version int, retained bool, handedOff map[string]bool, handedIn map[string]Risk) []Risk {
	byID := map[string]Risk{}
	for _, r := range current {
		byID[r.ID] = r
	}
	out := []Risk{}
	if found {
		for _, old := range prior {
			introduced := priorVersion
			if old.IntroducedVersion != nil {
				introduced = *old.IntroducedVersion
			}
			if introduced < 1 {
				introduced = max(version-1, 1)
			}
			next, named := byID[old.ID]
			if handedOff[old.ID] {
				// Its code left: a copy the section still names is dropped.
				delete(byID, old.ID)
				continue
			}
			delete(byID, old.ID)
			if named && !retained {
				next.State, next.IntroducedVersion, next.MitigatedVersion = "active", &introduced, nil
				out = append(out, next)
				continue
			}
			if !named {
				next = old
			}
			next.IntroducedVersion = &introduced
			switch {
			case old.State == "mitigated":
				mitigated := version
				if old.MitigatedVersion != nil {
					mitigated = *old.MitigatedVersion
				}
				next.State, next.MitigatedVersion = "mitigated", &mitigated
			case retained:
				next.State, next.MitigatedVersion = "active", nil
			default:
				next.State, next.MitigatedVersion = "mitigated", &version
			}
			out = append(out, next)
		}
	}
	for _, r := range current {
		if _, fresh := byID[r.ID]; !fresh {
			continue
		}
		introduced := version
		if old, ok := handedIn[r.ID]; ok {
			introduced = priorVersion
			if old.IntroducedVersion != nil {
				introduced = *old.IntroducedVersion
			}
			if introduced < 1 {
				introduced = max(version-1, 1)
			}
		}
		r.State, r.IntroducedVersion, r.MitigatedVersion = "active", &introduced, nil
		out = append(out, r)
	}
	return out
}

// completeTiles fills code tiles' excerpts from the diff and gives every
// tile its state: native data's caption and source move to the tile.
func completeTiles(d *Document, b *bundle.Bundle) {
	hunks := map[string]bundle.Hunk{}
	for _, h := range b.Diff {
		hunks[h.ID] = h
	}
	for i := range d.Tiles {
		t := &d.Tiles[i]
		switch t.Type {
		case TypeFlow, TypeColumns, TypeSequence, TypeTable:
			liftNative(t)
		case TypeCode:
			if t.State == TileFailed || t.State == TilePending && len(t.Data) == 0 {
				break
			}
			fillCode(t, hunks)
		}
		if t.State == "" {
			t.State = TilePending
			if len(t.Data) > 0 {
				t.State = TileReady
			}
		}
	}
}

// LiftNative moves a native tile's data caption and source, as tiles/view
// writes them, to the tile's caption and citations, as Complete does: an
// added native tile is stored the way a generated one is.
func LiftNative(t *Tile) { liftNative(t) }

// liftNative moves a native tile's data caption and source, as tiles/view
// writes them, to the tile's caption and citations.
func liftNative(t *Tile) {
	var data map[string]json.RawMessage
	if json.Unmarshal(t.Data, &data) != nil || data == nil {
		return
	}
	caption, hasCaption := data["caption"]
	source, hasSource := data["source"]
	if !hasCaption && !hasSource {
		return
	}
	if t.Caption == "" {
		_ = json.Unmarshal(caption, &t.Caption)
	}
	if len(t.Cites) == 0 {
		var anchors []Anchor
		if json.Unmarshal(source, &anchors) == nil {
			t.Cites = anchors
		}
	}
	delete(data, "caption")
	delete(data, "source")
	if encoded, err := json.Marshal(data); err == nil {
		t.Data = encoded
	}
}

// fillCode fills a code tile's text with its hunk's new side: the lines
// between its first and last line when it names them, else the whole hunk,
// whose first and last new line become its lines.
func fillCode(t *Tile, hunks map[string]bundle.Hunk) {
	var code CodeData
	if json.Unmarshal(t.Data, &code) != nil || code.Hunk == "" {
		t.State, t.Problems = TileUnavailable, []string{"the tile names no hunk"}
		return
	}
	h, ok := hunks[code.Hunk]
	if !ok {
		t.State, t.Problems = TileUnavailable, []string{"the hunk it shows is not in this version's diff"}
		return
	}
	_, lines, ok := review.HunkLines(h)
	if !ok {
		t.State, t.Problems = TileUnavailable, []string{"the hunk's text is not available"}
		return
	}
	var text []string
	first, last := 0, 0
	for _, l := range lines {
		if l.New == 0 || len(code.Lines) == 2 && (l.New < code.Lines[0] || l.New > code.Lines[1]) {
			continue
		}
		if first == 0 {
			first = l.New
		}
		last = l.New
		text = append(text, l.Text)
	}
	if len(text) == 0 {
		t.State, t.Problems = TileUnavailable, []string{"the lines it names are not in the hunk"}
		return
	}
	if len(code.Lines) != 2 {
		code.Lines = []int{first, last}
	}
	code.Text = strings.Join(text, "\n")
	encoded, err := json.Marshal(code)
	if err != nil {
		return
	}
	t.Data = encoded
	if len(t.Cites) == 0 {
		t.Cites = []Anchor{HunkAnchor(code.Hunk, code.Lines)}
	}
	if t.State == "" || t.State == TilePending || t.State == TileUnavailable {
		t.State, t.Problems = TileReady, nil
	}
}

// ReviewMinutes is the document's reading time: the words of its prose (the
// summary's text, the verdict, why the risk is what it is, the intent, the
// context, each section's title, gist, body and risks, and each
// tile's title and caption), at baum.review/1's reading speed
// (review.MinutesForWords).
func ReviewMinutes(d Document) int {
	words := 0
	count := func(texts ...string) {
		for _, t := range texts {
			words += len(strings.Fields(t))
		}
	}
	count(d.Summary.Text, d.Summary.RiskWhy)
	if v := d.Summary.Verdict; v != nil {
		count(v.Text)
		for _, p := range v.Points {
			count(p.Text)
		}
	}
	for _, in := range d.Overview.Intent {
		count(in.Asked, in.Got)
	}
	if c := d.Context; c != nil {
		count(c.Area.Text, c.ReachedBy.Text, c.Contracts.Text, c.Related.Text)
	}
	for _, s := range d.Sections {
		count(s.Title, s.Gist)
		count(bodyWords(s.Body)...)
		for _, r := range s.Risks {
			if r.State != "mitigated" {
				count(r.Text)
			}
		}
	}
	for _, t := range d.Tiles {
		if t.State == TileReady {
			count(t.Title, t.Caption)
		}
	}
	return review.MinutesForWords(words)
}

// Read decodes a stored document of either schema as baum.review/2: a
// baum.review/1 document is converted without its composition (FromV1).
func Read(document []byte) (Document, error) {
	var head struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(document, &head); err != nil {
		return Document{}, err
	}
	switch head.Schema {
	case Name:
		var d Document
		if err := json.Unmarshal(document, &d); err != nil {
			return Document{}, err
		}
		EnsureBodies(&d)
		return d, nil
	case schema.Name:
		return FromV1(document)
	}
	return Document{}, fmt.Errorf("reviewv2: the document's schema is %q", head.Schema)
}

// IsV2 reports whether a stored document is a baum.review/2 one, by its
// schema field.
func IsV2(document []byte) bool {
	var head struct {
		Schema string `json:"schema"`
	}
	return json.Unmarshal(document, &head) == nil && head.Schema == Name
}
