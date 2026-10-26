package reviewgroups

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MemberReview is one member's saved review, as a group run reads it.
type MemberReview struct {
	Number int
	// Head is the member's head the group run reviews.
	Head string
	// Version and Document are the member's latest saved review.
	Version  int
	Document []byte
}

// memberContext is one entry of members.json.
type memberContext struct {
	Number        int    `json:"number"`
	Title         string `json:"title"`
	Head          string `json:"head"`
	ReviewVersion int    `json:"review_version"`
	// ReviewedHead is the head the saved review read; it differs from Head
	// when the member moved since.
	ReviewedHead string          `json:"reviewed_head"`
	Summary      string          `json:"summary"`
	ChangeKind   string          `json:"change_kind,omitempty"`
	RiskScore    *int            `json:"risk_score,omitempty"`
	Sections     []memberSection `json:"sections"`
}

type memberSection struct {
	Title   string       `json:"title"`
	Kind    string       `json:"kind"`
	Summary string       `json:"summary"`
	Risks   []memberRisk `json:"risks,omitempty"`
}

type memberRisk struct {
	Tier     string `json:"tier"`
	Category string `json:"category"`
	Text     string `json:"text"`
}

// maxMemberContextBytes bounds one member's entry in members.json; sections
// past it are left out.
const maxMemberContextBytes = 24 << 10

// MembersFile is members.json, the members' saved reviews in brief for the
// group run: each member's title, the head the run reviews, and its review's
// summary, current sections and active risks. Stale sections and mitigated
// risks are left out, and so is what does not fit a member's share.
func MembersFile(members []MemberReview) ([]byte, error) {
	out := make([]memberContext, 0, len(members))
	for _, member := range members {
		var doc struct {
			Review struct {
				Head string `json:"head"`
			} `json:"review"`
			Summary struct {
				Title      string `json:"title"`
				Text       string `json:"text"`
				ChangeKind string `json:"change_kind"`
				RiskScore  *struct {
					Score *int `json:"score"`
				} `json:"risk_score"`
			} `json:"summary"`
			Sections []struct {
				Title   string `json:"title"`
				Kind    string `json:"kind"`
				Summary struct {
					Text string `json:"text"`
				} `json:"summary"`
				Risks []struct {
					Tier     string `json:"tier"`
					Category string `json:"category"`
					Text     string `json:"text"`
					State    string `json:"state"`
				} `json:"risks"`
				Derived struct {
					State string `json:"state"`
				} `json:"derived"`
			} `json:"sections"`
		}
		if err := json.Unmarshal(member.Document, &doc); err != nil {
			return nil, fmt.Errorf("the saved review of #%d: %w", member.Number, err)
		}
		entry := memberContext{
			Number: member.Number, Title: doc.Summary.Title, Head: member.Head, ReviewVersion: member.Version,
			ReviewedHead: doc.Review.Head, Summary: doc.Summary.Text, ChangeKind: doc.Summary.ChangeKind, Sections: []memberSection{},
		}
		if doc.Summary.RiskScore != nil {
			entry.RiskScore = doc.Summary.RiskScore.Score
		}
		size := len(entry.Summary) + len(entry.Title)
		for _, section := range doc.Sections {
			if section.Derived.State == "stale" {
				continue
			}
			item := memberSection{Title: section.Title, Kind: section.Kind, Summary: section.Summary.Text}
			for _, risk := range section.Risks {
				if risk.State == "" || strings.EqualFold(risk.State, "active") {
					item.Risks = append(item.Risks, memberRisk{Tier: risk.Tier, Category: risk.Category, Text: risk.Text})
				}
			}
			encoded, _ := json.Marshal(item)
			if size += len(encoded); size > maxMemberContextBytes {
				break
			}
			entry.Sections = append(entry.Sections, item)
		}
		out = append(out, entry)
	}
	encoded, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}
