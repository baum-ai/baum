package replay

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/review"
)

var _ interface {
	Chainer
	Meter
	Deriver
	Definer
} = (*ReviewGenerator)(nil)

func init() { RegisterMeter("review", &ReviewGenerator{}) }

// Next implements Chainer: the previous version is carried into the bundle as
// the app does (review.PreviousVersionOf, with the stored document's own
// version number), and its hunk anchors are moved to where the hunks sit in
// this revision's diff, as assembly does.
func (g *ReviewGenerator) Next(c Case, document []byte) (bundle.Bundle, error) {
	var stored struct {
		Review struct {
			Version int `json:"version"`
		} `json:"review"`
	}
	if err := json.Unmarshal(document, &stored); err != nil || stored.Review.Version < 1 {
		return bundle.Bundle{}, fmt.Errorf("case %s: the previous document has no review version", c.ID)
	}
	previous, err := review.PreviousVersionOf(document, stored.Review.Version)
	if err != nil {
		return bundle.Bundle{}, fmt.Errorf("case %s: carrying version %d: %w", c.ID, stored.Review.Version, err)
	}
	b, err := c.FreshBundle()
	if err != nil {
		return bundle.Bundle{}, err
	}
	b.Previous = previous
	if b.Sources == nil {
		b.Sources = map[string]bool{}
	}
	b.Sources["previous_version"] = true
	if err := review.FitPreviousHunkAnchors(&b); err != nil {
		return bundle.Bundle{}, fmt.Errorf("case %s: fitting the previous version to the diff: %w", c.ID, err)
	}
	return b, nil
}

// storedReview is the part of a stored review document the metrics read.
type storedReview struct {
	Review struct {
		Version int `json:"version"`
	} `json:"review"`
	Sections []struct {
		ID       string   `json:"id"`
		Title    string   `json:"title"`
		Hunks    []string `json:"hunks"`
		Evidence []struct {
			Status string `json:"status"`
		} `json:"evidence"`
		Risks []reviewRisk `json:"risks"`
	} `json:"sections"`
	Index struct {
		Hunks []struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"hunks"`
	} `json:"index"`
	UnassignedHunks []string `json:"unassigned_hunks"`
}

type reviewRisk struct {
	ID                string `json:"id"`
	Tier              string `json:"tier"`
	State             string `json:"state"`
	IntroducedVersion *int   `json:"introduced_version"`
	MitigatedVersion  *int   `json:"mitigated_version"`
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

// evidenceStatuses are the statuses evidence has; each is counted, as zero
// when there is none.
var evidenceStatuses = []string{"verified", "observed", "asserted", "reported", "pending", "missing", "failed", "skipped", "unavailable"}

// Definitions implements Definer.
func (g *ReviewGenerator) Definitions() map[string]string {
	return map[string]string{
		"sections":                    "sections in the document",
		"sections_without_hunks":      "sections with no hunk (stale or restored)",
		"risks":                       "risks, active and mitigated",
		"risks_active":                "risks in the active state, by tier in risks_active_<tier>",
		"risks_mitigated":             "risks in the mitigated state",
		"evidence":                    "evidence items, by status in evidence_<status>",
		"hunks":                       "hunks in the document's index (the index may be fitted or collapsed, which varies this denominator between cases)",
		"hunks_covered":               "distinct hunks that some section lists",
		"hunks_unassigned":            "hunks the document lists as assigned to no section",
		"hunks_covered_ratio":         "hunks_covered over hunks; left out when the index has no hunk",
		"risk_lifecycle_violations":   "risks whose state contradicts itself or whose versions are impossible (see the README)",
		"ids_previous":                "section IDs of the earlier version",
		"ids_survived":                "of those, IDs the later version has",
		"ids_unmatched":               "of those, IDs the later version lacks",
		"id_survival":                 "ids_survived over ids_previous. The review validator forces earlier IDs to stay, so for this generator it is a sanity check; it discriminates for generators without that rule",
		"survived_matched_hunk":       "surviving IDs whose section still shares a hunk with the earlier one",
		"survived_matched_path_only":  "surviving IDs that share a file path but no hunk (the lines changed or the file was renamed)",
		"survived_no_overlap":         "surviving IDs whose section shares no hunk and no path with the earlier one",
		"survived_empty":              "surviving IDs whose earlier section had no hunk, so there is nothing to continue",
		"continuity":                  "(matched by hunk + by path only) over (survived - empty)",
		"reverse_matched":             "later sections matched to an earlier one (most shared hunks, then paths, then similar titles)",
		"reverse_same_id":             "of those, matched sections that carry the same ID",
		"reverse_id_agreement":        "reverse_same_id over reverse_matched: whether the ID follows the content",
		"sections_new":                "later sections that match no earlier one",
		"sections_dropped":            "earlier sections that match no later one",
		"splits":                      "earlier sections that two or more later sections best match",
		"merges":                      "later sections that two or more earlier sections best match",
		"risks_carried":               "risks in both versions in the same state",
		"risks_newly_mitigated":       "active in the earlier version, mitigated in the later",
		"risks_reopened":              "mitigated in the earlier version, active in the later",
		"risks_introduced":            "risks only the later version has",
		"risks_vanished":              "active risks the later version no longer lists",
		"risk_introduced_changed":     "carried risks whose introduced_version differs from the earlier version's",
		"id_survival_pooled":          "over all pairs: ids_survived / ids_previous",
		"continuity_pooled":           "over all pairs: matched (hunk or path) / (survived - empty)",
		"reverse_id_agreement_pooled": "over all pairs: reverse_same_id / reverse_matched",
		"hunks_covered_ratio_pooled":  "over all cases: hunks_covered / hunks",
	}
}

// riskViolations counts the risks of a document whose state contradicts itself
// (active with a mitigated version, mitigated without one, an unknown state, or
// the same risk in two states) or whose versions are impossible (above the
// document's own, or mitigated before it was introduced).
func riskViolations(d storedReview) int {
	violations := 0
	states := map[string]string{}
	for _, s := range d.Sections {
		for _, r := range s.Risks {
			switch r.State {
			case "active":
				if r.MitigatedVersion != nil {
					violations++
				}
			case "mitigated":
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
			if v := d.Review.Version; v > 0 && (r.IntroducedVersion != nil && *r.IntroducedVersion > v || r.MitigatedVersion != nil && *r.MitigatedVersion > v) {
				violations++
			}
			if r.IntroducedVersion != nil && r.MitigatedVersion != nil && *r.MitigatedVersion < *r.IntroducedVersion {
				violations++
			}
		}
	}
	return violations
}

// DocumentMetrics implements Meter. Every count is present, as zero when there
// is none; only a ratio with a zero denominator is left out.
func (g *ReviewGenerator) DocumentMetrics(document []byte) (map[string]float64, error) {
	d, err := decodeReview(document)
	if err != nil {
		return nil, err
	}
	m := map[string]float64{"sections": float64(len(d.Sections)), "sections_without_hunks": 0, "risks": 0, "risks_active": 0, "risks_mitigated": 0, "evidence": 0}
	for _, tier := range riskTiers {
		m["risks_active_"+tier] = 0
	}
	for _, status := range evidenceStatuses {
		m["evidence_"+status] = 0
	}
	covered := map[string]bool{}
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
			case "mitigated":
				m["risks_mitigated"]++
			}
		}
	}
	m["risk_lifecycle_violations"] = float64(riskViolations(d))
	m["hunks"] = float64(len(d.Index.Hunks))
	m["hunks_covered"] = float64(len(covered))
	m["hunks_unassigned"] = float64(len(d.UnassignedHunks))
	if len(d.Index.Hunks) > 0 {
		m["hunks_covered_ratio"] = float64(len(covered)) / float64(len(d.Index.Hunks))
	}
	return m, nil
}

// section is what the matching reads of a section.
type section struct {
	id, title string
	hunks     map[string]bool
	paths     map[string]bool
}

func sectionsOf(d storedReview) []section {
	pathOf := map[string]string{}
	for _, h := range d.Index.Hunks {
		pathOf[h.ID] = h.Path
	}
	var out []section
	for _, s := range d.Sections {
		sec := section{id: s.ID, title: s.Title, hunks: map[string]bool{}, paths: map[string]bool{}}
		for _, h := range s.Hunks {
			sec.hunks[h] = true
			if p := pathOf[h]; p != "" {
				sec.paths[p] = true
			}
		}
		out = append(out, sec)
	}
	return out
}

func overlap(a, b map[string]bool) int {
	n := 0
	for k := range a {
		if b[k] {
			n++
		}
	}
	return n
}

func words(title string) map[string]bool {
	set := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(title), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		set[w] = true
	}
	return set
}

// similarity is the Jaccard index of the words of two titles.
func similarity(a, b string) float64 {
	wa, wb := words(a), words(b)
	if len(wa) == 0 || len(wb) == 0 {
		return 0
	}
	shared := overlap(wa, wb)
	return float64(shared) / float64(len(wa)+len(wb)-shared)
}

// bestMatch finds the section of others that s most resembles: the most shared
// hunks, then the most shared paths, then the most similar title (at least half
// of its words in common). A tie goes to the section with the same ID, then
// the first. It returns -1 when nothing resembles s.
func bestMatch(s section, others []section) int {
	index := -1
	type score struct {
		hunks, paths int
		title        float64
		sameID       bool
	}
	var best score
	for i, o := range others {
		sc := score{overlap(s.hunks, o.hunks), overlap(s.paths, o.paths), similarity(s.title, o.title), o.id == s.id}
		if sc.hunks == 0 && sc.paths == 0 && sc.title < 0.5 {
			continue
		}
		better := index < 0 || sc.hunks > best.hunks || sc.hunks == best.hunks && (sc.paths > best.paths ||
			sc.paths == best.paths && (sc.title > best.title || sc.title == best.title && sc.sameID && !best.sameID))
		if better {
			index, best = i, sc
		}
	}
	return index
}

// PairMetrics implements Meter. See Definitions for each key.
func (g *ReviewGenerator) PairMetrics(previous, next []byte) (map[string]float64, error) {
	a, err := decodeReview(previous)
	if err != nil {
		return nil, err
	}
	b, err := decodeReview(next)
	if err != nil {
		return nil, err
	}
	before, after := sectionsOf(a), sectionsOf(b)
	m := map[string]float64{"sections_previous": float64(len(before)), "sections_next": float64(len(after)), "ids_previous": float64(len(before)),
		"ids_survived": 0, "ids_unmatched": 0, "survived_matched_hunk": 0, "survived_matched_path_only": 0, "survived_no_overlap": 0, "survived_empty": 0,
		"reverse_matched": 0, "reverse_same_id": 0, "sections_new": 0, "sections_dropped": 0, "splits": 0, "merges": 0}

	// (a) and (b): the earlier IDs that the later version has, and whether those
	// sections still hold the earlier section's content.
	byID := map[string]section{}
	for _, s := range after {
		byID[s.id] = s
	}
	for _, s := range before {
		later, ok := byID[s.id]
		if !ok {
			m["ids_unmatched"]++
			continue
		}
		m["ids_survived"]++
		switch {
		case len(s.hunks) == 0:
			m["survived_empty"]++
		case overlap(s.hunks, later.hunks) > 0:
			m["survived_matched_hunk"]++
		case overlap(s.paths, later.paths) > 0:
			m["survived_matched_path_only"]++
		default:
			m["survived_no_overlap"]++
		}
	}
	if len(before) > 0 {
		m["id_survival"] = m["ids_survived"] / m["ids_previous"]
	}
	if denominator := m["ids_survived"] - m["survived_empty"]; denominator > 0 {
		m["continuity"] = (m["survived_matched_hunk"] + m["survived_matched_path_only"]) / denominator
	}

	// (c): each later section to the earlier one it resembles, and back.
	matchedBy := map[int]int{} // earlier section -> later sections matching it
	for _, s := range after {
		i := bestMatch(s, before)
		if i < 0 {
			m["sections_new"]++
			continue
		}
		m["reverse_matched"]++
		matchedBy[i]++
		if before[i].id == s.id {
			m["reverse_same_id"]++
		}
	}
	for _, n := range matchedBy {
		if n >= 2 {
			m["splits"]++
		}
	}
	forward := map[int]int{} // later section -> earlier sections matching it
	for _, s := range before {
		i := bestMatch(s, after)
		if i < 0 {
			m["sections_dropped"]++
			continue
		}
		forward[i]++
	}
	for _, n := range forward {
		if n >= 2 {
			m["merges"]++
		}
	}
	if m["reverse_matched"] > 0 {
		m["reverse_id_agreement"] = m["reverse_same_id"] / m["reverse_matched"]
	}

	// (d): the risks between the versions.
	for _, k := range []string{"risks_carried", "risks_newly_mitigated", "risks_reopened", "risks_introduced", "risks_vanished", "risk_introduced_changed"} {
		m[k] = 0
	}
	earlier := map[string]reviewRisk{}
	for _, s := range a.Sections {
		for _, r := range s.Risks {
			earlier[r.ID] = r
		}
	}
	later := map[string]reviewRisk{}
	for _, s := range b.Sections {
		for _, r := range s.Risks {
			later[r.ID] = r
		}
	}
	for id, r := range later {
		before, known := earlier[id]
		switch {
		case !known:
			m["risks_introduced"]++
			continue
		case before.State == "active" && r.State == "mitigated":
			m["risks_newly_mitigated"]++
		case before.State == "mitigated" && r.State == "active":
			m["risks_reopened"]++
		default:
			m["risks_carried"]++
		}
		if before.IntroducedVersion != nil && r.IntroducedVersion != nil && *before.IntroducedVersion != *r.IntroducedVersion {
			m["risk_introduced_changed"]++
		}
	}
	for id, r := range earlier {
		if _, kept := later[id]; !kept && r.State == "active" {
			m["risks_vanished"]++
		}
	}
	m["risk_lifecycle_violations"] = float64(riskViolations(b)) + m["risk_introduced_changed"]
	return m, nil
}

// Derived implements Deriver: ratios over the sums of a group, not the mean of
// each case's or pair's.
func (g *ReviewGenerator) Derived(sums map[string]float64) map[string]float64 {
	derived := map[string]float64{}
	if v := sums["ids_previous"]; v > 0 {
		derived["id_survival_pooled"] = sums["ids_survived"] / v
	}
	if v := sums["survived_matched_hunk"] + sums["survived_matched_path_only"] + sums["survived_no_overlap"]; v > 0 {
		derived["continuity_pooled"] = (sums["survived_matched_hunk"] + sums["survived_matched_path_only"]) / v
	}
	if v := sums["reverse_matched"]; v > 0 {
		derived["reverse_id_agreement_pooled"] = sums["reverse_same_id"] / v
	}
	if v := sums["hunks"]; v > 0 {
		derived["hunks_covered_ratio_pooled"] = sums["hunks_covered"] / v
	}
	return derived
}
