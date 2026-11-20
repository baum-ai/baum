package replay

import (
	"encoding/json"
	"testing"

	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/reviewv2"
)

func v2Doc(t *testing.T, version int, sections ...reviewv2.Section) []byte {
	t.Helper()
	doc := reviewv2.Document{
		Schema:   reviewv2.Name,
		Review:   json.RawMessage(`{"version":` + string(rune('0'+version)) + `}`),
		Index:    json.RawMessage(`{"hunks":[{"id":"h1","path":"a.go"},{"id":"h2","path":"a.go"},{"id":"h3","path":"b.go"},{"id":"h4","path":"c.go"},{"id":"h5","path":"c.go"}]}`),
		Sections: sections,
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func prose(text string) []reviewv2.Block {
	return []reviewv2.Block{{Type: reviewv2.BlockProse, Text: text}}
}

func TestV2PairSectionFates(t *testing.T) {
	g := &V2Generator{}
	previous := v2Doc(t, 1,
		reviewv2.Section{ID: "sec_same", Hunks: []string{"h1"}, Body: prose("The cache keeps **ten** entries.")},
		reviewv2.Section{ID: "sec_rewritten", Hunks: []string{"h3"}, Body: prose("Old words here.")},
		reviewv2.Section{ID: "sec_moved", Hunks: []string{"h4"}, Body: prose("Reads from `store`.")},
		reviewv2.Section{ID: "sec_gone", Hunks: []string{"h5x"}, Body: prose("Gone.")},
	)
	next := v2Doc(t, 2,
		reviewv2.Section{ID: "sec_same", Hunks: []string{"h1"}, Body: prose("The cache keeps **ten** entries.")},
		reviewv2.Section{ID: "sec_rewritten", Hunks: []string{"h3"}, Body: prose("Entirely different text.")},
		reviewv2.Section{ID: "sec_renamed", Hunks: []string{"h4"}, Body: prose("Reads from `store`.")},
		reviewv2.Section{ID: "sec_fresh", Hunks: []string{"h2x"}, Body: prose("New.")},
	)
	m, err := g.PairMetrics(previous, next)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{
		MetricV2SectionsPrevious: 4, MetricV2SectionsReused: 1, MetricV2SectionsRewritten: 1, MetricV2SectionsMatched: 1,
		MetricV2SectionsDropped: 1, MetricV2SectionsNew: 1, MetricV2BodyPairs: 3,
		// ten, `store`: kept; sec_rewritten has no fact.
		MetricV2FactsPrevious: 2, MetricV2FactsKept: 2,
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
	// Two identical bodies and one with no word in common.
	if m[MetricV2BodySimilaritySum] != 2 {
		t.Errorf("similarity sum %v, want 2", m[MetricV2BodySimilaritySum])
	}
	for name := range m {
		if g.Definitions()[name] == "" {
			t.Errorf("%s has no definition", name)
		}
	}
	derived := g.Derived(m)
	if derived["v2_section_reuse_pooled"] != 0.25 || derived["v2_fact_retention_pooled"] != 1 {
		t.Errorf("derived %v", derived)
	}
	for name := range derived {
		if g.Definitions()[name] == "" {
			t.Errorf("%s has no definition", name)
		}
	}
}

// TestV2PairLosesFactsOfARewrittenSection is the v2 to v3 shape of a review
// that kept its section but dropped the specifics.
func TestV2PairLosesFactsOfARewrittenSection(t *testing.T) {
	g := &V2Generator{}
	points := func(text string) []reviewv2.Block {
		return []reviewv2.Block{{Type: reviewv2.BlockPoints, Items: []reviewv2.Point{{Text: text}}}}
	}
	previous := v2Doc(t, 2, reviewv2.Section{ID: "sec_defaults", Hunks: []string{"h1"},
		Body: points("Defaults are **24 repositories, 320 pull requests, a 5,000-line diff, 20 inboxes, 8 Claude Code and 4 Codex sessions**.")})
	next := v2Doc(t, 3, reviewv2.Section{ID: "sec_defaults", Hunks: []string{"h1", "h2"},
		Body: points("The defaults load a large set of repositories and sessions.")})
	m, err := g.PairMetrics(previous, next)
	if err != nil {
		t.Fatal(err)
	}
	if m[MetricV2SectionsRewritten] != 1 || m[MetricV2SectionsReused] != 0 {
		t.Errorf("rewritten %v reused %v", m[MetricV2SectionsRewritten], m[MetricV2SectionsReused])
	}
	// The bold span, and the numbers 24, 320, 5,000, 20, 8 and 4.
	if m[MetricV2FactsPrevious] != 7 || m[MetricV2FactsKept] != 0 {
		t.Errorf("facts previous %v kept %v", m[MetricV2FactsPrevious], m[MetricV2FactsKept])
	}
	if got := g.Derived(m)["v2_fact_retention_pooled"]; got != 0 {
		t.Errorf("retention %v", got)
	}
}

func TestV2FactsMatchNumbersWhole(t *testing.T) {
	total, kept := retained("Costs 3.5 and 5,000 of **x** and `y`.", "Costs 13.55 and 5,000,000 of x and y.")
	if total != 4 || kept != 2 {
		t.Errorf("total %d kept %d, want 4 and 2 (x and y)", total, kept)
	}
}

func TestV2SummaryAndOverviewSimilarity(t *testing.T) {
	g := &V2Generator{}
	doc := func(title string, asked string) []byte {
		d := reviewv2.Document{Schema: reviewv2.Name, Review: json.RawMessage(`{"version":1}`), Summary: reviewv2.Summary{Title: title, Text: "same text"},
			Overview: reviewv2.Overview{Intent: []reviewv2.Intent{{Asked: asked, Got: "done"}}}}
		out, _ := json.Marshal(d)
		return out
	}
	m, err := g.PairMetrics(doc("Cache", "make it fast"), doc("Cache", "make it fast"))
	if err != nil || m[MetricV2SummarySimilarity] != 1 || m[MetricV2OverviewSimilar] != 1 {
		t.Fatalf("%v %v", m, err)
	}
	m, _ = g.PairMetrics(doc("Cache", "make it fast"), doc("Cache", "make it slow"))
	if m[MetricV2OverviewSimilar] >= 1 || m[MetricV2SummarySimilarity] != 1 {
		t.Errorf("%v", m)
	}
	empty, _ := json.Marshal(reviewv2.Document{Schema: reviewv2.Name, Review: json.RawMessage(`{"version":1}`)})
	m, _ = g.PairMetrics(empty, empty)
	if _, ok := m[MetricV2OverviewSimilar]; ok {
		t.Error("an overview with no intent has a similarity")
	}
}

func TestV2ChainedRevisionReadsThePreviousDocument(t *testing.T) {
	previous := v2Doc(t, 2, reviewv2.Section{ID: "sec_a", Hunks: []string{"h1"}, Body: prose("x")})
	c := Case{ID: "r-b", BundleJSON: []byte(`{"previous":{"version":9,"sections":[]}}`)}

	g := &V2Generator{}
	first := g.options(generate.Options{}, Case{ID: "r-a"}, nil)
	if first.V2.Previous != nil || first.V2.PreviousVersion != 0 {
		t.Errorf("the first revision has a previous version: %d", first.V2.PreviousVersion)
	}
	b, err := g.Next(c, previous)
	if err != nil {
		t.Fatal(err)
	}
	if b.Previous != nil {
		t.Errorf("the bundle carries a previous version: %+v", b.Previous)
	}
	o := g.options(generate.Options{}, c, nil)
	if string(o.V2.Previous) != string(previous) || o.V2.PreviousVersion != 2 {
		t.Errorf("previous %q version %d", o.V2.Previous, o.V2.PreviousVersion)
	}
	if _, err := g.Next(c, []byte(`{"schema":"baum.review/2"}`)); err == nil {
		t.Error("a document with no version was handed on")
	}
}

func TestV2ReuseIsOptIn(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		g := &V2Generator{Reuse: reuse}
		o := g.options(generate.Options{}, Case{ID: "x"}, nil)
		if o.NoReuse == reuse || o.V2.NoReuse == reuse {
			t.Errorf("reuse %v: NoReuse %v, V2.NoReuse %v", reuse, o.NoReuse, o.V2.NoReuse)
		}
	}
	g, err := NewGenerator("v2", GeneratorConfig{Reuse: true})
	if err != nil || !g.(*V2Generator).Reuse {
		t.Errorf("--reuse does not reach the v2 generator: %v", err)
	}
}

func TestV2FactsMatchSpansAtTokenBoundaries(t *testing.T) {
	for _, c := range []struct {
		earlier, later string
		kept           int
	}{
		{"Used **id** and `ok`.", "Please provide a book.", 0},
		{"Used **id** and `ok`.", "The id and ok values.", 2},
		{"Runs **4** jobs.", "Runs 14 jobs.", 0},
		{"Uses `a.b`.", "Uses (a.b).", 1},
	} {
		if _, kept := retained(c.earlier, c.later); kept != c.kept {
			t.Errorf("%q in %q: kept %d, want %d", c.earlier, c.later, kept, c.kept)
		}
	}
	// A bold span and the numbers inside it are separate facts.
	if total, _ := retained("**24 repos**", ""); total != 2 {
		t.Errorf("total %d, want 2", total)
	}
}

// A later section pairs once: B shares h1 with A', which A already claimed by id.
func TestV2PairClaimsALaterSectionOnce(t *testing.T) {
	g := &V2Generator{}
	previous := v2Doc(t, 1,
		reviewv2.Section{ID: "sec_a", Hunks: []string{"h1"}, Body: prose("Alpha.")},
		reviewv2.Section{ID: "sec_b", Hunks: []string{"h1", "h2"}, Body: prose("Beta.")},
	)
	next := v2Doc(t, 2, reviewv2.Section{ID: "sec_a", Hunks: []string{"h1"}, Body: prose("Alpha.")})
	m, err := g.PairMetrics(previous, next)
	if err != nil {
		t.Fatal(err)
	}
	if m[MetricV2SectionsReused] != 1 || m[MetricV2SectionsMatched] != 0 || m[MetricV2SectionsDropped] != 1 || m[MetricV2BodyPairs] != 1 || m[MetricV2SectionsNew] != 0 {
		t.Errorf("%v", m)
	}
}

func TestV2ChangedPairsLeaveOutReusedSections(t *testing.T) {
	g := &V2Generator{}
	previous := v2Doc(t, 1,
		reviewv2.Section{ID: "sec_a", Hunks: []string{"h1"}, Body: prose("Keeps **ten** items.")},
		reviewv2.Section{ID: "sec_b", Hunks: []string{"h3"}, Body: prose("Holds **five** and 5 things.")},
	)
	next := v2Doc(t, 2,
		reviewv2.Section{ID: "sec_a", Hunks: []string{"h1"}, Body: prose("Keeps **ten** items.")},
		reviewv2.Section{ID: "sec_b", Hunks: []string{"h3"}, Body: prose("Something else.")},
	)
	m, err := g.PairMetrics(previous, next)
	if err != nil {
		t.Fatal(err)
	}
	if m[MetricV2ChangedPairs] != 1 || m[MetricV2ChangedSimilaritySum] != 0 || m[MetricV2ChangedFactsPrevious] != 2 || m[MetricV2ChangedFactsKept] != 0 {
		t.Errorf("%v", m)
	}
	// Over all pairs the reused section dilutes the figures.
	if m[MetricV2BodyPairs] != 2 || m[MetricV2BodySimilaritySum] != 1 || m[MetricV2FactsPrevious] != 3 || m[MetricV2FactsKept] != 1 {
		t.Errorf("%v", m)
	}
	d := g.Derived(m)
	if d["v2_changed_body_similarity_pooled"] != 0 || d["v2_changed_fact_retention_pooled"] != 0 || d["v2_fact_retention_pooled"] != 1.0/3 {
		t.Errorf("%v", d)
	}
}
