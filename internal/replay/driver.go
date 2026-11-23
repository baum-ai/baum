package replay

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/baum-ai/baum/internal/generate"
)

// Commit is the tool's git commit, set at build time:
//
//	-ldflags "-X github.com/baum-ai/baum/internal/replay.Commit=$(git rev-parse HEAD)"
//
// A binary built without it reads the commit Go recorded, then the git
// repository of the working directory.
var Commit string

// Plan is a replay to execute.
type Plan struct {
	Dataset   *Dataset
	OutDir    string
	Name      string
	Selection Selection
	Settings  Settings
	// Concurrency is how many cases run at once; below one is one.
	Concurrency int
	// MaxCostUSD stops starting cases once the priced cost so far, resumed
	// cases' included, reaches it, or once a case that made calls has no cost
	// (an unpriced run cannot be guarded); zero is no guard. Cases already
	// running finish, so with concurrency N the total can pass it by up to N-1
	// cases beyond the one that crossed it.
	MaxCostUSD float64
	// MaxCases refuses a run of more cases than this; zero is no guard.
	MaxCases int
	// Chain replays the revisions of each selected pull request in order, each
	// reading the previous revision's generated document as its previous
	// version (see Dataset.SelectChains); a revision that fails ends the chain.
	Chain bool
	// DetectorReport writes, into the run directory's detectors folder, the case
	// ids by kind of detector evidence for the cases passing the index filters.
	DetectorReport bool
	// Resume skips cases whose outcome in the run directory succeeded.
	Resume, DryRun bool
	// ResumeAnyway continues a run whose saved settings differ from these.
	ResumeAnyway bool
	// Out receives the table and progress lines.
	Out io.Writer
	// NewRunner defaults to the package's.
	NewRunner func(context.Context, Settings) (*Runner, error)
}

// Manifest records what a run was asked to do. It holds no dataset content:
// only case ids and the index file's hash.
type Manifest struct {
	Run                string    `json:"run"`
	CreatedAt          time.Time `json:"created_at"`
	ToolCommit         string    `json:"tool_commit"`
	ToolDirty          bool      `json:"tool_dirty,omitempty"`
	DatasetIndexSHA256 string    `json:"dataset_index_sha256,omitempty"`
	DatasetCases       int       `json:"dataset_cases"`
	Selection          Selection `json:"selection"`
	CaseIDs            []string  `json:"case_ids"`

	Backend            string         `json:"backend"`
	BackendVersion     string         `json:"backend_version"`
	Model              string         `json:"model"`
	Effort             string         `json:"effort"`
	ClaudeConfigDir    string         `json:"claude_config_dir,omitempty"`
	CodexHome          string         `json:"codex_home,omitempty"`
	AgentEnvNames      []string       `json:"agent_env_names,omitempty"`
	TimeLimitSeconds   float64        `json:"time_limit_seconds"`
	PassTimeoutSeconds float64        `json:"pass_timeout_seconds"`
	MaxCalls           int            `json:"max_calls"`
	MaxTokens          int            `json:"max_tokens"`
	Caps               map[string]int `json:"caps"`
	Concurrency        int            `json:"concurrency"`
	MaxCostUSD         float64        `json:"max_cost_usd"`
	PriceVersion       string         `json:"price_version,omitempty"`
	// Generator and GeneratorSettings are the generator's name and its
	// description (Generator.Describe): staged, reuse, prompt version and
	// digest, prompt override hashes for the review generator.
	Generator         string         `json:"generator"`
	GeneratorSettings map[string]any `json:"generator_settings"`
	// Chain says the run replayed pull requests' revisions in order.
	Chain bool `json:"chain,omitempty"`
	// CaseFacts are the size, split and session agents of each case, for
	// grouping a run's metrics without the dataset.
	CaseFacts map[string]CaseFacts `json:"case_facts,omitempty"`
}

// CaseFacts are the index facts a run's metrics group cases by.
type CaseFacts struct {
	Size     string   `json:"size,omitempty"`
	Split    string   `json:"split,omitempty"`
	Sessions []string `json:"sessions,omitempty"`
}

// comparable is what a resume must find unchanged.
func (m Manifest) comparable() map[string]any {
	return map[string]any{"backend": m.Backend, "model": m.Model, "effort": m.Effort, "generator": m.Generator, "generator_settings": m.GeneratorSettings, "chain": m.Chain}
}

// differences names the settings of saved that differ from m's.
func (m Manifest) differences(saved Manifest) []string {
	normalise := func(v map[string]any) map[string]any {
		encoded, _ := json.Marshal(v)
		var out map[string]any
		_ = json.Unmarshal(encoded, &out)
		return out
	}
	now, then := normalise(m.comparable()), normalise(saved.comparable())
	var diff []string
	for _, key := range slices.Sorted(maps.Keys(now)) {
		a, _ := json.Marshal(now[key])
		b, _ := json.Marshal(then[key])
		if !bytes.Equal(a, b) {
			diff = append(diff, fmt.Sprintf("%s (saved %s, now %s)", key, b, a))
		}
	}
	return diff
}

// Row is one case in the summary.
type Row struct {
	CaseID      string   `json:"case_id"`
	Status      string   `json:"status"` // ok, failed, interrupted, skipped, resumed
	Reason      string   `json:"reason,omitempty"`
	WallSeconds float64  `json:"wall_seconds"`
	Calls       int      `json:"calls"`
	Tokens      int      `json:"tokens"`
	CostUSD     *float64 `json:"cost_usd"`
	Repairs     int      `json:"repairs"`
	Notes       []string `json:"notes,omitempty"`
}

// Totals add up a run's rows.
type Totals struct {
	Cases, OK, Failed, Skipped, Resumed int
	Calls, Tokens                       int
	CostUSD                             float64
	// Unpriced counts cases that ran without a price (no usage reported or an
	// unlisted model); the cost total leaves them out.
	Unpriced    int     `json:"unpriced"`
	WallSeconds float64 `json:"wall_seconds"`
	Repairs     int     `json:"repairs"`
}

// Summary is summary.json.
type Summary struct {
	Run              string         `json:"run"`
	Rows             []Row          `json:"cases"`
	Totals           Totals         `json:"totals"`
	FailuresByReason map[string]int `json:"failures_by_reason"`
	// Stopped says why the run did not start every case ("" when it did).
	Stopped string `json:"stopped,omitempty"`
}

// RunDir is where the plan writes.
func (p Plan) RunDir() string { return filepath.Join(p.OutDir, p.Name) }

// unit is what one slot runs: a case, or the chain of a pull request. The
// first done cases already succeeded in the run being resumed.
type unit struct {
	chain *Chain
	ids   []string
	done  int
}

// Execute selects the cases, runs them and writes the manifest, each case's
// files and the summary. Cases that fail do not make it return an error: the
// caller reads the summary.
func Execute(ctx context.Context, p Plan) (Summary, error) {
	if p.Out == nil {
		p.Out = io.Discard
	}
	// A generator that holds resources releases them when the run ends.
	if closer, ok := p.Settings.Generator.(io.Closer); ok {
		defer func() { _ = closer.Close() }()
	}
	if p.Name == "" {
		p.Name = time.Now().UTC().Format("20060102T150405Z")
	}
	if strings.ContainsAny(p.Name, `/\`) || p.Name == "." || p.Name == ".." {
		return Summary{}, errors.New("the run name must be a plain directory name")
	}
	var ids []string
	var units []unit
	if p.DetectorReport {
		if len(p.Selection.IDs) > 0 {
			return Summary{}, errors.New("--detector-report reads the index filters; it does not take --case or --case-file")
		}
		report, err := p.Dataset.DetectorReport(p.Selection)
		if err != nil {
			return Summary{}, err
		}
		if err := WriteDetectorReport(filepath.Join(p.RunDir(), "detectors"), report); err != nil {
			return Summary{}, err
		}
		_, _ = fmt.Fprintf(p.Out, "wrote the detector report (case ids per kind of evidence) to %s\n", filepath.Join(p.RunDir(), "detectors"))
	}
	if p.Chain {
		if g := p.Settings.Generator; g != nil {
			if _, ok := g.(Chainer); !ok {
				return Summary{}, fmt.Errorf("--chain needs a generator that can carry a document into the next revision; %s cannot", g.Name())
			}
		}
		chains, err := p.Dataset.SelectChains(p.Selection)
		if err != nil {
			return Summary{}, err
		}
		for i := range chains {
			ids = append(ids, chains[i].IDs...)
			units = append(units, unit{chain: &chains[i], ids: chains[i].IDs})
		}
	} else {
		var err error
		if ids, err = p.Dataset.Select(p.Selection); err != nil {
			return Summary{}, err
		}
		for _, id := range ids {
			units = append(units, unit{ids: []string{id}})
		}
	}
	if len(ids) == 0 {
		return Summary{}, errors.New("no case matches the selection")
	}
	dir := p.RunDir()
	var rows []Row
	if p.Resume {
		// A case that succeeded is skipped. In a chain only the leading ones
		// are: a revision after one that runs again reads a new previous version.
		var todo []unit
		for _, u := range units {
			for u.done < len(u.ids) {
				out, err := ReadOutcome(filepath.Join(dir, "cases", u.ids[u.done]))
				if err != nil || !out.OK() {
					break
				}
				rows = append(rows, rowOf(out, "resumed"))
				u.done++
				if u.chain == nil {
					break
				}
			}
			if u.done < len(u.ids) {
				todo = append(todo, u)
			}
		}
		units = todo
	}
	remaining := 0
	for _, u := range units {
		remaining += len(u.ids) - u.done
	}
	if p.MaxCases > 0 && remaining > p.MaxCases {
		if p.Chain {
			return Summary{}, fmt.Errorf("the selection is %d cases in %d chains, above --max-cases %d; a chain runs whole, so lower --limit (it counts cases, and keeps whole chains) or raise the guard", remaining, len(units), p.MaxCases)
		}
		return Summary{}, fmt.Errorf("the selection is %d cases, above --max-cases %d; narrow it with --limit or raise the guard", remaining, p.MaxCases)
	}
	if p.DryRun {
		_, _ = fmt.Fprintf(p.Out, "dry run: %d case(s) would run, %d already done, nothing was called\n", remaining, len(rows))
		for _, u := range units {
			if u.chain != nil {
				_, _ = fmt.Fprintf(p.Out, "chain %s: %d revision(s), in this order\n", u.chain.Name(), len(u.ids))
			}
			for _, id := range u.ids[u.done:] {
				if _, err := p.Dataset.LoadCase(ctx, id); err != nil {
					_, _ = fmt.Fprintf(p.Out, "%s\t(would fail to load: %v)\n", id, err)
					continue
				}
				_, _ = fmt.Fprintln(p.Out, id)
			}
		}
		if p.Chain {
			if eligible, err := p.Dataset.SelectChains(Selection{Split: p.Selection.Split, Representative: p.Selection.Representative, Sizes: p.Selection.Sizes,
				Sessions: p.Selection.Sessions, Images: p.Selection.Images, Threads: p.Selection.Threads, Previous: p.Selection.Previous, Detector: p.Selection.Detector, IDs: p.Selection.IDs}); err == nil {
				revisions := 0
				for _, c := range eligible {
					revisions += len(c.IDs)
				}
				_, _ = fmt.Fprintf(p.Out, "%d chain(s) with %d revision(s) are eligible before --limit; %d chain(s), %d case(s) are selected\n", len(eligible), revisions, len(units), remaining+len(rows))
			}
			for _, u := range units {
				for _, w := range u.chain.Warnings {
					_, _ = fmt.Fprintf(p.Out, "warning: chain %s: %s\n", u.chain.Name(), w)
				}
			}
		} else if len(p.Selection.IDs) == 0 {
			if b, err := p.Dataset.Break(p.Selection); err == nil {
				b.Print(p.Out)
			}
		}
		return Summary{Run: p.Name}, nil
	}
	newRunner := p.NewRunner
	if newRunner == nil {
		newRunner = NewRunner
	}
	runner, err := newRunner(ctx, p.Settings)
	if err != nil {
		return Summary{}, err
	}
	if _, ok := runner.generator.(Chainer); p.Chain && !ok {
		return Summary{}, fmt.Errorf("--chain needs a generator that can carry a document into the next revision; %s cannot", runner.generator.Name())
	}
	manifest := p.manifest(runner, ids)
	manifestPath := filepath.Join(dir, "manifest.json")
	var saved Manifest
	if raw, err := os.ReadFile(manifestPath); p.Resume && err == nil && json.Unmarshal(raw, &saved) == nil {
		if diff := manifest.differences(saved); len(diff) > 0 && !p.ResumeAnyway {
			return Summary{}, fmt.Errorf("the run %s was started with other settings: %s; use the same settings, or --resume-anyway", p.Name, strings.Join(diff, "; "))
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "cases"), 0o700); err != nil {
		return Summary{}, err
	}
	// A resumed run keeps the manifest of the run it continues.
	if _, err := os.Stat(manifestPath); !p.Resume || err != nil {
		if err := writeJSON(manifestPath, manifest); err != nil {
			return Summary{}, err
		}
	}

	var (
		mu      sync.Mutex
		spent   float64
		stopped string
		wg      sync.WaitGroup
	)
	for _, row := range rows {
		if row.CostUSD != nil {
			spent += *row.CostUSD
		} else if p.MaxCostUSD > 0 && row.Calls > 0 && stopped == "" {
			stopped = fmt.Sprintf("cost guard: resumed case %s made %d call(s) but has no cost (usage not reported or the model is not priced), so --max-cost-usd cannot be enforced", row.CaseID, row.Calls)
		}
	}
	slots := make(chan struct{}, max(p.Concurrency, 1))
	add := func(row Row) {
		mu.Lock()
		defer mu.Unlock()
		rows = append(rows, row)
		if row.CostUSD != nil {
			spent += *row.CostUSD
		} else if p.MaxCostUSD > 0 && row.Calls > 0 && stopped == "" {
			stopped = fmt.Sprintf("cost guard: case %s made %d call(s) but has no cost (usage not reported or the model is not priced), so --max-cost-usd cannot be enforced", row.CaseID, row.Calls)
		}
		_, _ = fmt.Fprintf(p.Out, "%-11s %s  %.0fs  %d call(s)  %s\n", row.Status, row.CaseID, row.WallSeconds, row.Calls, costText(row.CostUSD))
	}
	// halted says whether no further case may start: the run was interrupted
	// or it reached its cost guard.
	halted := func() bool {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case ctx.Err() != nil:
			if stopped == "" || !strings.HasPrefix(stopped, "interrupted") {
				stopped = "interrupted"
			}
		case p.MaxCostUSD > 0 && spent >= p.MaxCostUSD && stopped == "":
			stopped = fmt.Sprintf("cost guard: %.2f USD spent, --max-cost-usd %.2f", spent, p.MaxCostUSD)
		}
		return stopped != ""
	}
	for _, u := range units {
		slots <- struct{}{}
		if halted() {
			<-slots
			mu.Lock()
			for _, id := range u.ids[u.done:] {
				rows = append(rows, Row{CaseID: id, Status: "skipped", Reason: "stopped"})
			}
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			if u.chain == nil {
				row, _ := p.runCase(ctx, runner, u.ids[0], nil)
				add(row)
				return
			}
			p.runChain(ctx, runner, u, halted, add)
		}()
	}
	wg.Wait()
	if ctx.Err() != nil && stopped == "" {
		stopped = "interrupted"
	}
	summary := summarize(p.Name, rows, stopped)
	if err := writeJSON(filepath.Join(dir, "summary.json"), summary); err != nil {
		return summary, err
	}
	summary.Print(p.Out)
	return summary, nil
}

// runCase loads and generates one case; a case that cannot be loaded or
// written is a failed row, so one broken case does not end the run. prepare,
// when set, changes the loaded case before it runs (a chain gives it its
// previous version). A case that succeeded also returns its document.
func (p Plan) runCase(ctx context.Context, runner *Runner, id string, prepare func(Case) (Case, error)) (Row, []byte) {
	caseDir := filepath.Join(p.RunDir(), "cases", id)
	fail := func(reason string, err error) (Row, []byte) {
		out := Outcome{CaseID: id, Status: "failed", Reason: reason, Error: err.Error(), StartedAt: time.Now().UTC()}
		_ = os.MkdirAll(caseDir, 0o700)
		_ = writeJSON(filepath.Join(caseDir, "outcome.json"), out)
		return rowOf(out, "failed"), nil
	}
	c, err := p.Dataset.LoadCase(ctx, id)
	if err != nil {
		return fail("case", err)
	}
	// A rerun starts the case's directory over: nothing of an earlier run stays.
	_ = os.RemoveAll(caseDir)
	if prepare != nil {
		if c, err = prepare(c); err != nil {
			return fail(cmp.Or(string(generate.CategoryOf(err)), "case"), err)
		}
	}
	if err := os.MkdirAll(caseDir, 0o700); err != nil {
		return fail("internal", err)
	}
	run := *runner
	run.Settings.OutputDir = caseDir
	out, err := run.Run(ctx, c)
	if err != nil {
		// Keep the outcome's usage and cost: the generation may have spent
		// before writing its files failed, and the cost guard must count it.
		out.CaseID = id
		out.Status, out.Reason, out.Error = "failed", "internal", err.Error()
		out.Document, out.DocumentValid = nil, false
		_ = os.MkdirAll(caseDir, 0o700)
		_ = writeJSON(filepath.Join(caseDir, "outcome.json"), out)
		return rowOf(out, "failed"), nil
	}
	if out.Status == "interrupted" {
		_ = os.Remove(caseDir)
	}
	if out.OK() {
		return rowOf(out, out.Status), out.Document
	}
	return rowOf(out, out.Status), nil
}

func rowOf(o Outcome, status string) Row {
	return Row{CaseID: o.CaseID, Status: status, Reason: o.Reason, WallSeconds: o.WallSeconds, Calls: o.Calls, Tokens: o.Tokens, CostUSD: o.CostUSD,
		Repairs: o.Repairs, Notes: o.Notes}
}

func summarize(name string, rows []Row, stopped string) Summary {
	slices.SortFunc(rows, func(a, b Row) int { return cmp.Compare(a.CaseID, b.CaseID) })
	s := Summary{Run: name, Rows: rows, FailuresByReason: map[string]int{}, Stopped: stopped}
	for _, r := range rows {
		s.Totals.Cases++
		switch r.Status {
		case "ok":
			s.Totals.OK++
		case "resumed":
			s.Totals.Resumed++
			continue // earlier cost and time belong to the earlier run
		case "skipped":
			s.Totals.Skipped++
			s.FailuresByReason["skipped"]++
			continue
		default:
			s.Totals.Failed++
			s.FailuresByReason[cmp.Or(r.Reason, "unknown")]++
		}
		s.Totals.Calls += r.Calls
		s.Totals.Tokens += r.Tokens
		s.Totals.Repairs += r.Repairs
		s.Totals.WallSeconds += r.WallSeconds
		if r.CostUSD != nil {
			s.Totals.CostUSD += *r.CostUSD
		} else if r.Calls > 0 {
			s.Totals.Unpriced++
		}
	}
	return s
}

func costText(c *float64) string {
	if c == nil {
		return "-"
	}
	return fmt.Sprintf("$%.2f", *c)
}

// Print writes the per-case table, the totals and the failures by reason.
func (s Summary) Print(w io.Writer) {
	table := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "CASE\tSTATUS\tTIME\tCALLS\tTOKENS\tCOST\tREPAIRS\tNOTES")
	for _, r := range s.Rows {
		status := r.Status
		if r.Reason != "" && r.Status != "resumed" {
			status += " (" + r.Reason + ")"
		}
		_, _ = fmt.Fprintf(table, "%s\t%s\t%.0fs\t%d\t%d\t%s\t%d\t%s\n", r.CaseID, status, r.WallSeconds, r.Calls, r.Tokens, costText(r.CostUSD), r.Repairs, strings.Join(r.Notes, ","))
	}
	t := s.Totals
	_, _ = fmt.Fprintf(table, "TOTAL\t%d ok, %d failed, %d skipped, %d resumed\t%.0fs\t%d\t%d\t$%.2f\t%d\t\n", t.OK, t.Failed, t.Skipped, t.Resumed, t.WallSeconds, t.Calls, t.Tokens, t.CostUSD, t.Repairs)
	_ = table.Flush()
	if t.Unpriced > 0 {
		_, _ = fmt.Fprintf(w, "%d case(s) have no cost: usage was not reported or the model is not priced\n", t.Unpriced)
	}
	if len(s.FailuresByReason) > 0 {
		reasons := make([]string, 0, len(s.FailuresByReason))
		for reason, n := range s.FailuresByReason {
			reasons = append(reasons, fmt.Sprintf("%s: %d", reason, n))
		}
		slices.Sort(reasons)
		_, _ = fmt.Fprintf(w, "failures by reason: %s\n", strings.Join(reasons, ", "))
	}
	if s.Stopped != "" {
		_, _ = fmt.Fprintf(w, "stopped: %s\n", s.Stopped)
	}
}

func (p Plan) manifest(r *Runner, ids []string) Manifest {
	s := r.Settings
	commit, dirty := toolCommit()
	m := Manifest{Run: p.Name, CreatedAt: time.Now().UTC(), ToolCommit: commit, ToolDirty: dirty,
		DatasetIndexSHA256: p.Dataset.IndexSHA256, DatasetCases: len(p.Dataset.Index), Selection: p.Selection, CaseIDs: ids,
		Backend: r.Selected.Backend.Name(), BackendVersion: r.Selected.Version, Model: r.Selected.Model, Effort: s.Effort,
		ClaudeConfigDir: s.ClaudeConfigDir, CodexHome: s.CodexHome, AgentEnvNames: slices.Sorted(maps.Keys(s.AgentEnv)),
		TimeLimitSeconds: s.TimeLimit.Seconds(), PassTimeoutSeconds: s.PassTimeout.Seconds(), MaxCalls: s.MaxCalls, MaxTokens: s.MaxTokens,
		Caps: r.Caps, Concurrency: max(p.Concurrency, 1), MaxCostUSD: p.MaxCostUSD, PriceVersion: r.prices.Version(),
		Generator: r.generator.Name(), GeneratorSettings: r.Described, Chain: p.Chain}
	for _, id := range ids {
		for _, e := range p.Dataset.Index {
			if e.ID == id {
				if m.CaseFacts == nil {
					m.CaseFacts = map[string]CaseFacts{}
				}
				m.CaseFacts[id] = CaseFacts{Size: e.Size, Split: e.Split, Sessions: e.SessionVendors}
			}
		}
	}
	return m
}

func toolCommit() (commit string, dirty bool) {
	if Commit != "" {
		return Commit, false
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				commit = setting.Value
			case "vcs.modified":
				dirty = setting.Value == "true"
			}
		}
		if commit != "" {
			return commit, dirty
		}
	}
	if out, err := exec.Command("git", "rev-parse", "HEAD").Output(); err == nil {
		commit = strings.TrimSpace(string(out))
		status, _ := exec.Command("git", "status", "--porcelain").Output()
		return commit, len(status) > 0
	}
	return "unknown", false
}

func writeJSON(path string, v any) error {
	encoded, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return generate.WriteFileAtomic(path, append(encoded, '\n'))
}
