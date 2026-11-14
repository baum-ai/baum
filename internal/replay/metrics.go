package replay

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
)

// Meter is implemented by a generator that can measure its own documents. The
// metrics command knows nothing of a document's format: it asks the run's
// generator (by the name its manifest records, see RegisterMeter) for numbers
// and aggregates whatever it gets. Keys that a document does not have are
// simply absent.
type Meter interface {
	// DocumentMetrics measures one generated document.
	DocumentMetrics(document []byte) (map[string]float64, error)
	// PairMetrics measures how the document of a revision relates to the
	// document of the revision before it in a chain.
	PairMetrics(previous, next []byte) (map[string]float64, error)
}

// Deriver is implemented by a Meter that also reports figures computed from
// the sums of a group's metrics, such as a ratio over all cases rather than
// the mean of each case's.
type Deriver interface {
	Derived(sums map[string]float64) map[string]float64
}

var meters = map[string]Meter{}

// RegisterMeter makes a generator's metrics available to `replay metrics` for
// runs whose manifest names it. Call it at initialisation.
func RegisterMeter(generator string, m Meter) { meters[generator] = m }

// MeterFor returns the metrics of a generator, or nil when it has none.
func MeterFor(generator string) Meter { return meters[generator] }

// Stat summarises the values of one metric.
type Stat struct {
	N                                int
	Sum, Mean, Median, P90, Min, Max float64
}

func statOf(values []float64) Stat {
	if len(values) == 0 {
		return Stat{}
	}
	sorted := slices.Sorted(slices.Values(values))
	s := Stat{N: len(sorted), Min: sorted[0], Max: sorted[len(sorted)-1], Median: quantile(sorted, 0.5), P90: quantile(sorted, 0.9)}
	for _, v := range sorted {
		s.Sum += v
	}
	s.Mean = s.Sum / float64(len(sorted))
	return s
}

// quantile interpolates linearly between the closest ranks of sorted values.
func quantile(sorted []float64, q float64) float64 {
	position := q * float64(len(sorted)-1)
	low := int(math.Floor(position))
	high := int(math.Ceil(position))
	return sorted[low] + (sorted[high]-sorted[low])*(position-float64(low))
}

// Metric names every generator's cases have.
const (
	MetricWall       = "wall_seconds"
	MetricCalls      = "calls"
	MetricTokens     = "tokens"
	MetricCost       = "cost_usd"
	MetricRepairs    = "repairs"
	MetricFailed     = "failed_parts"
	MetricDocument   = "document_bytes"
	MetricValidation = "validation_findings"
)

// runMetrics are the keys the runner's own outcome gives; any other key of a
// case comes from the generator's Meter.
var runMetrics = []string{MetricWall, MetricCalls, MetricTokens, MetricCost, MetricRepairs, MetricFailed, MetricDocument, MetricValidation}

// CaseMetrics are the numbers of one case.
type CaseMetrics struct {
	CaseID string `json:"case_id"`
	// Status is the outcome's, or "skipped" for a case the run never started.
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Size   string `json:"size,omitempty"`
	// Agent is the agent of the case's sessions: claude_code, codex, both or none.
	Agent  string             `json:"agent,omitempty"`
	Values map[string]float64 `json:"values,omitempty"`
	// Problem says why the generator's metrics could not be read.
	Problem string `json:"problem,omitempty"`
}

// Group aggregates a set of cases (or of chain pairs).
type Group struct {
	Cases    int                `json:"cases"`
	Statuses map[string]int     `json:"statuses,omitempty"`
	Reasons  map[string]int     `json:"reasons,omitempty"`
	Stats    map[string]Stat    `json:"stats"`
	Derived  map[string]float64 `json:"derived,omitempty"`
}

// PairRecord is the metrics of one revision against the one before it.
type PairRecord struct {
	From   string             `json:"from"`
	To     string             `json:"to"`
	Values map[string]float64 `json:"values,omitempty"`
	// Skipped says why the pair was not measured.
	Skipped string `json:"skipped,omitempty"`
}

// ChainReport is the measurements of one pull request's chain.
type ChainReport struct {
	PullRequest string       `json:"pull_request"`
	Cases       []string     `json:"cases"`
	Stopped     string       `json:"stopped,omitempty"`
	Pairs       []PairRecord `json:"pairs"`
}

// RunMetrics is metrics.json.
type RunMetrics struct {
	Run       string           `json:"run"`
	Generator string           `json:"generator"`
	Cases     []CaseMetrics    `json:"cases"`
	All       Group            `json:"all"`
	BySize    map[string]Group `json:"by_size,omitempty"`
	ByAgent   map[string]Group `json:"by_agent,omitempty"`
	Chains    []ChainReport    `json:"chains,omitempty"`
	// Pairs aggregates the measured pairs of every chain.
	Pairs *Group `json:"pairs,omitempty"`
}

func agentOf(vendors []string) string {
	claude, codex := slices.Contains(vendors, "claude_code"), slices.Contains(vendors, "codex")
	switch {
	case claude && codex:
		return "both"
	case claude:
		return "claude_code"
	case codex:
		return "codex"
	}
	return "none"
}

// ReadMetrics reads a finished run directory and measures it. It calls no model.
func ReadMetrics(dir string) (RunMetrics, error) {
	var manifest Manifest
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return RunMetrics{}, fmt.Errorf("%s is not a replay run: %w", dir, err)
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return RunMetrics{}, fmt.Errorf("%s: manifest.json: %w", dir, err)
	}
	meter := MeterFor(manifest.Generator)
	m := RunMetrics{Run: manifest.Run, Generator: manifest.Generator}
	entries, err := os.ReadDir(filepath.Join(dir, "cases"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return m, err
	}
	have := map[string]bool{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		have[entry.Name()] = true
		m.Cases = append(m.Cases, readCase(filepath.Join(dir, "cases", entry.Name()), entry.Name(), manifest, meter))
	}
	var summary Summary
	if raw, err := os.ReadFile(filepath.Join(dir, "summary.json")); err == nil && json.Unmarshal(raw, &summary) == nil {
		for _, row := range summary.Rows {
			if row.Status == "skipped" && !have[row.CaseID] {
				c := CaseMetrics{CaseID: row.CaseID, Status: "skipped", Reason: row.Reason}
				fillFacts(&c, manifest)
				m.Cases = append(m.Cases, c)
			}
		}
	}
	slices.SortFunc(m.Cases, func(a, b CaseMetrics) int { return strings.Compare(a.CaseID, b.CaseID) })

	m.All = group(m.Cases, meter)
	m.BySize, m.ByAgent = map[string]Group{}, map[string]Group{}
	bySize, byAgent := map[string][]CaseMetrics{}, map[string][]CaseMetrics{}
	for _, c := range m.Cases {
		bySize[cmp.Or(c.Size, "unknown")] = append(bySize[cmp.Or(c.Size, "unknown")], c)
		byAgent[cmp.Or(c.Agent, "unknown")] = append(byAgent[cmp.Or(c.Agent, "unknown")], c)
	}
	for key, cases := range bySize {
		m.BySize[key] = group(cases, meter)
	}
	for key, cases := range byAgent {
		m.ByAgent[key] = group(cases, meter)
	}
	if err := readChains(dir, &m, meter); err != nil {
		return m, err
	}
	return m, nil
}

func fillFacts(c *CaseMetrics, manifest Manifest) {
	if facts, ok := manifest.CaseFacts[c.CaseID]; ok {
		c.Size, c.Agent = facts.Size, agentOf(facts.Sessions)
	}
}

func readCase(dir, id string, manifest Manifest, meter Meter) CaseMetrics {
	c := CaseMetrics{CaseID: id}
	fillFacts(&c, manifest)
	out, err := ReadOutcome(dir)
	if err != nil {
		c.Status, c.Problem = "unfinished", "outcome.json: "+err.Error()
		return c
	}
	c.Status, c.Reason = out.Status, out.Reason
	c.Values = map[string]float64{MetricWall: out.WallSeconds, MetricCalls: float64(out.Calls), MetricTokens: float64(out.Tokens),
		MetricRepairs: float64(out.Repairs), MetricFailed: float64(len(out.Notes)), MetricValidation: float64(len(out.ValidationFindings))}
	if out.CostUSD != nil {
		c.Values[MetricCost] = *out.CostUSD
	}
	document, err := os.ReadFile(filepath.Join(dir, "document.json"))
	if err != nil {
		return c
	}
	c.Values[MetricDocument] = float64(len(document))
	if meter != nil {
		if values, err := meter.DocumentMetrics(document); err != nil {
			c.Problem = err.Error()
		} else {
			maps.Copy(c.Values, values)
		}
	}
	return c
}

func group(cases []CaseMetrics, meter Meter) Group {
	g := Group{Cases: len(cases), Statuses: map[string]int{}, Reasons: map[string]int{}}
	values := map[string][]float64{}
	for _, c := range cases {
		g.Statuses[c.Status]++
		if c.Status != "ok" && c.Reason != "" {
			g.Reasons[c.Reason]++
		}
		for key, v := range c.Values {
			values[key] = append(values[key], v)
		}
	}
	g.Stats = statsOf(values)
	g.Derived = derive(g.Stats, meter)
	return g
}

func statsOf(values map[string][]float64) map[string]Stat {
	stats := map[string]Stat{}
	for key, v := range values {
		stats[key] = statOf(v)
	}
	return stats
}

func derive(stats map[string]Stat, meter Meter) map[string]float64 {
	deriver, ok := meter.(Deriver)
	if !ok {
		return nil
	}
	sums := map[string]float64{}
	for key, s := range stats {
		sums[key] = s.Sum
	}
	return deriver.Derived(sums)
}

// readChains measures each pair of consecutive revisions whose documents both
// exist, from the chains/<pull request>/chain.json records the run wrote.
func readChains(dir string, m *RunMetrics, meter Meter) error {
	entries, err := os.ReadDir(filepath.Join(dir, "chains"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	values := map[string][]float64{}
	pairs := 0
	for _, entry := range entries {
		var record ChainRecord
		raw, err := os.ReadFile(filepath.Join(dir, "chains", entry.Name(), "chain.json"))
		if err != nil || json.Unmarshal(raw, &record) != nil {
			continue
		}
		report := ChainReport{PullRequest: record.PullRequest, Stopped: record.Stopped}
		var previous []byte
		for i, rev := range record.Revisions {
			report.Cases = append(report.Cases, rev.CaseID)
			document, _ := os.ReadFile(filepath.Join(dir, "cases", rev.CaseID, "document.json"))
			if i > 0 {
				pair := PairRecord{From: record.Revisions[i-1].CaseID, To: rev.CaseID}
				switch {
				case meter == nil:
					pair.Skipped = "the generator has no metrics"
				case previous == nil || document == nil:
					pair.Skipped = "a revision has no document"
				default:
					if pair.Values, err = meter.PairMetrics(previous, document); err != nil {
						pair.Skipped, pair.Values = err.Error(), nil
					}
				}
				for key, v := range pair.Values {
					values[key] = append(values[key], v)
				}
				if pair.Skipped == "" {
					pairs++
				}
				report.Pairs = append(report.Pairs, pair)
			}
			previous = document
		}
		m.Chains = append(m.Chains, report)
	}
	slices.SortFunc(m.Chains, func(a, b ChainReport) int { return strings.Compare(a.PullRequest, b.PullRequest) })
	if len(m.Chains) > 0 {
		g := Group{Cases: pairs, Stats: statsOf(values)}
		g.Derived = derive(g.Stats, meter)
		m.Pairs = &g
	}
	return nil
}

// orderedKeys lists a stats map's keys: the runner's own metrics first, in
// their fixed order, then the generator's, sorted.
func orderedKeys(stats map[string]Stat) []string {
	var keys []string
	for _, key := range runMetrics {
		if _, ok := stats[key]; ok {
			keys = append(keys, key)
		}
	}
	var rest []string
	for key := range stats {
		if !slices.Contains(runMetrics, key) {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	return append(keys, rest...)
}

func countsText(counts map[string]int) string {
	var parts []string
	for _, key := range slices.Sorted(maps.Keys(counts)) {
		parts = append(parts, fmt.Sprintf("%s %d", key, counts[key]))
	}
	return strings.Join(parts, ", ")
}

func number(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e9 {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.3g", v)
}

func (g Group) printStats(w io.Writer) {
	table := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "METRIC\tN\tMEDIAN\tP90\tMEAN\tMIN\tMAX\tSUM")
	for _, key := range orderedKeys(g.Stats) {
		s := g.Stats[key]
		_, _ = fmt.Fprintf(table, "%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\n", key, s.N, number(s.Median), number(s.P90), number(s.Mean), number(s.Min), number(s.Max), number(s.Sum))
	}
	for _, key := range slices.Sorted(maps.Keys(g.Derived)) {
		_, _ = fmt.Fprintf(table, "%s\t\t\t\t\t\t\t%.4f\n", key, g.Derived[key])
	}
	_ = table.Flush()
}

// Print writes the run's metrics: statuses, every metric over all cases, the
// key metrics by size bucket and by agent, and the chains.
func (m RunMetrics) Print(w io.Writer) {
	_, _ = fmt.Fprintf(w, "run %s, generator %s: %d case(s)\n", m.Run, m.Generator, len(m.Cases))
	_, _ = fmt.Fprintf(w, "status: %s\n", countsText(m.All.Statuses))
	if len(m.All.Reasons) > 0 {
		_, _ = fmt.Fprintf(w, "failures by reason: %s\n", countsText(m.All.Reasons))
	}
	if MeterFor(m.Generator) == nil {
		_, _ = fmt.Fprintf(w, "the generator %q has no metrics of its own: only the runner's are shown\n", m.Generator)
	}
	for _, c := range m.Cases {
		if c.Problem != "" {
			_, _ = fmt.Fprintf(w, "%s: %s\n", c.CaseID, c.Problem)
		}
	}
	_, _ = fmt.Fprintln(w, "\nall cases")
	m.All.printStats(w)
	for _, part := range []struct {
		title  string
		groups map[string]Group
	}{{"size", m.BySize}, {"agent", m.ByAgent}} {
		if len(part.groups) == 0 {
			continue
		}
		_, _ = fmt.Fprintf(w, "\nby %s\n", part.title)
		table := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
		_, _ = fmt.Fprintln(table, strings.ToUpper(part.title)+"\tCASES\tSTATUS\tWALL MEDIAN\tWALL P90\tCALLS MEDIAN\tCOST MEDIAN\tCOST P90\tCOST SUM")
		for _, key := range sortedGroupKeys(part.groups) {
			g := part.groups[key]
			_, _ = fmt.Fprintf(table, "%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", key, g.Cases, countsText(g.Statuses),
				number(g.Stats[MetricWall].Median), number(g.Stats[MetricWall].P90), number(g.Stats[MetricCalls].Median),
				money(g.Stats[MetricCost], g.Stats[MetricCost].Median), money(g.Stats[MetricCost], g.Stats[MetricCost].P90), money(g.Stats[MetricCost], g.Stats[MetricCost].Sum))
		}
		_ = table.Flush()
	}
	if len(m.Chains) > 0 {
		_, _ = fmt.Fprintf(w, "\nchains: %d pull request(s), %d pair(s) measured\n", len(m.Chains), m.Pairs.Cases)
		for _, c := range m.Chains {
			note := ""
			if c.Stopped != "" {
				note = " (ended: " + c.Stopped + ")"
			}
			_, _ = fmt.Fprintf(w, "  %s: %d revision(s)%s\n", c.PullRequest, len(c.Cases), note)
			for _, p := range c.Pairs {
				if p.Skipped != "" {
					_, _ = fmt.Fprintf(w, "    %s -> %s: not measured: %s\n", p.From, p.To, p.Skipped)
				}
			}
		}
		if m.Pairs.Cases > 0 {
			m.Pairs.printStats(w)
		}
	}
}

func money(s Stat, v float64) string {
	if s.N == 0 {
		return "-"
	}
	return fmt.Sprintf("$%.2f", v)
}

func sortedGroupKeys(groups map[string]Group) []string {
	order := map[string]int{"xs": 0, "s": 1, "m": 2, "l": 3, "xl": 4}
	keys := slices.Collect(maps.Keys(groups))
	slices.SortFunc(keys, func(a, b string) int {
		oa, okA := order[a]
		ob, okB := order[b]
		switch {
		case okA && okB:
			return oa - ob
		case okA:
			return -1
		case okB:
			return 1
		}
		return strings.Compare(a, b)
	})
	return keys
}

// WriteMetrics writes metrics.json into the run directory.
func WriteMetrics(dir string, m RunMetrics) error {
	return writeJSON(filepath.Join(dir, "metrics.json"), m)
}

// Delta is how one metric moved between two runs over the cases both have.
type Delta struct {
	N int `json:"n"`
	// Base and Other are the metric's sums over the aligned cases.
	Base  float64 `json:"base"`
	Other float64 `json:"other"`
	// Changed counts the cases whose value differs.
	Changed int `json:"changed"`
	// The distribution of the per-case difference (this run minus the other).
	Mean   float64 `json:"mean"`
	Median float64 `json:"median"`
	P10    float64 `json:"p10"`
	P90    float64 `json:"p90"`
	// MedianRatio is the median of this run's value over the other's, for cases
	// where the other's is above zero; zero when there is none.
	MedianRatio float64 `json:"median_ratio,omitempty"`
}

// StatusChange is a case whose status differs between the runs.
type StatusChange struct {
	CaseID string `json:"case_id"`
	Base   string `json:"base"`
	Other  string `json:"other"`
}

// Comparison is the alignment of two runs by case id.
type Comparison struct {
	Run   string `json:"run"`
	Other string `json:"other"`
	// Aligned counts the cases both runs have.
	Aligned   int      `json:"aligned"`
	OnlyRun   []string `json:"only_run,omitempty"`
	OnlyOther []string `json:"only_other,omitempty"`
	// StatusChanges lists aligned cases whose status differs.
	StatusChanges []StatusChange `json:"status_changes,omitempty"`
	// Metrics has a Delta per metric, for the cases both runs completed.
	Metrics map[string]Delta `json:"metrics"`
	// SameDocumentMetrics counts, among the cases both completed, those whose
	// generator metrics (every metric but the runner's: cost, time, calls...)
	// are all equal in both runs: the analysis the metrics see is unchanged.
	SameDocumentMetrics int `json:"same_document_metrics"`
	Completed           int `json:"completed"`
}

// Compare aligns two runs by case id. Deltas are this run minus other, over
// the cases both completed (status ok).
func Compare(run, other RunMetrics) Comparison {
	c := Comparison{Run: run.Run, Other: other.Run, Metrics: map[string]Delta{}}
	theirs := map[string]CaseMetrics{}
	for _, o := range other.Cases {
		theirs[o.CaseID] = o
	}
	deltas, ratios := map[string][]float64{}, map[string][]float64{}
	sums := map[string][2]float64{}
	changed := map[string]int{}
	for _, mine := range run.Cases {
		o, ok := theirs[mine.CaseID]
		if !ok {
			c.OnlyRun = append(c.OnlyRun, mine.CaseID)
			continue
		}
		delete(theirs, mine.CaseID)
		c.Aligned++
		if mine.Status != o.Status {
			c.StatusChanges = append(c.StatusChanges, StatusChange{CaseID: mine.CaseID, Base: mine.Status, Other: o.Status})
		}
		if mine.Status != "ok" || o.Status != "ok" {
			continue
		}
		c.Completed++
		same := true
		for key, v := range mine.Values {
			w, ok := o.Values[key]
			if !ok {
				continue
			}
			deltas[key] = append(deltas[key], v-w)
			if w > 0 {
				ratios[key] = append(ratios[key], v/w)
			}
			s := sums[key]
			sums[key] = [2]float64{s[0] + v, s[1] + w}
			if v != w {
				changed[key]++
				if !slices.Contains(runMetrics, key) {
					same = false
				}
			}
		}
		for key := range o.Values {
			if _, ok := mine.Values[key]; !ok && !slices.Contains(runMetrics, key) {
				same = false
			}
		}
		if same {
			c.SameDocumentMetrics++
		}
	}
	c.OnlyOther = slices.Sorted(maps.Keys(theirs))
	for key, values := range deltas {
		sorted := slices.Sorted(slices.Values(values))
		d := Delta{N: len(sorted), Base: sums[key][0], Other: sums[key][1], Changed: changed[key], Median: quantile(sorted, 0.5), P10: quantile(sorted, 0.1), P90: quantile(sorted, 0.9)}
		d.Mean = (d.Base - d.Other) / float64(d.N)
		if r := ratios[key]; len(r) > 0 {
			d.MedianRatio = quantile(slices.Sorted(slices.Values(r)), 0.5)
		}
		c.Metrics[key] = d
	}
	return c
}

// Print writes the comparison.
func (c Comparison) Print(w io.Writer) {
	_, _ = fmt.Fprintf(w, "%s against %s (deltas are %s minus %s)\n", c.Run, c.Other, c.Run, c.Other)
	_, _ = fmt.Fprintf(w, "%d case(s) in both runs, %d completed in both; %d only in %s, %d only in %s\n", c.Aligned, c.Completed, len(c.OnlyRun), c.Run, len(c.OnlyOther), c.Other)
	for _, s := range c.StatusChanges {
		_, _ = fmt.Fprintf(w, "status changed: %s: %s -> %s\n", s.CaseID, s.Other, s.Base)
	}
	_, _ = fmt.Fprintf(w, "document metrics identical in %d of %d completed case(s)\n", c.SameDocumentMetrics, c.Completed)
	stats := map[string]Stat{}
	for key := range c.Metrics {
		stats[key] = Stat{}
	}
	table := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "METRIC\tN\tCHANGED\tTHIS SUM\tOTHER SUM\tMEAN\tMEDIAN\tP10\tP90\tMEDIAN RATIO")
	for _, key := range orderedKeys(stats) {
		d := c.Metrics[key]
		ratio := "-"
		if d.MedianRatio != 0 {
			ratio = fmt.Sprintf("%.2f", d.MedianRatio)
		}
		_, _ = fmt.Fprintf(table, "%s\t%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", key, d.N, d.Changed, number(d.Base), number(d.Other), signed(d.Mean), signed(d.Median), signed(d.P10), signed(d.P90), ratio)
	}
	_ = table.Flush()
}

func signed(v float64) string {
	if v > 0 {
		return "+" + number(v)
	}
	return number(v)
}

// WriteComparison writes compare.json into the run directory.
func WriteComparison(dir string, c Comparison) error {
	return writeJSON(filepath.Join(dir, "compare.json"), c)
}
