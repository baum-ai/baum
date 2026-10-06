package review

import (
	"strings"

	bundlepkg "github.com/lastpersonlabs/baum/bundle"
)

// RiskRubricVersion names the rules riskScore applies. A change to the scale,
// the floors or what counts as a limitation needs a new version, so a stored
// score always says which rules produced it.
const RiskRubricVersion = "risk-v1"

const maxRiskScoreReasons = 3

// riskFloors is the least score an active risk of each tier imposes: a
// concern the review found cannot be outweighed by a low assessment.
var riskFloors = map[string]int{"high": 4, "critical": 5}

// riskScore rates the pull request's inherent risk from its current sections.
// The score is the highest section level, never an average, so harmless
// sections cannot dilute a dangerous one; mechanical sections rate 1 and
// active high or critical risks set a floor. Obsolete sections and mitigated
// risks take no part. Because the score is a maximum, anything the review
// could not rate can only hide a higher one, so incomplete input makes the
// score a lower bound rather than withholding it.
func riskScore(sections []documentSection, diff []bundlepkg.Hunk) *documentRiskScore {
	score, unassessed := 0, 0
	var top []*documentSection
	raise := func(level int) {
		if level > score {
			score, top = level, nil
		}
	}
	for i := range sections {
		section := &sections[i]
		if section.Derived.State == "stale" {
			continue
		}
		level, _ := sectionReviewScore(section)
		raise(level)
		switch {
		case section.Kind == "mechanical":
		case section.Assessment == nil:
			unassessed++
		case section.Assessment.Level == score:
			top = append(top, section)
		}
	}

	reasons := []documentRiskScoreReason{}
	add := func(reason documentRiskScoreReason) {
		if len(reasons) < maxRiskScoreReasons {
			reasons = append(reasons, reason)
		}
	}
	// A concrete finding at the score explains it best; the assessments of
	// the sections at the score follow, one reason from each in turn.
	for i := range sections {
		section := &sections[i]
		if section.Derived.State == "stale" {
			continue
		}
		for _, risk := range section.Risks {
			if risk.State != "mitigated" && riskFloors[risk.Tier] == score {
				add(documentRiskScoreReason{Section: section.ID, Risk: &risk.ID, Text: risk.Text})
			}
		}
	}
	for round := range maxRiskScoreReasons {
		for _, section := range top {
			if round < len(section.Assessment.Reasons) {
				add(documentRiskScoreReason{Section: section.ID, Text: section.Assessment.Reasons[round].Text})
			}
		}
	}

	limitations := []documentRiskLimitation{}
	if unassessed > 0 {
		limitations = append(limitations, documentRiskLimitation{Kind: "unassessed_sections", Count: unassessed})
	}
	// A file the checkout left out was unreadable to the review. A patch cut
	// for size is not: the full file is still in the checkout. Generated and
	// vendored files, lockfiles among them, carry no behavior of their own.
	omitted := map[string]bool{}
	for _, hunk := range diff {
		if strings.HasPrefix(hunk.TextlessReason, "omitted_") && hunk.Role != bundlepkg.RoleGenerated && hunk.Role != bundlepkg.RoleVendored {
			omitted[hunk.Path] = true
		}
	}
	if len(omitted) > 0 {
		limitations = append(limitations, documentRiskLimitation{Kind: "omitted_source", Count: len(omitted)})
	}

	result := &documentRiskScore{Rubric: RiskRubricVersion, Status: "assessed", Reasons: reasons, Limitations: limitations}
	switch {
	case score == 0:
		result.Status = "not_assessed"
	case len(limitations) > 0:
		result.Status, result.Score = "lower_bound", &score
	default:
		result.Score = &score
	}
	return result
}

// sectionReviewScore is how much one current section asks of its reviewer,
// on the pull request score's own rules: its assessed level, 1 for the
// mechanical section, raised to the floor of any active high or critical risk.
// The pull request's score is the highest of these, so the section a reader
// meets first always carries it. The reason is the one riskScore would give:
// a risk at the score before the section's own assessment. A stale section,
// or a logical one with neither an assessment nor a floor, scores 0.
func sectionReviewScore(section *documentSection) (int, *documentReviewReason) {
	if section.Derived.State == "stale" {
		return 0, nil
	}
	score := 0
	switch {
	case section.Kind == "mechanical":
		score = 1
	case section.Assessment != nil:
		score = section.Assessment.Level
	}
	for _, risk := range section.Risks {
		if risk.State != "mitigated" {
			score = max(score, riskFloors[risk.Tier])
		}
	}
	if score == 0 {
		return 0, nil
	}
	for _, risk := range section.Risks {
		if risk.State != "mitigated" && riskFloors[risk.Tier] == score {
			return score, &documentReviewReason{Risk: &risk.ID, Text: risk.Text}
		}
	}
	if section.Kind != "mechanical" && section.Assessment != nil && section.Assessment.Level == score && len(section.Assessment.Reasons) > 0 {
		return score, &documentReviewReason{Text: section.Assessment.Reasons[0].Text}
	}
	return score, nil
}
