package reviewv2

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/baum-ai/baum/schema"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/view"
)

// v1Document is what FromV1 reads of a stored baum.review/1 document.
type v1Document struct {
	Schema  string          `json:"schema"`
	Review  json.RawMessage `json:"review"`
	Summary struct {
		Title          string `json:"title"`
		Text           string `json:"text"`
		ChangeKind     string `json:"change_kind"`
		Recommendation *struct {
			Decision string `json:"decision"`
			Text     string `json:"text"`
		} `json:"recommendation"`
		Context *struct {
			Area      *v1ContextPart `json:"area"`
			ReachedBy *v1ContextPart `json:"reached_by"`
			Contracts *v1ContextPart `json:"contracts"`
			Related   *v1ContextPart `json:"related"`
		} `json:"context"`
		RiskScore     json.RawMessage `json:"risk_score"`
		ReviewMinutes *int            `json:"review_minutes"`
	} `json:"summary"`
	Sections []v1Section     `json:"sections"`
	Stats    json.RawMessage `json:"stats"`
	Index    json.RawMessage `json:"index"`
}

type v1ContextPart struct {
	Text string `json:"text"`
	Refs []struct {
		Path string `json:"path"`
	} `json:"refs"`
}

func (p *v1ContextPart) v2() ContextPart {
	out := ContextPart{Files: []string{}}
	if p == nil {
		return out
	}
	out.Text = p.Text
	for _, ref := range p.Refs {
		if ref.Path != "" {
			out.Files = append(out.Files, ref.Path)
		}
	}
	return out
}

type v1Section struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Kind    string   `json:"kind"`
	Hunks   []string `json:"hunks"`
	Summary *struct {
		Text string `json:"text"`
	} `json:"summary"`
	Assessment *struct {
		Level   int `json:"level"`
		Reasons []struct {
			Text    string   `json:"text"`
			Anchors []Anchor `json:"anchors"`
		} `json:"reasons"`
	} `json:"assessment"`
	Blocks  []json.RawMessage `json:"blocks"`
	Risks   []v1Risk          `json:"risks"`
	Details string            `json:"details"`
	Derived json.RawMessage   `json:"derived"`
}

type v1Risk struct {
	ID                string   `json:"id"`
	Tier              string   `json:"tier"`
	Category          string   `json:"category"`
	Text              string   `json:"text"`
	Anchors           []Anchor `json:"anchors"`
	Cites             []Anchor `json:"cites"`
	State             string   `json:"state"`
	IntroducedVersion *int     `json:"introduced_version"`
	MitigatedVersion  *int     `json:"mitigated_version"`
}

// v2 is the risk as baum.review/2 has it. A risk read from a baum.review/2
// section's content keeps its citations.
func (r v1Risk) v2() Risk {
	cites := r.Anchors
	if cites == nil {
		cites = r.Cites
	}
	if cites == nil {
		cites = []Anchor{}
	}
	return Risk{ID: r.ID, Tier: r.Tier, Category: r.Category, Text: r.Text, Cites: cites, State: r.State, IntroducedVersion: r.IntroducedVersion, MitigatedVersion: r.MitigatedVersion}
}

type v1Behavior struct {
	Kind  string `json:"kind"`
	Steps []struct {
		ID      string   `json:"id"`
		Title   string   `json:"title"`
		Text    string   `json:"text"`
		Anchors []Anchor `json:"anchors"`
	} `json:"steps"`
}

// FromV1 converts a stored baum.review/1 document, with the review view
// composition (baum.composition/2) stored beside it or nil, to a
// baum.review/2 document:
//
//   - the header, stats, index, risk score and reading time are kept;
//   - the summary keeps its title, text and change kind; the recommendation
//     becomes the verdict, without points; the composition's risk summary
//     is risk_why;
//   - the summary's context parts become the context, their references the
//     files they name;
//   - each current section keeps its ID, title, kind, hunks, summary text,
//     risks (anchors as cites), details state and derived facts; its level
//     and level reasons are its assessment's; its behaviour steps become its
//     steps (beh_ IDs as st_); its gist is the summary's first sentence, made
//     plain and cut to MaxGist. A stale section, whose code is gone, is left
//     out;
//   - the composition's overview gives the screenshots, main diagram and
//     intent, and its native diagrams, library tiles and authored tiles
//     become tiles, owned by the section or context the composition places
//     them on (the overview for the main diagram), the rest placed as the
//     review page placed them (view.Build). Library and authored tiles are
//     not checked here: a host serving them checks them (TilesOfView of the
//     served view). Authored tiles keep their data, as type v1_table,
//     v1_code or v1_prose.
func FromV1(document, composition []byte) (Document, error) {
	var v1 v1Document
	if err := json.Unmarshal(document, &v1); err != nil {
		return Document{}, fmt.Errorf("reviewv2: decoding the baum.review/1 document: %w", err)
	}
	if v1.Schema != schema.Name {
		return Document{}, fmt.Errorf("reviewv2: the document's schema is %q, not %q", v1.Schema, schema.Name)
	}
	d := Document{Schema: Name, Review: v1.Review, Stats: v1.Stats, Index: v1.Index, Sections: []Section{}, Tiles: []Tile{}}
	d.Summary = Summary{Title: v1.Summary.Title, Text: v1.Summary.Text, ChangeKind: v1.Summary.ChangeKind, RiskScore: v1.Summary.RiskScore, ReviewMinutes: v1.Summary.ReviewMinutes}
	if len(bytes.TrimSpace(d.Summary.RiskScore)) == 0 {
		d.Summary.RiskScore = json.RawMessage("null")
	}
	if r := v1.Summary.Recommendation; r != nil {
		d.Summary.Verdict = &Verdict{Decision: r.Decision, Text: r.Text, Points: []VerdictPoint{}}
	}
	if c := v1.Summary.Context; c != nil {
		d.Context = &Context{Area: c.Area.v2(), ReachedBy: c.ReachedBy.v2(), Contracts: c.Contracts.v2(), Related: c.Related.v2()}
	}
	for _, s := range v1.Sections {
		var derived struct {
			State string `json:"state"`
		}
		_ = json.Unmarshal(s.Derived, &derived)
		if derived.State == "stale" {
			continue
		}
		d.Sections = append(d.Sections, sectionFromV1(s))
	}
	d.Overview = Overview{Screenshots: []string{}, Intent: []Intent{}}
	r, err := view.ParseReview(document)
	if err != nil {
		return Document{}, fmt.Errorf("reviewv2: reading the review for its tiles: %w", err)
	}
	var c *view.Composition
	var extras []tiles.Tile
	if len(composition) > 0 && view.IsComposition(composition) {
		if parsed, err := view.Parse(composition); err == nil {
			c = parsed
			extras = uncheckedExtras(parsed)
		}
	}
	v := view.Build(r, c, extras, nil)
	d.Tiles = TilesOfView(v)
	d.Overview = OverviewOfView(v)
	d.Summary.RiskWhy = v.Overview.RiskSummary
	return d, nil
}

func sectionFromV1(s v1Section) Section {
	out := Section{ID: s.ID, Title: s.Title, Kind: s.Kind, Hunks: s.Hunks, Details: s.Details, Derived: s.Derived, Steps: []Step{}, Risks: []Risk{}, LevelReasons: []LevelReason{}}
	if out.Hunks == nil {
		out.Hunks = []string{}
	}
	if s.Summary != nil {
		out.Summary = s.Summary.Text
	}
	out.Gist = Gist(out.Summary)
	if a := s.Assessment; a != nil {
		level := a.Level
		out.Level = &level
		for _, reason := range a.Reasons {
			cites := reason.Anchors
			if cites == nil {
				cites = []Anchor{}
			}
			out.LevelReasons = append(out.LevelReasons, LevelReason{Text: reason.Text, Cites: cites})
		}
	}
	for _, raw := range s.Blocks {
		var b v1Behavior
		if json.Unmarshal(raw, &b) != nil || b.Kind != "behavior" {
			continue
		}
		for _, step := range b.Steps {
			cites := step.Anchors
			if cites == nil {
				cites = []Anchor{}
			}
			id := step.ID
			if rest, ok := strings.CutPrefix(id, "beh_"); ok {
				id = "st_" + rest
			}
			out.Steps = append(out.Steps, Step{ID: id, Title: step.Title, Text: step.Text, Cites: cites})
		}
	}
	for _, risk := range s.Risks {
		out.Risks = append(out.Risks, risk.v2())
	}
	return out
}

// Gist is the first sentence of a text, made plain (view.Clean) and cut to
// MaxGist characters at a word, with an ellipsis.
func Gist(text string) string {
	plain := view.Clean(text)
	if loc := sentenceEnd.FindStringIndex(abbrev.ReplaceAllStringFunc(plain, func(s string) string { return strings.ReplaceAll(s, ".", "\x00") })); loc != nil {
		plain = strings.TrimSpace(plain[:loc[1]])
	}
	if utf8.RuneCountInString(plain) <= MaxGist {
		return plain
	}
	runes := []rune(plain)[:MaxGist-1]
	out := string(runes)
	if i := strings.LastIndex(out, " "); i > MaxGist/2 {
		out = out[:i]
	}
	return strings.TrimRight(out, " ,;:.-") + "…"
}

// uncheckedExtras are a composition's library and authored tiles as tiles,
// unchecked: a library tile is its type's (pending without data), an
// authored one its kind's.
func uncheckedExtras(c *view.Composition) []tiles.Tile {
	var out []tiles.Tile
	for _, lib := range c.Library {
		var def struct {
			Name  string `json:"name"`
			Title string `json:"title"`
		}
		_ = json.Unmarshal(lib.Definition, &def)
		if def.Name == "" {
			continue
		}
		state := tiles.StateReady
		if lib.Pending {
			state = tiles.StatePending
		}
		title := def.Title
		if title == "" {
			title = def.Name
		}
		out = append(out, tiles.Tile{ID: tiles.LibraryTileID(def.Name), Type: tiles.TypeCustom, Title: title, Data: lib.Data, State: state, Definition: lib.Definition})
	}
	for _, a := range c.Authored {
		kind, _, _ := strings.Cut(a.ID, "_")
		out = append(out, tiles.Tile{ID: a.ID, Type: kind, Title: a.Title, Data: a.Data, State: tiles.StateReady})
	}
	return out
}

// TilesOfView are a served review page view's tiles (baum.view/1) as
// baum.review/2 tiles, each owned by the section or context the view places
// it on, or by the overview when it is the main diagram (context when the
// view places it nowhere). A native diagram's caption and source move to
// the tile's caption and citations; a custom tile's type is its
// definition's name; an authored tile's type is v1_ and its kind. A tile's
// covered hunks become its citations when it has none, and an added tile
// keeps added, from and filling.
func TilesOfView(v view.View) []Tile {
	owner := map[string]string{}
	for _, p := range v.Sections {
		for _, id := range p.Tiles {
			if _, ok := owner[id]; !ok {
				owner[id] = p.ID
			}
		}
	}
	if d := v.Overview.Diagram; d != nil {
		owner[*d] = OwnerOverview
	}
	out := make([]Tile, 0, len(v.Tiles))
	for _, vt := range v.Tiles {
		t := Tile{ID: vt.ID, Type: vt.Type, Owner: owner[vt.ID], Title: vt.Title, Data: vt.Data, State: vt.State, Definition: vt.Definition, Problems: vt.Problems, Added: vt.Added, From: vt.From, Filling: vt.Filling, Cites: []Anchor{}}
		if t.Owner == "" {
			t.Owner = OwnerContext
		}
		switch {
		case strings.HasPrefix(vt.ID, "dg_"):
			liftNative(&t)
		case vt.Type == tiles.TypeCustom:
			var def struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(vt.Definition, &def)
			if typeName.MatchString(def.Name) {
				t.Type = def.Name
			}
		default:
			t.Type = "v1_" + vt.Type
		}
		if len(t.Cites) == 0 {
			for _, h := range vt.Covers {
				t.Cites = append(t.Cites, HunkAnchor(h, nil))
			}
		}
		if t.State == "" {
			t.State = TileReady
		}
		out = append(out, t)
	}
	return out
}

// OverviewOfView is a served view's overview parts as baum.review/2's:
// the screenshots, the main diagram and the intent.
func OverviewOfView(v view.View) Overview {
	o := Overview{Screenshots: []string{}, Diagram: v.Overview.Diagram, Intent: []Intent{}}
	o.Screenshots = append(o.Screenshots, v.Overview.Screenshots...)
	for _, in := range v.Overview.Intent {
		o.Intent = append(o.Intent, Intent{Session: in.Session, Prompt: in.Prompt, Asked: in.Asked, Got: in.Got})
	}
	return o
}
