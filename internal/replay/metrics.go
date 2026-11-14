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
	"sync"
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

// ArtifactMeter is implemented by a Meter that also measures the artifacts a
// generator wrote beside a case's document (Generated.Artifacts): read
// returns one by name.
type ArtifactMeter interface {
	ArtifactMetrics(read func(name string) ([]byte, error)) (map[string]float64, error)
}

// Definer is implemented by a Meter that says what each of its metrics
// means; the report prints the definitions of the metrics it shows.
type Definer interface {
	Definitions() map[string]string
}

var (
	metersMu sync.Mutex
	meters   = map[string]Meter{}
)

// RegisterMeter makes a generator's metrics available to `replay metrics` for
// runs whose manifest names it. Call it at initialisation. It panics when the
// name is taken.
func RegisterMeter(generator string, m Meter) {
	metersMu.Lock()
	defer metersMu.Unlock()
	if _, ok := meters[generator]; ok {
		panic("replay: metrics of generator " + generator + " registered twice")
	}
	meters[generator] = m
}

// UnregisterMeter removes a generator's metrics, for tests that register their
// own.
func UnregisterMeter(generator string) {
	metersMu.Lock()
	defer metersMu.Unlock()
	delete(meters, generator)
}

// MeterFor returns the metrics of a generator, or nil when it has none.
func MeterFor(generator string) Meter {
	metersMu.Lock()
	defer metersMu.Unlock()
	return meters[generator]
}

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
	Agent string `json:"agent,omitempty"`
	// Values are the case's metrics; a metric that is undefined for the case (a
	// cost nobody priced, a ratio over nothing) is absent, not zero.
	Values map[string]float64 `json:"values,omitempty"`
	// Problem says why the generator's metrics could not be read.
	Problem string `json:"problem,omitempty"`
}

// Group aggregates a set of cases (or of chain pairs).
type Group struct {
	// Cases counts every case of the group, whatever its status.
	Cases    int            `json:"cases"`
	Statuses map[string]int `json:"statuses,omitempty"`
	Reasons  map[string]int `json:"reasons,omitempty"`
	// Stats are over the cases that succeeded: a failed case has no document and
	// its time and cost stop where it failed. Failed has the runner's metrics of
	// the cases that ran and did not succeed, apart.
	Stats   map[string]Stat    `json:"stats"`
	Failed  map[string]Stat    `json:"failed,omitempty"`
	Derived map[string]float64 `json:"derived,omitempty"`
}

// PairRecord is the metrics of one revision against the one before it.
type PairRecord struct {
	From   string             `json:"from"`
	To     string             `json:"to"`
	Values map[string]float64 `json:"values,omitempty"`
	// First marks the pair of a chain's first two revisions: the earlier one
	// was generated from a previous version other code wrote.
	First bool `json:"first,omitempty"`
	// Skipped says why the pair was not measured.
	Skipped string `json:"skipped,omitempty"`
}

// ChainReport is the measurements of one pull request's chain.
type ChainReport struct {
	PullRequest string       `json:"pull_request"`
	Cases       []string     `json:"cases"`
	Stopped     string       `json:"stopped,omitempty"`
	Warnings    []string     `json:"warnings,omitempty"`
	Pairs       []PairRecord `json:"pairs"`
	// Totals are the sums of the measured pairs' metrics and Derived the
	// generator's figures over them: the chain's own, for comparing pull requests.
	Totals  map[string]float64 `json:"totals,omitempty"`
	Derived map[string]float64 `json:"derived,omitempty"`
}

// RunInfo is what a run was asked to do, as far as comparing two runs needs.
type RunInfo struct {
	DatasetIndexSHA256 string `json:"dataset_index_sha256,omitempty"`
	Chain              bool   `json:"chain,omitempty"`
	Selection          string `json:"selection,omitempty"`
	Generator          string `json:"generator"`
	GeneratorSettings  string `json:"generator_settings,omitempty"`
	Backend            string `json:"backend,omitempty"`
	Model              string `json:"model,omitempty"`
	Effort             string `json:"effort,omitempty"`
}

// RunMetrics is metrics.json.
type RunMetrics struct {
	Run       string           `json:"run"`
	Generator string           `json:"generator"`
	Info      RunInfo          `json:"info"`
	Cases     []CaseMetrics    `json:"cases"`
	All       Group            `json:"all"`
	BySize    map[string]Group `json:"by_size,omitempty"`
	ByAgent   map[string]Group `json:"by_agent,omitempty"`
	Chains    []ChainReport    `json:"chains,omitempty"`
	// FirstPairs aggregates the first pair of each chain, LaterPairs the rest.
	FirstPairs *Group `json:"first_pairs,omitempty"`
	LaterPairs *Group `json:"later_pairs,omitempty"`
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

func infoOf(m Manifest) RunInfo {
	selection, _ := json.Marshal(m.Selection)
	settings, _ := json.Marshal(m.GeneratorSettings)
	return RunInfo{DatasetIndexSHA256: m.DatasetIndexSHA256, Chain: m.Chain, Selection: string(selection), Generator: m.Generator,
		GeneratorSettings: string(settings), Backend: m.Backend, Model: m.Model, Effort: m.Effort}
}

// ReadManifest reads a run directory's manifest.json.
func ReadManifest(dir string) (Manifest, error) {
	var manifest Manifest
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return manifest, fmt.Errorf("%s is not a replay run: %w", dir, err)
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return manifest, fmt.Errorf("%s: manifest.json: %w", dir, err)
	}
	return manifest, nil
}

// ReadMetrics reads a finished run directory and measures it. It calls no model.
func ReadMetrics(dir string) (RunMetrics, error) {
	manifest, err := ReadManifest(dir)
	if err != nil {
		return RunMetrics{}, err
	}
	meter := MeterFor(manifest.Generator)
	m := RunMetrics{Run: manifest.Run, Generator: manifest.Generator, Info: infoOf(manifest)}
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
	if meter != nil && out.OK() {
		if values, err := meter.DocumentMetrics(document); err != nil {
			c.Problem = err.Error()
		} else {
			maps.Copy(c.Values, values)
		}
		if am, ok := meter.(ArtifactMeter); ok && c.Problem == "" {
			read := func(name string) ([]byte, error) {
				if !filepath.IsLocal(name) {
					return nil, fmt.Errorf("artifact %q is not below the case directory", name)
				}
				return os.ReadFile(filepath.Join(dir, name))
			}
			if values, err := am.ArtifactMetrics(read); err != nil {
				c.Problem = err.Error()
			} else {
				maps.Copy(c.Values, values)
			}
		}
	}
	return c
}

func group(cases []CaseMetrics, meter Meter) Group {
	g := Group{Cases: len(cases), Statuses: map[string]int{}, Reasons: map[string]int{}}
	ok, failed := map[string][]float64{}, map[string][]float64{}
	for _, c := range cases {
		g.Statuses[c.Status]++
		if c.Status != "ok" && c.Reason != "" {
			g.Reasons[c.Reason]++
		}
		for key, v := range c.Values {
			switch {
			case c.Status == "ok":
				ok[key] = append(ok[key], v)
			case slices.Contains(runMetrics, key):
				failed[key] = append(failed[key], v)
			}
		}
	}
	g.Stats = statsOf(ok)
	if len(failed) > 0 {
		g.Failed = statsOf(failed)
	}
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

func sumsOf(stats map[string]Stat) map[string]float64 {
	sums := map[string]float64{}
	for key, s := range stats {
		sums[key] = s.Sum
	}
	return sums
}

func derive(stats map[string]Stat, meter Meter) map[string]float64 {
	if deriver, ok := meter.(Deriver); ok {
		return deriver.Derived(sumsOf(stats))
	}
	return nil
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
	first, later := map[string][]float64{}, map[string][]float64{}
	var measuredFirst, measuredLater int
	for _, entry := range entries {
		var record ChainRecord
		raw, err := os.ReadFile(filepath.Join(dir, "chains", entry.Name(), "chain.json"))
		if err != nil || json.Unmarshal(raw, &record) != nil {
			continue
		}
		report := ChainReport{PullRequest: record.PullRequest, Stopped: record.Stopped, Warnings: record.Warnings}
		perChain := map[string][]float64{}
		var previous []byte
		for i, rev := range record.Revisions {
			report.Cases = append(report.Cases, rev.CaseID)
			document, _ := os.ReadFile(filepath.Join(dir, "cases", rev.CaseID, "document.json"))
			if i > 0 {
				pair := PairRecord{From: record.Revisions[i-1].CaseID, To: rev.CaseID, First: i == 1}
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
				if pair.Skipped == "" {
					target := later
					if pair.First {
						target = first
						measuredFirst++
					} else {
						measuredLater++
					}
					for key, v := range pair.Values {
						target[key] = append(target[key], v)
						perChain[key] = append(perChain[key], v)
					}
				}
				report.Pairs = append(report.Pairs, pair)
			}
			previous = document
		}
		if len(perChain) > 0 {
			stats := statsOf(perChain)
			report.Totals = sumsOf(stats)
			if deriver, ok := meter.(Deriver); ok {
				report.Derived = deriver.Derived(report.Totals)
			}
		}
		m.Chains = append(m.Chains, report)
	}
	slices.SortFunc(m.Chains, func(a, b ChainReport) int { return strings.Compare(a.PullRequest, b.PullRequest) })
	if len(m.Chains) > 0 {
		pairs := func(n int, values map[string][]float64) *Group {
			g := Group{Cases: n, Stats: statsOf(values)}
			g.Derived = derive(g.Stats, meter)
			return &g
		}
		m.FirstPairs, m.LaterPairs = pairs(measuredFirst, first), pairs(measuredLater, later)
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

// smallN is the sample size below which a statistic is marked: with fewer
// values a median and a p90 are examples, not estimates.
const smallN = 5

func nText(n int) string {
	if n < smallN {
		return fmt.Sprintf("%d*", n)
	}
	return fmt.Sprint(n)
}

func (g Group) printStats(w io.Writer, title string) {
	_, _ = fmt.Fprintln(w, title)
	table := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "METRIC\tN\tMEDIAN\tP90\tMEAN\tMIN\tMAX\tSUM")
	small := false
	for _, key := range orderedKeys(g.Stats) {
		s := g.Stats[key]
		small = small || s.N < smallN
		_, _ = fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", key, nText(s.N), number(s.Median), number(s.P90), number(s.Mean), number(s.Min), number(s.Max), number(s.Sum))
	}
	for _, key := range slices.Sorted(maps.Keys(g.Derived)) {
		_, _ = fmt.Fprintf(table, "%s\t\t\t\t\t\t\t%.4f\n", key, g.Derived[key])
	}
	_ = table.Flush()
	if small {
		_, _ = fmt.Fprintf(w, "* N below %d: read these as examples, not estimates\n", smallN)
	}
}

// Print writes the run's metrics: statuses, every metric over the cases that
// succeeded, the key metrics by size bucket and by agent, the cases that
// failed apart, and the chains.
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
	_, _ = fmt.Fprintln(w)
	m.All.printStats(w, fmt.Sprintf("cases that succeeded (%d of %d)", m.All.Statuses["ok"], m.All.Cases))
	if len(m.All.Failed) > 0 {
		table := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
		_, _ = fmt.Fprintln(table, "\ncases that ran and did not succeed, apart\nMETRIC\tN\tMEDIAN\tSUM")
		for _, key := range orderedKeys(m.All.Failed) {
			s := m.All.Failed[key]
			_, _ = fmt.Fprintf(table, "%s\t%d\t%s\t%s\n", key, s.N, number(s.Median), number(s.Sum))
		}
		_ = table.Flush()
	}
	for _, part := range []struct {
		title  string
		groups map[string]Group
	}{{"size", m.BySize}, {"agent", m.ByAgent}} {
		if len(part.groups) == 0 {
			continue
		}
		_, _ = fmt.Fprintf(w, "\nby %s, over the cases that succeeded\n", part.title)
		table := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
		_, _ = fmt.Fprintln(table, strings.ToUpper(part.title)+"\tCASES\tSTATUS\tOK\tWALL MEDIAN\tWALL P90\tCALLS MEDIAN\tCOST N\tCOST MEDIAN\tCOST P90\tCOST SUM")
		for _, key := range sortedGroupKeys(part.groups) {
			g := part.groups[key]
			cost := g.Stats[MetricCost]
			_, _ = fmt.Fprintf(table, "%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", key, g.Cases, countsText(g.Statuses), nText(g.Statuses["ok"]),
				number(g.Stats[MetricWall].Median), number(g.Stats[MetricWall].P90), number(g.Stats[MetricCalls].Median),
				nText(cost.N), money(cost, cost.Median), money(cost, cost.P90), money(cost, cost.Sum))
		}
		_ = table.Flush()
	}
	if len(m.Chains) > 0 {
		_, _ = fmt.Fprintf(w, "\nchains: %d pull request(s)\n", len(m.Chains))
		for _, c := range m.Chains {
			note := ""
			if c.Stopped != "" {
				note = " (ended: " + c.Stopped + ")"
			}
			_, _ = fmt.Fprintf(w, "  %s: %d revision(s)%s\n", c.PullRequest, len(c.Cases), note)
			for _, warning := range c.Warnings {
				_, _ = fmt.Fprintf(w, "    order guessed: %s\n", warning)
			}
			for _, p := range c.Pairs {
				if p.Skipped != "" {
					_, _ = fmt.Fprintf(w, "    %s -> %s: not measured: %s\n", p.From, p.To, p.Skipped)
				}
			}
			for _, key := range slices.Sorted(maps.Keys(c.Derived)) {
				_, _ = fmt.Fprintf(w, "    %s %.4f\n", key, c.Derived[key])
			}
		}
		if m.FirstPairs.Cases > 0 {
			_, _ = fmt.Fprintln(w)
			m.FirstPairs.printStats(w, fmt.Sprintf("first pair of each chain (%d), which starts from a previous version other code wrote", m.FirstPairs.Cases))
		}
		if m.LaterPairs.Cases > 0 {
			_, _ = fmt.Fprintln(w)
			m.LaterPairs.printStats(w, fmt.Sprintf("later pairs (%d), whose earlier document this generator wrote from its own previous one", m.LaterPairs.Cases))
		}
	}
	if definer, ok := MeterFor(m.Generator).(Definer); ok {
		shown := map[string]bool{}
		for _, g := range []*Group{&m.All, m.FirstPairs, m.LaterPairs} {
			if g != nil {
				for key := range g.Stats {
					shown[key] = true
				}
				for key := range g.Derived {
					shown[key] = true
				}
			}
		}
		definitions := definer.Definitions()
		_, _ = fmt.Fprintln(w, "\ndefinitions")
		for _, key := range slices.Sorted(maps.Keys(definitions)) {
			if shown[key] {
				_, _ = fmt.Fprintf(w, "  %s: %s\n", key, definitions[key])
			}
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

// Delta is how one metric moved between a run and a baseline over the cases both
// completed.
type Delta struct {
	// N counts the cases that have the metric in both runs; Missing the cases
	// that have it in only one.
	N       int `json:"n"`
	Missing int `json:"missing,omitempty"`
	// Run and Baseline are the metric's sums over those N cases.
	Run      float64 `json:"run"`
	Baseline float64 `json:"baseline"`
	// Changed counts the cases whose value differs.
	Changed int `json:"changed"`
	// The distribution of the per-case difference (the run minus the baseline).
	Mean   float64 `json:"mean"`
	Median float64 `json:"median"`
	P10    float64 `json:"p10"`
	P90    float64 `json:"p90"`
	// MedianRatio is the median of the run's value over the baseline's, for
	// cases where the baseline's is above zero; zero when there is none.
	MedianRatio float64 `json:"median_ratio,omitempty"`
}

// StatusChange is a case whose status differs between the runs.
type StatusChange struct {
	CaseID   string `json:"case_id"`
	Run      string `json:"run"`
	Baseline string `json:"baseline"`
}

// equality is the rule "identical" uses, stated in the comparison.
const equality = "a case is identical when both runs completed it, its generator metrics decoded in both, and every metric that is not the runner's (cost, time, calls, tokens, repairs, size) is present in both with exactly equal values"

// Comparison is the alignment of a run with a baseline by case id.
type Comparison struct {
	Run      string `json:"run"`
	Baseline string `json:"baseline"`
	// Warnings say where the runs differ in more than the settings under test:
	// the deltas then mix effects.
	Warnings []string `json:"warnings,omitempty"`
	// Aligned counts the cases both runs have.
	Aligned      int      `json:"aligned"`
	OnlyRun      []string `json:"only_run,omitempty"`
	OnlyBaseline []string `json:"only_baseline,omitempty"`
	// StatusChanges lists aligned cases whose status differs.
	StatusChanges []StatusChange `json:"status_changes,omitempty"`
	// Metrics has a Delta per metric, over the cases both runs completed.
	Metrics map[string]Delta `json:"metrics"`
	// Identical counts, among the Completed cases, those equal by the rule in
	// Equality; Undecoded counts those left out of that count because the
	// generator's metrics could not be read in one of the runs.
	Identical int    `json:"identical"`
	Completed int    `json:"completed"`
	Undecoded int    `json:"undecoded,omitempty"`
	Equality  string `json:"equality"`
}

// Compare aligns a run with a baseline by case id. Deltas are the run minus the
// baseline, over the cases both completed (status ok), for every metric either
// run has.
func Compare(run, baseline RunMetrics) Comparison {
	c := Comparison{Run: run.Run, Baseline: baseline.Run, Metrics: map[string]Delta{}, Equality: equality}
	for _, w := range []struct {
		name      string
		a, b      string
		unlessSet bool
	}{
		{"dataset (index hash)", run.Info.DatasetIndexSHA256, baseline.Info.DatasetIndexSHA256, false},
		{"generator", run.Info.Generator, baseline.Info.Generator, false},
		{"model", run.Info.Model, baseline.Info.Model, false},
		{"selection", run.Info.Selection, baseline.Info.Selection, false},
	} {
		if w.a != w.b {
			c.Warnings = append(c.Warnings, fmt.Sprintf("the runs differ in %s", w.name))
		}
	}
	if run.Info.Chain != baseline.Info.Chain {
		c.Warnings = append(c.Warnings, "one run is a chain run and the other is not")
	}
	theirs := map[string]CaseMetrics{}
	for _, o := range baseline.Cases {
		theirs[o.CaseID] = o
	}
	deltas, ratios, missing := map[string][]float64{}, map[string][]float64{}, map[string]int{}
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
			c.StatusChanges = append(c.StatusChanges, StatusChange{CaseID: mine.CaseID, Run: mine.Status, Baseline: o.Status})
		}
		if mine.Status != "ok" || o.Status != "ok" {
			continue
		}
		c.Completed++
		same := true
		for key := range maps.Keys(unionKeys(mine.Values, o.Values)) {
			v, inRun := mine.Values[key]
			w, inBase := o.Values[key]
			if !inRun || !inBase {
				missing[key]++
				if !slices.Contains(runMetrics, key) {
					same = false
				}
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
		switch {
		case mine.Problem != "" || o.Problem != "":
			c.Undecoded++
		case same:
			c.Identical++
		}
	}
	c.OnlyBaseline = slices.Sorted(maps.Keys(theirs))
	for key := range unionKeys(deltas, missing) {
		values := deltas[key]
		sorted := slices.Sorted(slices.Values(values))
		d := Delta{N: len(sorted), Missing: missing[key], Run: sums[key][0], Baseline: sums[key][1], Changed: changed[key]}
		if len(sorted) > 0 {
			d.Median, d.P10, d.P90 = quantile(sorted, 0.5), quantile(sorted, 0.1), quantile(sorted, 0.9)
			d.Mean = (d.Run - d.Baseline) / float64(d.N)
		}
		if r := ratios[key]; len(r) > 0 {
			d.MedianRatio = quantile(slices.Sorted(slices.Values(r)), 0.5)
		}
		c.Metrics[key] = d
	}
	return c
}

func unionKeys[A, B any](a map[string]A, b map[string]B) map[string]struct{} {
	out := map[string]struct{}{}
	for k := range a {
		out[k] = struct{}{}
	}
	for k := range b {
		out[k] = struct{}{}
	}
	return out
}

// Print writes the comparison.
func (c Comparison) Print(w io.Writer) {
	_, _ = fmt.Fprintf(w, "%s against the baseline %s (deltas are %s minus the baseline)\n", c.Run, c.Baseline, c.Run)
	for _, warning := range c.Warnings {
		_, _ = fmt.Fprintf(w, "warning: %s\n", warning)
	}
	_, _ = fmt.Fprintf(w, "%d case(s) in both runs, %d completed in both; %d only in the run, %d only in the baseline\n", c.Aligned, c.Completed, len(c.OnlyRun), len(c.OnlyBaseline))
	for _, s := range c.StatusChanges {
		_, _ = fmt.Fprintf(w, "status changed: %s: %s (baseline) -> %s\n", s.CaseID, s.Baseline, s.Run)
	}
	_, _ = fmt.Fprintf(w, "identical document metrics in %d of %d completed case(s)", c.Identical, c.Completed)
	if c.Undecoded > 0 {
		_, _ = fmt.Fprintf(w, "; %d left out because their metrics did not decode", c.Undecoded)
	}
	_, _ = fmt.Fprintf(w, "\n(%s)\n", c.Equality)
	stats := map[string]Stat{}
	for key := range c.Metrics {
		stats[key] = Stat{}
	}
	table := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "METRIC\tN\tMISSING\tCHANGED\tRUN SUM\tBASELINE SUM\tMEAN\tMEDIAN\tP10\tP90\tMEDIAN RATIO")
	for _, key := range orderedKeys(stats) {
		d := c.Metrics[key]
		ratio := "-"
		if d.MedianRatio != 0 {
			ratio = fmt.Sprintf("%.2f", d.MedianRatio)
		}
		_, _ = fmt.Fprintf(table, "%s\t%s\t%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", key, nText(d.N), d.Missing, d.Changed, number(d.Run), number(d.Baseline), signed(d.Mean), signed(d.Median), signed(d.P10), signed(d.P90), ratio)
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
