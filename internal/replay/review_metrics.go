package replay

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/baum-ai/baum/review"
)

var _ interface {
	Chainer
	Meter
	Deriver
} = (*ReviewGenerator)(nil)

func init() { RegisterMeter("review", &ReviewGenerator{}) }

// Next implements Chainer: the previous version is carried into the next
// bundle as the app does (review.PreviousVersionOf), and its hunk anchors are
// moved to where the hunks sit in this revision's diff, as assembly does.
func (g *ReviewGenerator) Next(c Case, document []byte, version int) (Case, error) {
	previous, err := review.PreviousVersionOf(document, version)
	if err != nil {
		return Case{}, fmt.Errorf("case %s: carrying version %d: %w", c.ID, version, err)
	}
	b, err := c.FreshBundle()
	if err != nil {
		return Case{}, err
	}
	b.Previous = previous
	if b.Sources == nil {
		b.Sources = map[string]bool{}
	}
	b.Sources["previous_version"] = true
	if err := review.FitPreviousHunkAnchors(&b); err != nil {
		return Case{}, fmt.Errorf("case %s: fitting the previous version to the diff: %w", c.ID, err)
	}
	return c.WithBundle(b)
}

// storedReview is the part of a stored review document the metrics read.
type storedReview struct {
	Sections []struct {
		ID       string   `json:"id"`
		Hunks    []string `json:"hunks"`
		Evidence []struct {
			Status string `json:"status"`
		} `json:"evidence"`
		Risks []struct {
			ID                string `json:"id"`
			Tier              string `json:"tier"`
			State             string `json:"state"`
			IntroducedVersion *int   `json:"introduced_version"`
			MitigatedVersion  *int   `json:"mitigated_version"`
		} `json:"risks"`
	} `json:"sections"`
	Index struct {
		Hunks []struct {
			ID string `json:"id"`
		} `json:"hunks"`
	} `json:"index"`
	UnassignedHunks []string `json:"unassigned_hunks"`
}

func decodeReview(document []byte) (storedReview, error) {
	var d storedReview
	if err := json.Unmarshal(document, &d); err != nil {
		return d, fmt.Errorf("the document is not a review: %w", err)
	}
	return d, nil
}

// riskTiers are the tiers a risk has, most severe first.
var riskTiers = []string{"critical", "high", "medium", "low"}

// DocumentMetrics implements Meter: sections, active risks by tier,
// mitigated risks, evidence by status, and how many of the diff's hunks the
// sections cover. risk_lifecycle_violations counts risks whose state
// contradicts itself: active with a mitigated version, mitigated without one,
// or listed in two sections in different states.
func (g *ReviewGenerator) DocumentMetrics(document []byte) (map[string]float64, error) {
	d, err := decodeReview(document)
	if err != nil {
		return nil, err
	}
	m := map[string]float64{"sections": float64(len(d.Sections))}
	for _, tier := range riskTiers {
		m["risks_active_"+tier] = 0
	}
	covered := map[string]bool{}
	states := map[string]string{}
	violations := 0
	for _, s := range d.Sections {
		if len(s.Hunks) == 0 {
			m["sections_without_hunks"]++
		}
		for _, h := range s.Hunks {
			covered[h] = true
		}
		for _, e := range s.Evidence {
			m["evidence_"+e.Status]++
			m["evidence"]++
		}
		for _, r := range s.Risks {
			m["risks"]++
			switch r.State {
			case "active":
				m["risks_active"]++
				m["risks_active_"+r.Tier]++
				if r.MitigatedVersion != nil {
					violations++
				}
			case "mitigated":
				m["risks_mitigated"]++
				if r.MitigatedVersion == nil {
					violations++
				}
			default:
				violations++
			}
			if earlier, ok := states[r.ID]; ok && earlier != r.State {
				violations++
			}
			states[r.ID] = r.State
		}
	}
	m["risk_lifecycle_violations"] = float64(violations)
	m["hunks"] = float64(len(d.Index.Hunks))
	m["hunks_covered"] = float64(len(covered))
	m["hunks_unassigned"] = float64(len(d.UnassignedHunks))
	if len(d.Index.Hunks) > 0 {
		m["hunks_covered_ratio"] = float64(len(covered)) / float64(len(d.Index.Hunks))
	}
	return m, nil
}

// PairMetrics implements Meter. Each section of the earlier version that
// shares hunks with a section of the later one is matched to the later
// section it shares most hunks with (the same section ID wins a tie);
// section_ids_retained counts the matches that kept the ID, out of
// sections_matched. Earlier sections with no counterpart are sections_dropped
// and later ones that match no earlier section are sections_added. The risk
// counts follow risk IDs between the two versions.
func (g *ReviewGenerator) PairMetrics(previous, next []byte) (map[string]float64, error) {
	a, err := decodeReview(previous)
	if err != nil {
		return nil, err
	}
	b, err := decodeReview(next)
	if err != nil {
		return nil, err
	}
	m := map[string]float64{"sections_previous": float64(len(a.Sections)), "sections_next": float64(len(b.Sections))}
	claimed := map[string]bool{}
	for _, before := range a.Sections {
		if len(before.Hunks) == 0 {
			continue
		}
		best, bestShared := "", 0
		for _, after := range b.Sections {
			shared := 0
			for _, h := range after.Hunks {
				if slices.Contains(before.Hunks, h) {
					shared++
				}
			}
			if shared > bestShared || shared > 0 && shared == bestShared && after.ID == before.ID {
				best, bestShared = after.ID, shared
			}
		}
		if bestShared == 0 {
			m["sections_dropped"]++
			continue
		}
		claimed[best] = true
		m["sections_matched"]++
		if best == before.ID {
			m["section_ids_retained"]++
		}
	}
	for _, after := range b.Sections {
		if !claimed[after.ID] {
			m["sections_added"]++
		}
	}
	if m["sections_matched"] > 0 {
		m["section_id_retention"] = m["section_ids_retained"] / m["sections_matched"]
	}

	earlier := map[string]string{}
	for _, s := range a.Sections {
		for _, r := range s.Risks {
			earlier[r.ID] = r.State
		}
	}
	later := map[string]string{}
	for _, s := range b.Sections {
		for _, r := range s.Risks {
			later[r.ID] = r.State
		}
	}
	for id, state := range later {
		before, known := earlier[id]
		switch {
		case !known:
			m["risks_introduced"]++
		case before == "active" && state == "mitigated":
			m["risks_newly_mitigated"]++
		default:
			m["risks_carried"]++
		}
	}
	for id, state := range earlier {
		if _, kept := later[id]; !kept && state == "active" {
			m["risks_vanished"]++
		}
	}
	nextMetrics, err := g.DocumentMetrics(next)
	if err != nil {
		return nil, err
	}
	m["risk_lifecycle_violations"] = nextMetrics["risk_lifecycle_violations"]
	return m, nil
}

// Derived implements Deriver: retention over all matched sections of the
// pairs, not the mean of each pair's.
func (g *ReviewGenerator) Derived(sums map[string]float64) map[string]float64 {
	derived := map[string]float64{}
	if matched := sums["sections_matched"]; matched > 0 {
		derived["section_id_retention_pooled"] = sums["section_ids_retained"] / matched
	}
	if total := sums["hunks"]; total > 0 {
		derived["hunks_covered_ratio_pooled"] = sums["hunks_covered"] / total
	}
	return derived
}
