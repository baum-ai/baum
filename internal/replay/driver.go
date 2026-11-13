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
	"math/rand/v2"
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

// Selection picks cases. Without IDs it filters the dataset's index; the
// result is sorted by id, shuffled when ShuffleSeed is set, then cut to Limit.
type Selection struct {
	IDs            []string
	Split          string
	Representative bool
	Primary        bool
	// Sizes are the size buckets to keep (xs, s, m, l, xl); empty keeps all.
	Sizes []string
	// Sessions is "claude_code", "codex" or "none"; empty keeps all.
	Sessions    string
	Limit       int
	ShuffleSeed *uint64
}

// Select returns the ids of the cases the selection names.
func (d *Dataset) Select(sel Selection) ([]string, error) {
	var ids []string
	if len(sel.IDs) > 0 {
		ids = slices.Clone(sel.IDs)
		if sel.Split != "" || sel.Representative || sel.Primary || len(sel.Sizes) > 0 || sel.Sessions != "" {
			return nil, errors.New("--case cannot be combined with index filters")
		}
	} else {
		if len(d.Index) == 0 {
			return nil, errors.New("the dataset has no index/cases.jsonl: name cases with --case")
		}
		switch sel.Split {
		case "", "calibration", "held_out":
		default:
			return nil, fmt.Errorf("unknown split %q; use calibration or held_out", sel.Split)
		}
		switch sel.Sessions {
		case "", "claude_code", "codex", "none":
		default:
			return nil, fmt.Errorf("unknown --with-sessions %q; use claude_code, codex or none", sel.Sessions)
		}
		for _, e := range d.Index {
			switch {
			case sel.Split != "" && e.Split != sel.Split,
				sel.Representative && !e.Representative,
				sel.Primary && !e.Primary,
				len(sel.Sizes) > 0 && !slices.Contains(sel.Sizes, e.Size),
				sel.Sessions == "none" && len(e.SessionVendors) > 0,
				sel.Sessions != "" && sel.Sessions != "none" && !slices.Contains(e.SessionVendors, sel.Sessions):
				continue
			}
			ids = append(ids, e.ID)
		}
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if sel.ShuffleSeed != nil {
		rng := rand.New(rand.NewPCG(*sel.ShuffleSeed, 0))
		rng.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
	}
	if sel.Limit > 0 && len(ids) > sel.Limit {
		ids = ids[:sel.Limit]
	}
	return ids, nil
}

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
}

// comparable is what a resume must find unchanged.
func (m Manifest) comparable() map[string]any {
	return map[string]any{"backend": m.Backend, "model": m.Model, "effort": m.Effort, "generator": m.Generator, "generator_settings": m.GeneratorSettings}
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

// Execute selects the cases, runs them and writes the manifest, each case's
// files and the summary. Cases that fail do not make it return an error: the
// caller reads the summary.
func Execute(ctx context.Context, p Plan) (Summary, error) {
	if p.Out == nil {
		p.Out = io.Discard
	}
	if p.Name == "" {
		p.Name = time.Now().UTC().Format("20060102T150405Z")
	}
	if strings.ContainsAny(p.Name, `/\`) || p.Name == "." || p.Name == ".." {
		return Summary{}, errors.New("the run name must be a plain directory name")
	}
	ids, err := p.Dataset.Select(p.Selection)
	if err != nil {
		return Summary{}, err
	}
	if len(ids) == 0 {
		return Summary{}, errors.New("no case matches the selection")
	}
	dir := p.RunDir()
	todo := ids
	var rows []Row
	if p.Resume {
		todo = nil
		for _, id := range ids {
			if out, err := ReadOutcome(filepath.Join(dir, "cases", id)); err == nil && out.OK() {
				rows = append(rows, rowOf(out, "resumed"))
				continue
			}
			todo = append(todo, id)
		}
	}
	if p.MaxCases > 0 && len(todo) > p.MaxCases {
		return Summary{}, fmt.Errorf("the selection is %d cases, above --max-cases %d; narrow it with --limit or raise the guard", len(todo), p.MaxCases)
	}
	if p.DryRun {
		_, _ = fmt.Fprintf(p.Out, "dry run: %d case(s) would run, %d already done, nothing was called\n", len(todo), len(rows))
		for _, id := range todo {
			if _, err := p.Dataset.LoadCase(ctx, id); err != nil {
				_, _ = fmt.Fprintf(p.Out, "%s\t(would fail to load: %v)\n", id, err)
				continue
			}
			_, _ = fmt.Fprintln(p.Out, id)
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
	for _, id := range todo {
		slots <- struct{}{}
		mu.Lock()
		switch {
		case ctx.Err() != nil:
			if stopped == "" || !strings.HasPrefix(stopped, "interrupted") {
				stopped = "interrupted"
			}
		case p.MaxCostUSD > 0 && spent >= p.MaxCostUSD && stopped == "":
			stopped = fmt.Sprintf("cost guard: %.2f USD spent, --max-cost-usd %.2f", spent, p.MaxCostUSD)
		}
		halt := stopped != ""
		mu.Unlock()
		if halt {
			<-slots
			mu.Lock()
			rows = append(rows, Row{CaseID: id, Status: "skipped", Reason: "stopped"})
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			add(p.runCase(ctx, runner, id))
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
// written is a failed row, so one broken case does not end the run.
func (p Plan) runCase(ctx context.Context, runner *Runner, id string) Row {
	caseDir := filepath.Join(p.RunDir(), "cases", id)
	fail := func(reason string, err error) Row {
		out := Outcome{CaseID: id, Status: "failed", Reason: reason, Error: err.Error(), StartedAt: time.Now().UTC()}
		_ = os.MkdirAll(caseDir, 0o700)
		_ = writeJSON(filepath.Join(caseDir, "outcome.json"), out)
		return rowOf(out, "failed")
	}
	c, err := p.Dataset.LoadCase(ctx, id)
	if err != nil {
		return fail("case", err)
	}
	if err := os.MkdirAll(caseDir, 0o700); err != nil {
		return fail("internal", err)
	}
	// A rerun replaces the case's earlier files.
	for _, name := range caseFiles {
		_ = os.Remove(filepath.Join(caseDir, name))
	}
	run := *runner
	run.Settings.OutputDir = caseDir
	out, err := run.Run(ctx, c)
	if err != nil {
		return fail("internal", err)
	}
	if out.Status == "interrupted" {
		_ = os.Remove(caseDir)
	}
	return rowOf(out, out.Status)
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
		Generator: r.generator.Name(), GeneratorSettings: r.Described}
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
