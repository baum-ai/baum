package replay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/baum-ai/baum/reviewv2"
)

// The v2 pair metrics.
const (
	MetricV2SectionsPrevious  = "v2_sections_previous"
	MetricV2SectionsReused    = "v2_sections_reused"
	MetricV2SectionsRewritten = "v2_sections_rewritten"
	MetricV2SectionsMatched   = "v2_sections_matched"
	MetricV2SectionsDropped   = "v2_sections_dropped"
	MetricV2SectionsNew       = "v2_sections_new"
	MetricV2BodySimilaritySum = "v2_body_similarity_sum"
	MetricV2BodyPairs         = "v2_body_pairs"
	MetricV2FactsPrevious     = "v2_facts_previous"
	MetricV2FactsKept         = "v2_facts_kept"
	MetricV2SummarySimilarity = "v2_summary_similarity"
	MetricV2OverviewSimilar   = "v2_overview_similarity"

	// The same four figures over changed pairs only (rewritten and matched).
	MetricV2ChangedSimilaritySum = "v2_changed_body_similarity_sum"
	MetricV2ChangedPairs         = "v2_changed_body_pairs"
	MetricV2ChangedFactsPrevious = "v2_changed_facts_previous"
	MetricV2ChangedFactsKept     = "v2_changed_facts_kept"
)

var v2PairDefinitions = map[string]string{
	MetricV2SectionsPrevious:  "sections of the earlier document",
	MetricV2SectionsReused:    "earlier sections the later document has under the same id, with the same hunks and an identical body",
	MetricV2SectionsRewritten: "earlier sections the later document has under the same id, with other hunks or another body",
	MetricV2SectionsMatched:   "earlier sections whose id the later document lacks but one of its sections matches by code (most shared hunks, then shared file paths)",
	MetricV2SectionsDropped:   "earlier sections with no same id and no match by code",
	MetricV2SectionsNew:       "later sections no earlier section was paired with, by id or by code",
	MetricV2BodySimilaritySum: "over the same-id and matched section pairs: the sum of the word-level Jaccard index of the two bodies' text (block titles, text, points' text and more); two empty bodies count 1",
	MetricV2BodyPairs:         "same-id and matched section pairs: the divisor of v2_body_similarity_sum",
	MetricV2FactsPrevious:     "over those pairs: facts in the earlier body, each counted once (bold spans, numbers such as 24, 5,000 and 3.5, and code spans)",
	MetricV2FactsKept:         "of those, facts present verbatim in the later body (a number as the same number, not part of another)",
	MetricV2SummarySimilarity: "word-level Jaccard index of the two summaries' text (title, text, risk_why, verdict text and points); left out when both are empty",
	MetricV2OverviewSimilar:   "word-level Jaccard index of the two overviews' intent text (asked and got); left out when both are empty",

	MetricV2ChangedSimilaritySum: "like v2_body_similarity_sum over changed pairs only (rewritten and matched sections), without the reused ones that are the same by construction",
	MetricV2ChangedPairs:         "changed pairs: the divisor of v2_changed_body_similarity_sum",
	MetricV2ChangedFactsPrevious: "like v2_facts_previous over changed pairs only",
	MetricV2ChangedFactsKept:     "like v2_facts_kept over changed pairs only",

	"v2_changed_body_similarity_pooled": "over all pairs: v2_changed_body_similarity_sum / v2_changed_body_pairs (the headline figure for how much a rewrite changed)",
	"v2_changed_fact_retention_pooled":  "over all pairs: v2_changed_facts_kept / v2_changed_facts_previous (the headline figure for facts a rewrite lost)",
	"v2_body_similarity_pooled":         "over all pairs: v2_body_similarity_sum / v2_body_pairs",
	"v2_fact_retention_pooled":          "over all pairs: v2_facts_kept / v2_facts_previous",
	"v2_section_reuse_pooled":           "over all pairs: v2_sections_reused / v2_sections_previous",
	"v2_section_dropped_pooled":         "over all pairs: v2_sections_dropped / v2_sections_previous",
}

// v2Section is a section with what the matching and the body comparison read.
type v2Section struct {
	section
	body    []reviewv2.Block
	hunkIDs []string
}

func v2SectionsOf(d reviewv2.Document) []v2Section {
	var index struct {
		Hunks []struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"hunks"`
	}
	_ = json.Unmarshal(d.Index, &index)
	pathOf := map[string]string{}
	for _, h := range index.Hunks {
		pathOf[h.ID] = h.Path
	}
	var out []v2Section
	for _, s := range d.Sections {
		sec := v2Section{section: section{id: s.ID, hunks: map[string]bool{}, paths: map[string]bool{}}, body: s.Body, hunkIDs: s.Hunks}
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

// bodyText is the text of a body: each text block's title and text, each
// point's text and more, markdown as written. Tile blocks have none.
func bodyText(body []reviewv2.Block) string {
	var parts []string
	for _, b := range body {
		if b.Type == reviewv2.BlockTile {
			continue
		}
		parts = append(parts, b.Title, b.Text)
		for _, p := range b.Items {
			parts = append(parts, p.Text, p.More)
		}
	}
	return strings.Join(parts, "\n")
}

// textSimilarity is the Jaccard index of the words of two texts; two texts
// with no word are the same.
func textSimilarity(a, b string) float64 {
	if len(words(a)) == 0 && len(words(b)) == 0 {
		return 1
	}
	return similarity(a, b)
}

var (
	factBold   = regexp.MustCompile(`\*\*(.+?)\*\*`)
	factCode   = regexp.MustCompile("`([^`]+)`")
	factNumber = regexp.MustCompile(`\b\d+(?:,\d{3})*(?:\.\d+)?\b`)
)

// facts are the checkable specifics of a text: bold spans and code spans (their
// inner text) and numbers.
type facts struct {
	spans, numbers map[string]bool
}

func factsOf(text string) facts {
	f := facts{spans: map[string]bool{}, numbers: map[string]bool{}}
	for _, re := range []*regexp.Regexp{factBold, factCode} {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			f.spans[m[1]] = true
		}
	}
	for _, n := range factNumber.FindAllString(text, -1) {
		f.numbers[n] = true
	}
	return f
}

// retained counts the facts of earlier that later has too.
func retained(earlier, later string) (total, kept int) {
	before, after := factsOf(earlier), factsOf(later)
	plain := strings.NewReplacer("**", "", "`", "").Replace(later)
	for s := range before.spans {
		total++
		if containsToken(plain, s) {
			kept++
		}
	}
	for n := range before.numbers {
		total++
		if after.numbers[n] {
			kept++
		}
	}
	return total, kept
}

// containsToken says text has s, not inside a longer word or number: where s
// starts or ends with a letter or digit, the text beside it is not one.
func containsToken(text, s string) bool {
	if s == "" {
		return false
	}
	word := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
	first, _ := utf8.DecodeRuneInString(s)
	last, _ := utf8.DecodeLastRuneInString(s)
	for from := 0; ; {
		i := strings.Index(text[from:], s)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(s)
		before, _ := utf8.DecodeLastRuneInString(text[:start])
		after, _ := utf8.DecodeRuneInString(text[end:])
		cutBefore := start > 0 && word(first) && word(before)
		cutAfter := end < len(text) && word(last) && word(after)
		if !cutBefore && !cutAfter {
			return true
		}
		from = start + 1
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := map[string]bool{}
	for _, x := range a {
		set[x] = true
	}
	for _, x := range b {
		if !set[x] {
			return false
		}
	}
	return len(set) == len(b)
}

func summaryText(d reviewv2.Document) string {
	parts := []string{d.Summary.Title, d.Summary.Text, d.Summary.RiskWhy}
	if v := d.Summary.Verdict; v != nil {
		parts = append(parts, v.Text)
		for _, p := range v.Points {
			parts = append(parts, p.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func intentText(d reviewv2.Document) string {
	var parts []string
	for _, i := range d.Overview.Intent {
		parts = append(parts, i.Asked, i.Got)
	}
	return strings.Join(parts, "\n")
}

// PairMetrics implements Meter. See Definitions for each key.
func (g *V2Generator) PairMetrics(previous, next []byte) (map[string]float64, error) {
	a, err := reviewv2.Read(previous)
	if err != nil {
		return nil, fmt.Errorf("the earlier document: %w", err)
	}
	b, err := reviewv2.Read(next)
	if err != nil {
		return nil, fmt.Errorf("the later document: %w", err)
	}
	before, after := v2SectionsOf(a), v2SectionsOf(b)
	m := map[string]float64{MetricV2SectionsPrevious: float64(len(before)), MetricV2SectionsReused: 0, MetricV2SectionsRewritten: 0, MetricV2SectionsMatched: 0,
		MetricV2SectionsDropped: 0, MetricV2SectionsNew: 0, MetricV2BodySimilaritySum: 0, MetricV2BodyPairs: 0, MetricV2FactsPrevious: 0, MetricV2FactsKept: 0,
		MetricV2ChangedSimilaritySum: 0, MetricV2ChangedPairs: 0, MetricV2ChangedFactsPrevious: 0, MetricV2ChangedFactsKept: 0}

	compare := func(earlier, later v2Section, changed bool) {
		x, y := bodyText(earlier.body), bodyText(later.body)
		similar := textSimilarity(x, y)
		total, kept := retained(x, y)
		m[MetricV2BodyPairs]++
		m[MetricV2BodySimilaritySum] += similar
		m[MetricV2FactsPrevious] += float64(total)
		m[MetricV2FactsKept] += float64(kept)
		if changed {
			m[MetricV2ChangedPairs]++
			m[MetricV2ChangedSimilaritySum] += similar
			m[MetricV2ChangedFactsPrevious] += float64(total)
			m[MetricV2ChangedFactsKept] += float64(kept)
		}
	}
	// A later section pairs with one earlier section: first the ones with the
	// same id, then the rest by code among the later sections still unpaired.
	claimed := map[int]bool{}
	laterAt := map[string]int{}
	for i, s := range after {
		laterAt[s.id] = i
	}
	var unpaired []v2Section
	for _, s := range before {
		i, ok := laterAt[s.id]
		if !ok {
			unpaired = append(unpaired, s)
			continue
		}
		later := after[i]
		claimed[i] = true
		earlierBody, _ := json.Marshal(s.body)
		laterBody, _ := json.Marshal(later.body)
		same := sameSet(s.hunkIDs, later.hunkIDs) && bytes.Equal(earlierBody, laterBody)
		if same {
			m[MetricV2SectionsReused]++
		} else {
			m[MetricV2SectionsRewritten]++
		}
		compare(s, later, !same)
	}
	for _, s := range unpaired {
		var candidates []section
		var at []int
		for i, later := range after {
			if !claimed[i] {
				candidates, at = append(candidates, later.section), append(at, i)
			}
		}
		if i := bestMatch(s.section, candidates); i >= 0 {
			claimed[at[i]] = true
			m[MetricV2SectionsMatched]++
			compare(s, after[at[i]], true)
		} else {
			m[MetricV2SectionsDropped]++
		}
	}
	m[MetricV2SectionsNew] = float64(len(after) - len(claimed))

	if x, y := summaryText(a), summaryText(b); len(words(x)) > 0 || len(words(y)) > 0 {
		m[MetricV2SummarySimilarity] = textSimilarity(x, y)
	}
	if x, y := intentText(a), intentText(b); len(words(x)) > 0 || len(words(y)) > 0 {
		m[MetricV2OverviewSimilar] = textSimilarity(x, y)
	}
	return m, nil
}

// Derived implements Deriver: ratios over the sums of a group.
func (g *V2Generator) Derived(sums map[string]float64) map[string]float64 {
	derived := map[string]float64{}
	if v := sums[MetricV2BodyPairs]; v > 0 {
		derived["v2_body_similarity_pooled"] = sums[MetricV2BodySimilaritySum] / v
	}
	if v := sums[MetricV2ChangedPairs]; v > 0 {
		derived["v2_changed_body_similarity_pooled"] = sums[MetricV2ChangedSimilaritySum] / v
	}
	if v := sums[MetricV2ChangedFactsPrevious]; v > 0 {
		derived["v2_changed_fact_retention_pooled"] = sums[MetricV2ChangedFactsKept] / v
	}
	if v := sums[MetricV2FactsPrevious]; v > 0 {
		derived["v2_fact_retention_pooled"] = sums[MetricV2FactsKept] / v
	}
	if v := sums[MetricV2SectionsPrevious]; v > 0 {
		derived["v2_section_reuse_pooled"] = sums[MetricV2SectionsReused] / v
		derived["v2_section_dropped_pooled"] = sums[MetricV2SectionsDropped] / v
	}
	return derived
}
