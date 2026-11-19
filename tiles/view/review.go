package view

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Review is what the view reads of a baum.review/1 document: the parts the
// view call is told and the IDs a composition may name. It is read leniently
// from the normalised stored form (review.NormalizeStored): a member it does
// not know is ignored.
type Review struct {
	Title, Text    string
	Recommendation *Recommendation
	Risk           *RiskScore
	// Context holds the summary's context parts present, in order: area,
	// reached_by, contracts, related.
	Context     []ContextPart
	Sections    []Section
	Hunks       []Hunk
	Attachments []Attachment
	Sessions    []Session

	hunks map[string]bool
}

// Recommendation is the review's bottom line.
type Recommendation struct {
	Decision string `json:"decision"`
	Text     string `json:"text"`
}

// RiskScore is the review's risk score.
type RiskScore struct {
	Score   int          `json:"score"`
	Status  string       `json:"status,omitempty"`
	Reasons []RiskReason `json:"reasons,omitempty"`
}

// RiskReason is one reason for the risk score.
type RiskReason struct {
	Section string `json:"section,omitempty"`
	Risk    string `json:"risk,omitempty"`
	Text    string `json:"text"`
}

// ContextPart is one part of the summary's context.
type ContextPart struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// Section is a review section.
type Section struct {
	ID, Title, Summary string
	Hunks              []string
	// Stale says the section's code is no longer in the pull request.
	Stale bool
	Steps []Step
	Risks []Risk
}

// Step is a behaviour step of a section.
type Step struct {
	ID    string   `json:"id,omitempty"`
	Title string   `json:"title,omitempty"`
	Text  string   `json:"text"`
	Hunks []string `json:"-"`
}

// Risk is a section's risk.
type Risk struct {
	ID    string   `json:"id"`
	Tier  string   `json:"tier"`
	Text  string   `json:"text"`
	State string   `json:"state,omitempty"`
	Hunks []string `json:"-"`
}

// Hunk is a hunk of the pull request's diff.
type Hunk struct {
	ID   string
	Path string
}

// Attachment is an image of the pull request's description or of a session.
type Attachment struct {
	ID, Alt, Heading string
	// Origin is "description" or "session".
	Origin string
}

// Session is a linked agent session. A restricted one carries no prompts and
// is never named.
type Session struct {
	ID, Title  string
	Restricted bool
	Prompts    []Prompt
}

// Prompt is a person's prompt in a session, by its 1-based index.
type Prompt struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
}

// HasHunk reports whether the review's index has the hunk.
func (r *Review) HasHunk(id string) bool { return r.hunks[id] }

// Section returns the section with the ID, nil for none.
func (r *Review) Section(id string) *Section {
	for i := range r.Sections {
		if r.Sections[i].ID == id {
			return &r.Sections[i]
		}
	}
	return nil
}

// session returns the session with the ID, nil for none.
func (r *Review) session(id string) *Session {
	for i := range r.Sessions {
		if r.Sessions[i].ID == id {
			return &r.Sessions[i]
		}
	}
	return nil
}

type rawAnchor struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func hunksOf(anchors []rawAnchor) []string {
	var out []string
	for _, a := range anchors {
		if a.Type == "hunk" && a.ID != "" {
			out = append(out, a.ID)
		}
	}
	return out
}

type rawContextPart struct {
	Text string `json:"text"`
}

type rawReview struct {
	Schema  string `json:"schema"`
	Summary struct {
		Title          string          `json:"title"`
		Text           string          `json:"text"`
		Recommendation *Recommendation `json:"recommendation"`
		RiskScore      *RiskScore      `json:"risk_score"`
		Context        *struct {
			Area      *rawContextPart `json:"area"`
			ReachedBy *rawContextPart `json:"reached_by"`
			Contracts *rawContextPart `json:"contracts"`
			Related   *rawContextPart `json:"related"`
		} `json:"context"`
	} `json:"summary"`
	Sections []struct {
		ID      string   `json:"id"`
		Title   string   `json:"title"`
		Hunks   []string `json:"hunks"`
		Summary *struct {
			Text string `json:"text"`
		} `json:"summary"`
		Derived struct {
			State string `json:"state"`
		} `json:"derived"`
		Risks []struct {
			Risk
			Anchors []rawAnchor `json:"anchors"`
		} `json:"risks"`
		Blocks []json.RawMessage `json:"blocks"`
	} `json:"sections"`
	Index struct {
		Hunks []struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"hunks"`
		Attachments []struct {
			ID      string            `json:"id"`
			Alt     string            `json:"alt"`
			Heading string            `json:"heading"`
			Origins []json.RawMessage `json:"origins"`
		} `json:"attachments"`
		Sessions []struct {
			ID         string   `json:"id"`
			Title      string   `json:"title"`
			Restricted bool     `json:"restricted"`
			Prompts    []Prompt `json:"prompts"`
		} `json:"sessions"`
	} `json:"index"`
}

type rawBehavior struct {
	Kind  string `json:"kind"`
	Steps []struct {
		Step
		Anchors []rawAnchor `json:"anchors"`
	} `json:"steps"`
}

// rawReviewV2 is what Review reads of a baum.review/2 document.
type rawReviewV2 struct {
	Summary struct {
		Title     string          `json:"title"`
		Text      string          `json:"text"`
		Verdict   *Recommendation `json:"verdict"`
		RiskScore *RiskScore      `json:"risk_score"`
	} `json:"summary"`
	Context *struct {
		Area      *rawContextPart `json:"area"`
		ReachedBy *rawContextPart `json:"reached_by"`
		Contracts *rawContextPart `json:"contracts"`
		Related   *rawContextPart `json:"related"`
	} `json:"context"`
	Sections []struct {
		ID      string   `json:"id"`
		Title   string   `json:"title"`
		Hunks   []string `json:"hunks"`
		Summary string   `json:"summary"`
		Derived struct {
			State string `json:"state"`
		} `json:"derived"`
		// Body is what a section carries now; Summary and Steps are what a
		// document written before bodies carries.
		Body []struct {
			Type  string `json:"type"`
			Text  string `json:"text"`
			Items []struct {
				Text  string      `json:"text"`
				More  string      `json:"more"`
				Cites []rawAnchor `json:"cites"`
			} `json:"items"`
		} `json:"body"`
		Steps []struct {
			Step
			Cites []rawAnchor `json:"cites"`
		} `json:"steps"`
		Risks []struct {
			Risk
			Cites []rawAnchor `json:"cites"`
		} `json:"risks"`
	} `json:"sections"`
	Index json.RawMessage `json:"index"`
}

// parseReviewV2 reads a baum.review/2 document as Review: its verdict is the
// recommendation, its context and sections are read as baum.review/1's, and
// its index is the same object.
func parseReviewV2(document []byte) (*Review, error) {
	var raw rawReviewV2
	if err := json.Unmarshal(document, &raw); err != nil {
		return nil, fmt.Errorf("view: decoding the review: %w", err)
	}
	encoded, err := json.Marshal(map[string]any{"schema": "baum.review/1", "index": raw.Index})
	if err != nil {
		return nil, err
	}
	r, err := ParseReview(encoded)
	if err != nil {
		return nil, err
	}
	r.Title, r.Text, r.Recommendation, r.Risk = raw.Summary.Title, raw.Summary.Text, raw.Summary.Verdict, raw.Summary.RiskScore
	if c := raw.Context; c != nil {
		for _, part := range []struct {
			name string
			part *rawContextPart
		}{{"area", c.Area}, {"reached_by", c.ReachedBy}, {"contracts", c.Contracts}, {"related", c.Related}} {
			if part.part != nil && part.part.Text != "" {
				r.Context = append(r.Context, ContextPart{Name: part.name, Text: part.part.Text})
			}
		}
	}
	for _, s := range raw.Sections {
		section := Section{ID: s.ID, Title: s.Title, Summary: s.Summary, Hunks: s.Hunks, Stale: s.Derived.State == "stale"}
		for _, step := range s.Steps {
			step.Hunks = hunksOf(step.Cites)
			section.Steps = append(section.Steps, step.Step)
		}
		// A body is read as a summary (its first prose) and steps (its points).
		for _, block := range s.Body {
			switch block.Type {
			case "prose":
				if section.Summary == "" {
					section.Summary = block.Text
				}
			case "points":
				for _, p := range block.Items {
					step := Step{Title: p.Text, Text: p.More, Hunks: hunksOf(p.Cites)}
					if lead, rest, ok := strings.Cut(strings.TrimPrefix(p.Text, "**"), "**"); ok && strings.HasPrefix(p.Text, "**") {
						step.Title, step.Text = strings.TrimRight(lead, "."), strings.TrimSpace(rest)
					}
					step.Title = strings.NewReplacer("**", "").Replace(step.Title)
					section.Steps = append(section.Steps, step)
				}
			}
		}
		for _, risk := range s.Risks {
			risk.Hunks = hunksOf(risk.Cites)
			section.Risks = append(section.Risks, risk.Risk)
		}
		r.Sections = append(r.Sections, section)
	}
	return r, nil
}

// ParseReview reads a baum.review/1 document, normalised as stored, or a
// baum.review/2 document.
func ParseReview(document []byte) (*Review, error) {
	var raw rawReview
	var head struct {
		Schema string `json:"schema"`
	}
	if json.Unmarshal(document, &head) == nil && head.Schema == "baum.review/2" {
		return parseReviewV2(document)
	}
	if err := json.Unmarshal(document, &raw); err != nil {
		return nil, fmt.Errorf("view: decoding the review: %w", err)
	}
	if raw.Schema != "baum.review/1" {
		return nil, errors.New("view: the document is not a baum.review/1 review")
	}
	r := &Review{Title: raw.Summary.Title, Text: raw.Summary.Text, Recommendation: raw.Summary.Recommendation, Risk: raw.Summary.RiskScore, hunks: map[string]bool{}}
	if c := raw.Summary.Context; c != nil {
		for _, part := range []struct {
			name string
			part *rawContextPart
		}{{"area", c.Area}, {"reached_by", c.ReachedBy}, {"contracts", c.Contracts}, {"related", c.Related}} {
			if part.part != nil && part.part.Text != "" {
				r.Context = append(r.Context, ContextPart{Name: part.name, Text: part.part.Text})
			}
		}
	}
	for _, h := range raw.Index.Hunks {
		r.Hunks = append(r.Hunks, Hunk{ID: h.ID, Path: h.Path})
		r.hunks[h.ID] = true
	}
	for _, s := range raw.Sections {
		section := Section{ID: s.ID, Title: s.Title, Hunks: s.Hunks, Stale: s.Derived.State == "stale"}
		if s.Summary != nil {
			section.Summary = s.Summary.Text
		}
		for _, block := range s.Blocks {
			var b rawBehavior
			if json.Unmarshal(block, &b) != nil || b.Kind != "behavior" {
				continue
			}
			for _, step := range b.Steps {
				step.Hunks = hunksOf(step.Anchors)
				section.Steps = append(section.Steps, step.Step)
			}
		}
		for _, risk := range s.Risks {
			risk.Hunks = hunksOf(risk.Anchors)
			section.Risks = append(section.Risks, risk.Risk)
		}
		r.Sections = append(r.Sections, section)
	}
	for _, a := range raw.Index.Attachments {
		origin := "description"
		if len(a.Origins) > 0 {
			origin = "session"
		}
		r.Attachments = append(r.Attachments, Attachment{ID: a.ID, Alt: a.Alt, Heading: a.Heading, Origin: origin})
	}
	for _, s := range raw.Index.Sessions {
		r.Sessions = append(r.Sessions, Session{ID: s.ID, Title: s.Title, Restricted: s.Restricted, Prompts: s.Prompts})
	}
	return r, nil
}
