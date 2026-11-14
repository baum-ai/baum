package replay_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/replay"
)

// pricedAs is pricedReplies with the output tokens given.
func pricedAs(output int64) func(context.Context, agent.Request, int) (agent.Result, error) {
	inner := pricedReplies(nil)
	return func(c context.Context, r agent.Request, n int) (agent.Result, error) {
		result, err := inner(c, r, n)
		result.Usage.Output = output
		return result, err
	}
}

func TestMetricsOfARun(t *testing.T) {
	src := assembleSource(t)
	d := newDataset(t, src)
	d.addCase(t, "c1", map[string]any{"size": "s", "session_vendors": []string{"claude_code"}})
	d.addCase(t, "c2", map[string]any{"size": "m", "session_vendors": []string{"codex"}})
	d.addCase(t, "c3", map[string]any{"size": "m"})
	_ = os.Remove(filepath.Join(d.dir, "cases", "c3", "case.json"))
	ds, _ := replay.OpenDataset(d.dir)
	outDir := t.TempDir()
	priced := replay.Settings{Backends: fakeBackends(&generatetest.Backend{RunFunc: pricedAs(20000)}), Generator: &replay.ReviewGenerator{Staged: true}}
	summary, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Settings: priced})
	if err != nil || summary.Totals.OK != 2 || summary.Totals.Failed != 1 {
		t.Fatalf("%v %+v", err, summary.Totals)
	}
	runDir := filepath.Join(outDir, "run")
	m, err := replay.ReadMetrics(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Generator != "review" || len(m.Cases) != 3 || m.All.Statuses["ok"] != 2 || m.All.Statuses["failed"] != 1 || m.All.Reasons["case"] != 1 {
		t.Errorf("%+v", m.All)
	}
	// The statistics are over the cases that succeeded; the failed one is apart.
	cost := m.All.Stats[replay.MetricCost]
	if cost.N != 2 || cost.Sum != summary.Totals.CostUSD || cost.Median <= 0 || cost.P90 < cost.Median {
		t.Errorf("cost %+v, summary %v", cost, summary.Totals.CostUSD)
	}
	if m.All.Failed[replay.MetricWall].N != 1 {
		t.Errorf("failed %+v", m.All.Failed)
	}
	// A count that is zero is still a count: every case has every counter.
	for _, key := range []string{"sections", "risks", "risks_active_critical", "evidence_asserted", "evidence_verified", "hunks", "hunks_covered", "hunks_unassigned", "risk_lifecycle_violations"} {
		if m.All.Stats[key].N != 2 {
			t.Errorf("%s has N %d, want 2", key, m.All.Stats[key].N)
		}
	}
	if m.All.Stats["sections"].Min < 1 || m.All.Stats["risk_lifecycle_violations"].Sum != 0 {
		t.Errorf("sections %+v violations %+v", m.All.Stats["sections"], m.All.Stats["risk_lifecycle_violations"])
	}
	if m.BySize["m"].Cases != 2 || m.BySize["m"].Statuses["ok"] != 1 || m.BySize["s"].Cases != 1 || m.ByAgent["claude_code"].Cases != 1 || m.ByAgent["codex"].Cases != 1 || m.ByAgent["none"].Cases != 1 {
		t.Errorf("by size %v by agent %v", m.BySize, m.ByAgent)
	}
	if m.FirstPairs != nil || len(m.Chains) != 0 {
		t.Error("a run without chains has chain metrics")
	}
	var printed bytes.Buffer
	m.Print(&printed)
	for _, want := range []string{"run run, generator review", "status: failed 1, ok 2", "cases that succeeded (2 of 3)", "cases that ran and did not succeed", "cost_usd", "2*", "N below 5", "by size", "by agent", "definitions", "hunks_covered_ratio:"} {
		if !strings.Contains(printed.String(), want) {
			t.Errorf("the report lacks %q:\n%s", want, printed.String())
		}
	}
	if err := replay.WriteMetrics(runDir, m); err != nil {
		t.Fatal(err)
	}
	var back replay.RunMetrics
	raw, _ := os.ReadFile(filepath.Join(runDir, "metrics.json"))
	if err := json.Unmarshal(raw, &back); err != nil || back.All.Cases != 3 {
		t.Errorf("metrics.json: %v", err)
	}
}

func TestMetricsOfAChainRun(t *testing.T) {
	d, _, _ := chainDataset(t)
	ds, _ := replay.OpenDataset(d.dir)
	outDir := t.TempDir()
	seen := &bundleSeen{fail: "r-c"}
	if _, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Chain: true, Settings: staged(seen.backend())}); err != nil {
		t.Fatal(err)
	}
	m, err := replay.ReadMetrics(filepath.Join(outDir, "run"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Chains) != 2 || m.FirstPairs == nil || m.LaterPairs == nil || !m.Info.Chain {
		t.Fatalf("%+v", m.Chains)
	}
	// Pull request 7: r-a to r-b is the first pair, measured; r-b to r-c is a
	// later pair, not measured because r-c failed.
	for _, c := range m.Chains {
		if !strings.HasSuffix(c.PullRequest, "#7") {
			continue
		}
		if len(c.Pairs) != 2 || c.Pairs[0].Skipped != "" || !c.Pairs[0].First || c.Pairs[1].Skipped == "" || c.Pairs[1].First || c.Stopped == "" {
			t.Errorf("%+v", c)
		}
	}
	// Two first pairs (7 and 8), no later pair measured.
	if m.FirstPairs.Cases != 2 || m.LaterPairs.Cases != 0 {
		t.Errorf("first %d later %d", m.FirstPairs.Cases, m.LaterPairs.Cases)
	}
	// The stub keeps every ID and every section's hunks: survival is 1, and each
	// later section matches its own earlier one.
	stats := m.FirstPairs.Stats
	if stats["ids_previous"].N != 2 || stats["ids_previous"].Sum == 0 || stats["ids_survived"].Sum != stats["ids_previous"].Sum || stats["ids_unmatched"].N != 2 ||
		m.FirstPairs.Derived["id_survival_pooled"] != 1 || m.FirstPairs.Derived["continuity_pooled"] != 1 || m.FirstPairs.Derived["reverse_id_agreement_pooled"] != 1 {
		t.Errorf("%+v %v", stats, m.FirstPairs.Derived)
	}
	if stats["risk_lifecycle_violations"].Sum != 0 || stats["splits"].N != 2 {
		t.Errorf("%+v", stats)
	}
	var printed bytes.Buffer
	m.Print(&printed)
	for _, want := range []string{"first pair of each chain (2)", "id_survival_pooled", "id_survival_pooled 1.0000"} {
		if !strings.Contains(printed.String(), want) {
			t.Errorf("the report lacks %q:\n%s", want, printed.String())
		}
	}
	// Per pull request figures.
	for _, c := range m.Chains {
		if strings.HasSuffix(c.PullRequest, "#8") && (c.Totals["ids_previous"] == 0 || c.Derived["id_survival_pooled"] != 1) {
			t.Errorf("%+v", c)
		}
	}
}

func TestMetricsOfAGeneratorWithoutMeter(t *testing.T) {
	src := assembleSource(t)
	d := newDataset(t, src)
	d.addCase(t, "c1", nil)
	ds, _ := replay.OpenDataset(d.dir)
	outDir := t.TempDir()
	if _, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Settings: replay.Settings{Backends: fakeBackends(&generatetest.Backend{}), Generator: echo{valid: true}}}); err != nil {
		t.Fatal(err)
	}
	m, err := replay.ReadMetrics(filepath.Join(outDir, "run"))
	if err != nil || m.All.Stats[replay.MetricDocument].N != 1 || m.All.Stats[replay.MetricFailed].Sum != 1 || m.All.Stats["sections"].N != 0 {
		t.Fatalf("%v %+v", err, m.All)
	}
	var printed bytes.Buffer
	m.Print(&printed)
	if !strings.Contains(printed.String(), "has no metrics of its own") {
		t.Error(printed.String())
	}
}

// meterEcho is a generator that contributes its own metrics.
type meterEcho struct{ echo }

func (meterEcho) Name() string { return "meterecho" }
func (meterEcho) DocumentMetrics(document []byte) (map[string]float64, error) {
	return map[string]float64{"tiles": float64(len(document))}, nil
}
func (meterEcho) PairMetrics(previous, next []byte) (map[string]float64, error) {
	return map[string]float64{"tile_id_retention": 1}, nil
}

// A generator adds its metrics by registering a Meter, and itself by
// registering a constructor; the command changes nothing.
func TestAnotherGeneratorContributesItsMetrics(t *testing.T) {
	replay.RegisterMeter("meterecho", meterEcho{})
	t.Cleanup(func() { replay.UnregisterMeter("meterecho") })
	replay.RegisterGenerator("meterecho", func(c replay.GeneratorConfig) (replay.Generator, error) {
		return meterEcho{echo{valid: c.Options["valid"] == "yes"}}, nil
	})
	t.Cleanup(func() { replay.UnregisterGenerator("meterecho") })
	if !slices.Contains(replay.GeneratorNames(), "meterecho") || !slices.Contains(replay.GeneratorNames(), "review") {
		t.Errorf("generators %v", replay.GeneratorNames())
	}
	if _, err := replay.NewGenerator("nope", replay.GeneratorConfig{}); err == nil {
		t.Error("an unknown generator is built")
	}
	if _, err := replay.NewGenerator("review", replay.GeneratorConfig{Options: map[string]string{"x": "y"}}); err == nil {
		t.Error("the review generator accepts an option")
	}
	gen, err := replay.NewGenerator("meterecho", replay.GeneratorConfig{Options: map[string]string{"valid": "yes"}})
	if err != nil {
		t.Fatal(err)
	}
	for name, register := range map[string]func(){
		"meter":     func() { replay.RegisterMeter("meterecho", meterEcho{}) },
		"generator": func() { replay.RegisterGenerator("review", nil) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("registering a %s twice does not panic", name)
				}
			}()
			register()
		}()
	}
	src := assembleSource(t)
	d := newDataset(t, src)
	d.addCase(t, "c1", nil)
	ds, _ := replay.OpenDataset(d.dir)
	outDir := t.TempDir()
	if _, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Settings: replay.Settings{Backends: fakeBackends(&generatetest.Backend{}), Generator: gen}}); err != nil {
		t.Fatal(err)
	}
	m, err := replay.ReadMetrics(filepath.Join(outDir, "run"))
	if err != nil || m.All.Stats["tiles"].N != 1 || m.All.Stats["tiles"].Sum == 0 {
		t.Fatalf("%v %+v", err, m.All.Stats)
	}
}

func TestCompareAlignsCasesByID(t *testing.T) {
	src := assembleSource(t)
	d := newDataset(t, src)
	for _, id := range []string{"c1", "c2", "c3"} {
		d.addCase(t, id, nil)
	}
	ds, _ := replay.OpenDataset(d.dir)
	outDir := t.TempDir()
	runWith := func(name string, ids []string, backend func() *generatetest.Backend) replay.RunMetrics {
		t.Helper()
		settings := replay.Settings{Backends: fakeBackends(backend()), Generator: &replay.ReviewGenerator{Staged: true}}
		if _, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Name: name, Settings: settings, Selection: replay.Selection{IDs: ids}}); err != nil {
			t.Fatal(err)
		}
		m, err := replay.ReadMetrics(filepath.Join(outDir, name))
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	base := runWith("base", []string{"c1", "c2", "c3"}, func() *generatetest.Backend { return &generatetest.Backend{RunFunc: pricedAs(20000)} })
	// The same analysis, at twice the output tokens (cost) and with c3 left out.
	same := runWith("same", []string{"c1", "c2"}, func() *generatetest.Backend { return &generatetest.Backend{RunFunc: pricedAs(40000)} })
	c := replay.Compare(same, base)
	if c.Aligned != 2 || c.Completed != 2 || !slices.Equal(c.OnlyBaseline, []string{"c3"}) || len(c.OnlyRun) != 0 || len(c.StatusChanges) != 0 {
		t.Errorf("%+v", c)
	}
	// The case sets differ, so the selections differ: the comparison says so.
	if !slices.ContainsFunc(c.Warnings, func(w string) bool { return strings.Contains(w, "selection") }) {
		t.Errorf("warnings %v", c.Warnings)
	}
	cost := c.Metrics[replay.MetricCost]
	if cost.N != 2 || cost.Median <= 0 || cost.MedianRatio <= 1 || cost.Changed != 2 || c.Identical != 2 || c.Metrics["sections"].Changed != 0 || c.Equality == "" {
		t.Errorf("cost %+v identical %d", cost, c.Identical)
	}
	// Another analysis: c2 fails.
	failing := runWith("failing", []string{"c1", "c2"}, func() *generatetest.Backend {
		return &generatetest.Backend{RunFunc: func(cx context.Context, r agent.Request, n int) (agent.Result, error) {
			if n >= 3 {
				return agent.Result{Calls: 1}, os.ErrDeadlineExceeded
			}
			return pricedAs(20000)(cx, r, n)
		}}
	})
	c = replay.Compare(failing, base)
	if len(c.StatusChanges) != 1 || c.StatusChanges[0].CaseID != "c2" || c.StatusChanges[0].Run != "failed" || c.StatusChanges[0].Baseline != "ok" || c.Completed != 1 {
		t.Errorf("%+v", c)
	}
	// A document that differs in what it measures, in a key the baseline lacks,
	// and in a case whose metrics did not decode.
	changed := base
	changed.Cases = slices.Clone(base.Cases)
	for i := range changed.Cases {
		values := map[string]float64{}
		for k, v := range changed.Cases[i].Values {
			values[k] = v
		}
		values["sections"]++
		if i == 0 {
			values["tiles"] = 1
		}
		changed.Cases[i].Values = values
	}
	c = replay.Compare(changed, base)
	if c.Identical != 0 || c.Metrics["sections"].Median != 1 || c.Metrics["sections"].Changed != 3 || c.Metrics[replay.MetricCost].Changed != 0 || c.Metrics["tiles"].Missing != 1 || c.Metrics["tiles"].N != 0 {
		t.Errorf("%+v %+v", c.Metrics["sections"], c.Metrics["tiles"])
	}
	undecoded := base
	undecoded.Cases = slices.Clone(base.Cases)
	undecoded.Cases[0].Problem = "the document is not a review"
	if c = replay.Compare(undecoded, base); c.Identical != 2 || c.Undecoded != 1 {
		t.Errorf("identical %d undecoded %d", c.Identical, c.Undecoded)
	}
	var printed bytes.Buffer
	replay.Compare(changed, base).Print(&printed)
	for _, want := range []string{"identical document metrics in 0 of 3", "sections", "baseline", "a case is identical when"} {
		if !strings.Contains(printed.String(), want) {
			t.Errorf("the comparison lacks %q:\n%s", want, printed.String())
		}
	}
	raw, _ := json.Marshal(c)
	if !strings.Contains(string(raw), `"baseline"`) || strings.Contains(string(raw), `"other"`) {
		t.Errorf("the JSON fields: %s", raw)
	}
}

func docOf(version int, sections ...string) []byte {
	return []byte(`{"review":{"version":` + itoa(version) + `},"sections":[` + strings.Join(sections, ",") + `],"index":{"hunks":[` +
		`{"id":"h1","path":"a.go"},{"id":"h2","path":"a.go"},{"id":"h3","path":"b.go"},{"id":"h4","path":"c.go"},{"id":"h5","path":"b.go"}]},"unassigned_hunks":["h4"]}`)
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func sec(id, title, hunks string, risks ...string) string {
	return `{"id":"` + id + `","title":"` + title + `","hunks":[` + hunks + `],"risks":[` + strings.Join(risks, ",") + `]}`
}

func risk(id, tier, state string, introduced, mitigated string) string {
	return `{"id":"` + id + `","tier":"` + tier + `","state":"` + state + `","introduced_version":` + introduced + `,"mitigated_version":` + mitigated + `}`
}

func TestReviewMetrics(t *testing.T) {
	g := &replay.ReviewGenerator{}
	doc := docOf(3,
		sec("a", "A", `"h1","h2"`, risk("r1", "high", "active", "2", "null"), risk("r2", "low", "mitigated", "1", "2")),
		sec("b", "B", `"h3"`, risk("r3", "critical", "active", "3", "null")),
	)
	m, err := g.DocumentMetrics(doc)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]float64{"sections": 2, "sections_without_hunks": 0, "risks": 3, "risks_active": 2, "risks_active_high": 1, "risks_active_critical": 1, "risks_active_low": 0,
		"risks_active_medium": 0, "risks_mitigated": 1, "hunks": 5, "hunks_covered": 3, "hunks_unassigned": 1, "hunks_covered_ratio": 0.6, "risk_lifecycle_violations": 0,
		"evidence": 0, "evidence_asserted": 0, "evidence_verified": 0} {
		if got, ok := m[key]; !ok || got != want {
			t.Errorf("%s = %v (present %v), want %v", key, got, ok, want)
		}
	}
	// The ratio has no denominator without an index; the counts stay.
	empty, _ := g.DocumentMetrics([]byte(`{"sections":[]}`))
	if _, ok := empty["hunks_covered_ratio"]; ok || empty["sections"] != 0 || empty["risks"] != 0 {
		t.Errorf("%v", empty)
	}
	// Contradictions: a risk both active and mitigated, in one risk or in two
	// sections, an unknown state, a version above the document's and a risk
	// mitigated before it was introduced.
	bad := docOf(2,
		sec("a", "A", `"h1"`, risk("r1", "high", "active", "1", "2"), risk("r2", "low", "mitigated", "1", "null")),
		sec("b", "B", `"h2"`, risk("r2", "low", "active", "1", "null"), risk("r9", "low", "unknown", "1", "null"), risk("r8", "low", "active", "5", "null"), risk("r7", "low", "mitigated", "2", "1")),
	)
	if m, _ := g.DocumentMetrics(bad); m["risk_lifecycle_violations"] != 6 {
		t.Errorf("violations %v, want 6", m["risk_lifecycle_violations"])
	}
	if _, err := g.DocumentMetrics([]byte("not json")); err == nil {
		t.Error("a document that is not JSON is measured")
	}
}

func TestReviewPairMetrics(t *testing.T) {
	g := &replay.ReviewGenerator{}
	previous := docOf(1,
		sec("a", "Retry policy", `"h1","h2"`, risk("r1", "high", "active", "1", "null"), risk("r2", "low", "active", "1", "null"), risk("r3", "low", "mitigated", "1", "1"), risk("r4", "low", "active", "1", "null")),
		sec("b", "Billing", `"h3"`),
		sec("gone", "Gone", `"h4"`),
	)
	next := docOf(2,
		sec("a", "Retry policy", `"h1"`, risk("r1", "high", "mitigated", "1", "2"), risk("r2", "low", "active", "2", "null"), risk("r3", "low", "active", "1", "null")),
		sec("renamed", "Billing", `"h5"`),
		sec("new", "Brand new", `"h9"`),
	)
	m, err := g.PairMetrics(previous, next)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]float64{
		"sections_previous": 3, "sections_next": 3,
		// a survives with its hunk; b and gone do not survive by ID.
		"ids_previous": 3, "ids_survived": 1, "ids_unmatched": 2, "id_survival": 1.0 / 3, "survived_matched_hunk": 1, "continuity": 1,
		// renamed matches b by path (h5 and h3 are both in b.go) and not by ID.
		"reverse_matched": 2, "reverse_same_id": 1, "reverse_id_agreement": 0.5, "sections_new": 1, "sections_dropped": 1, "splits": 0, "merges": 0,
		"risks_carried": 1, "risks_newly_mitigated": 1, "risks_reopened": 1, "risks_introduced": 0, "risks_vanished": 1, "risk_introduced_changed": 1,
		// the changed introduced_version counts as a violation.
		"risk_lifecycle_violations": 1,
	} {
		if got, ok := m[key]; !ok || got != want {
			t.Errorf("%s = %v (present %v), want %v", key, got, ok, want)
		}
	}
}

// A section whose hunks all changed survives by ID and is matched by path only;
// a split and a merge are told apart from renames; a section that vanishes and
// is replaced by one with the same words in its title still matches.
func TestReviewPairContinuityAndSplitsAndMerges(t *testing.T) {
	g := &replay.ReviewGenerator{}
	// h1,h2 in a.go change to h4 in c.go: no hunk and no path in common.
	m, _ := g.PairMetrics(docOf(1, sec("a", "A", `"h1","h2"`)), docOf(2, sec("a", "Zed", `"h3"`)))
	if m["survived_no_overlap"] != 1 || m["continuity"] != 0 || m["reverse_matched"] != 0 || m["sections_new"] != 1 {
		t.Errorf("no overlap: %v", m)
	}
	// h5 replaces h3 in the same file: matched by path only.
	m, _ = g.PairMetrics(docOf(1, sec("a", "A", `"h3"`)), docOf(2, sec("a", "A", `"h5"`)))
	if m["survived_matched_path_only"] != 1 || m["survived_matched_hunk"] != 0 || m["continuity"] != 1 || m["reverse_matched"] != 1 || m["reverse_same_id"] != 1 {
		t.Errorf("path only: %v", m)
	}
	// A split: one earlier section is the best match of two later ones.
	m, _ = g.PairMetrics(docOf(1, sec("a", "A", `"h1","h3"`)), docOf(2, sec("x", "X", `"h1"`), sec("y", "Y", `"h3"`)))
	if m["splits"] != 1 || m["merges"] != 0 || m["ids_survived"] != 0 || m["reverse_matched"] != 2 || m["reverse_same_id"] != 0 {
		t.Errorf("split: %v", m)
	}
	// A merge: two earlier sections' best match is the same later one.
	m, _ = g.PairMetrics(docOf(1, sec("x", "X", `"h1"`), sec("y", "Y", `"h3"`)), docOf(2, sec("a", "A", `"h1","h3"`)))
	if m["merges"] != 1 || m["splits"] != 0 || m["reverse_matched"] != 1 {
		t.Errorf("merge: %v", m)
	}
	// Titles decide when nothing else is shared.
	m, _ = g.PairMetrics(docOf(1, sec("a", "Retry policy and backoff", "")), docOf(2, sec("b", "Retry policy backoff", "")))
	if m["reverse_matched"] != 1 || m["reverse_same_id"] != 0 || m["survived_empty"] != 0 || m["ids_survived"] != 0 {
		t.Errorf("titles: %v", m)
	}
	// A stale earlier section (no hunks) that survives has no content to continue.
	m, _ = g.PairMetrics(docOf(1, sec("a", "A", "")), docOf(2, sec("a", "A", `"h1"`)))
	if m["survived_empty"] != 1 {
		t.Errorf("empty: %v", m)
	}
	if _, ok := m["continuity"]; ok {
		t.Errorf("continuity has a denominator of zero but is %v", m["continuity"])
	}
	derived := g.Derived(map[string]float64{"ids_previous": 4, "ids_survived": 3, "survived_matched_hunk": 1, "survived_matched_path_only": 1, "survived_no_overlap": 1, "reverse_matched": 4, "reverse_same_id": 3})
	if derived["id_survival_pooled"] != 0.75 || derived["continuity_pooled"] != 2.0/3 || derived["reverse_id_agreement_pooled"] != 0.75 {
		t.Errorf("%v", derived)
	}
}
