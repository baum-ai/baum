package decisionlog

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/features/adaptive"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/sessions"
)

const (
	ReasonBuildNoProgress = "no_progress"
	ReasonBuildIncomplete = "selected_work_incomplete"
	adaptiveExploreSteps  = 12
	// adaptiveCoverageAsk is how many unselected catalogued pull requests make
	// a first finish ask whether the rest was meant to be left out.
	adaptiveCoverageAsk = 40
)

type adaptiveRun struct {
	*featureRun
	state   *adaptiveState
	source  repositorySource
	options BuildOptions
	spent   buildSpend
	started time.Time
	// notes say what of the last evidence request the host could not serve
	// and why, for the agent: such a request is served as far as it can be
	// rather than failed, since repeating it on a resume could never succeed.
	notes []string
}

func (w *FeatureWorker) buildAdaptive(ctx context.Context, repository, dir string, o BuildOptions) (report BuildReport, err error) {
	report.Repository = repository
	if workflow, e := w.Store.FeatureBuildWorkflow(ctx, repository); e != nil {
		return report, e
	} else if workflow == "legacy" {
		report.Stopped = "workflow_mismatch"
		return report, errWorkflowMismatch
	}
	r, err := w.newRun(ctx, repository, dir, FeatureOptions{Manual: !o.Gated, DryRun: o.DryRun, Backend: o.Backend, Model: o.Model, building: true, adaptive: true})
	if err != nil {
		return report, err
	}
	report.Version = r.version
	if r.version == 0 && o.DryRun {
		return report, nil
	}
	a, err := w.Store.adaptiveState(ctx, r.version)
	if err != nil {
		return report, err
	}
	if a == nil {
		var old int
		if err := w.Store.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM feature WHERE version_id=?1)+(SELECT count(*) FROM feature_settle WHERE version_id=?1)+(SELECT count(*) FROM feature_seed WHERE version_id=?1)`, r.version).Scan(&old); err != nil {
			return report, err
		}
		if old > 0 {
			return BuildReport{Repository: repository, Version: r.version, Stopped: "workflow_mismatch"}, errWorkflowMismatch
		}
		a = newAdaptiveState()
	}
	if o.DryRun {
		report.Coverage = a.coverage()
		report.Stopped = FeatureDryRun
		return report, nil
	}
	// Each run has its own no-progress allowance: a resume after a stop for no
	// progress is not stopped again by the first step that settles nothing.
	a.NoProgress = 0
	b := &adaptiveRun{featureRun: r, state: a, options: o, started: time.Now()}
	b.spent.version = r.version
	defer func() {
		report.Coverage = b.state.coverage()
		report.Calls = b.spent.calls
		report.SpentUSD = b.spent.run
		report.Placed = len(b.state.Assignments)
		report.PullRequests = len(b.state.Selected)
		// A rejected checkpoint may have mutated the working maps. Final API/job
		// progress must describe SQLite's accepted checkpoint, not the proposal.
		if err != nil {
			report.Coverage = nil
			report.Placed = 0
			report.PullRequests = 0
			if durable, readErr := w.Store.adaptiveState(context.WithoutCancel(ctx), r.version); readErr == nil && durable != nil {
				report.Coverage = durable.coverage()
				report.Placed = len(durable.Assignments)
				report.PullRequests = len(durable.Selected)
			}
		}
		_ = w.Store.db.QueryRowContext(context.WithoutCancel(ctx), `SELECT spent_usd,unpriced_calls FROM feature_version WHERE id=?`, r.version).Scan(&report.VersionSpentUSD, &report.UnpricedCalls)
		if err != nil {
			report.Stopped = "failed"
			if ctx.Err() != nil {
				report.Stopped = "interrupted"
			}
			if err.Error() == ReasonBuildNoProgress {
				report.Stopped = ReasonBuildNoProgress
			}
			if _, ok := errors.AsType[*adaptiveWaitError](err); ok {
				report.Stopped = "rate_limited"
			}
			if errors.Is(err, errWorkflowMismatch) {
				report.Stopped = "workflow_mismatch"
			}
			if errors.Is(err, errVersionRetired) {
				report.Stopped = ReasonVersionRetired
			}
			if errors.Is(err, errBuildConcurrent) {
				report.Stopped = "concurrent_build"
			}
			if !errors.Is(err, errBuildConcurrent) && !errors.Is(err, errVersionRetired) {
				_, _ = w.Store.db.ExecContext(context.WithoutCancel(ctx), `UPDATE feature_version SET stopped=? WHERE id=? AND state=?`, report.Stopped, r.version, VersionAdaptiveBuilding)
			}
		}
	}()
	if on, e := r.refresh(ctx); e != nil {
		return report, e
	} else if !on {
		report.Stopped = ReasonDisabled
		return report, nil
	}
	// Initialization is checkpointed before source preparation. A failed fetch
	// never loses a successfully catalogued window.
	if a.Sequence == 0 {
		if err = w.Store.saveAdaptive(ctx, r.version, repository, a, false, w.now()); err != nil {
			return report, err
		}
	}
	if !a.RetryAt.IsZero() && w.now().Before(a.RetryAt) {
		err = &adaptiveWaitError{At: a.RetryAt}
		return report, err
	}
	if len(a.Windows) == 0 && a.Pending == nil {
		since := w.now().AddDate(0, -1, 0).UTC().Format(time.DateOnly)
		request := adaptive.Reply{Action: "catalog", Since: since, Reason: "Initial recent metadata and open work"}
		if err = b.request(ctx, request); err != nil {
			return report, err
		}
	}
	if a.Pending != nil {
		pending := *a.Pending
		if err = b.request(ctx, pending); err != nil {
			return report, err
		}
	}
	if b.source, err = b.prepareSource(ctx); err != nil {
		return report, err
	}
	defer b.source.close()
	if a.Source.Revision == "" {
		a.Source = b.source.Context
		if err = w.Store.saveAdaptive(ctx, r.version, repository, a, false, w.now()); err != nil {
			return report, err
		}
	}
	for {
		if err = ctx.Err(); err != nil {
			return report, err
		}
		if on, e := r.refresh(ctx); e != nil {
			return report, e
		} else if !on {
			report.Stopped = ReasonDisabled
			return report, nil
		}
		reply, e := b.step(ctx)
		progress := false
		if rejected, ok := errors.AsType[*adaptiveRejection](e); ok {
			// A reply the validator refused after its repairs saved nothing.
			// The agent is told why and takes another step from the last
			// accepted checkpoint; repeated refusals stop as no progress.
			a.Feedback = "The host refused the last reply after its repairs and saved nothing of it: " + rejected.Findings + " Reply with a valid step; a smaller one is fine."
		} else if e != nil {
			err = e
			return report, err
		} else if progress, e = b.apply(ctx, reply); e != nil {
			err = e
			return report, err
		} else if a.Complete {
			report.Current = true
			_ = w.Store.db.QueryRowContext(ctx, `SELECT count(*) FROM feature WHERE version_id=?`, r.version).Scan(&report.Features)
			return report, nil
		}
		if !progress {
			a.NoProgress++
		} else {
			a.NoProgress = 0
		}
		a.Step++
		if a.Step >= adaptiveExploreSteps || time.Since(b.started) > 5*time.Minute {
			a.ExplorationClosed = true
		}
		if a.NoProgress >= 3 {
			a.Feedback = "Repeated requests or checkpoints made no progress; resume to reconsider the remaining work."
			_ = w.Store.saveAdaptive(context.WithoutCancel(ctx), r.version, repository, a, false, w.now())
			err = errors.New(ReasonBuildNoProgress)
			return report, err
		}
		if err = w.Store.saveAdaptive(ctx, r.version, repository, a, false, w.now()); err != nil {
			return report, err
		}
	}
}

func (b *adaptiveRun) prepareSource(ctx context.Context) (repositorySource, error) {
	if b.state.Source.Revision != "" {
		return prepareRepositorySource(ctx, b.dir, b.repository, b.state.Source.Revision, b.state.Source.Branch, b.prefs.Resolve(b.w.Config))
	}
	clone, remote, err := openClone(ctx, b.dir, b.repository)
	if err != nil {
		return repositorySource{}, err
	}
	defer func() { _ = clone.Close() }()
	var branch string
	for _, n := range adaptiveScopeNumbers(b.state) {
		if name := b.state.Catalog[n].DefaultBranch; name != "" {
			branch = name
			break
		}
	}
	var revision string
	if branch != "" {
		revision, err = clone.FetchBranch(ctx, remote, branch)
	} else {
		branch, err = clone.CurrentBranch(ctx)
		if err == nil {
			revision, err = clone.HeadCommit(ctx)
		}
	}
	if err != nil {
		return repositorySource{}, err
	}
	return prepareRepositorySource(ctx, b.dir, b.repository, revision, branch, b.prefs.Resolve(b.w.Config))
}

// adaptiveCatalogRow is one line of catalog.jsonl. Scope is the scope the pull
// request is assigned to, "" once it is skipped and absent until it is settled,
// so unsettled work is found by searching rather than by comparing maps.
type adaptiveCatalogRow struct {
	Number   int     `json:"number"`
	State    string  `json:"state"`
	Opened   string  `json:"opened"`
	Merged   string  `json:"merged,omitempty"`
	Selected bool    `json:"selected,omitempty"`
	Scope    *string `json:"scope,omitempty"`
	Title    string  `json:"title"`
}

// adaptiveCatalogFile names the catalog beside state.json. It is one compact
// line per pull request: a month of a busy repository is hundreds of rows, too
// many to read as indented JSON inside the state.
const adaptiveCatalogFile = "catalog.jsonl"

func (b *adaptiveRun) catalogLines() ([]byte, map[string]int, error) {
	a := b.state
	var out bytes.Buffer
	weeks := map[string]int{}
	for _, n := range adaptiveScopeNumbers(a) {
		p := a.Catalog[n]
		row := adaptiveCatalogRow{Number: n, Title: p.Title, State: p.State, Opened: p.CreatedAt.UTC().Format(time.DateOnly), Selected: a.Selected[n]}
		if p.MergedAt != nil {
			row.State, row.Merged = "merged", p.MergedAt.UTC().Format(time.DateOnly)
		}
		if assignment, ok := a.Assignments[n]; ok {
			row.Scope = &assignment.Scope
		}
		line, err := json.Marshal(row)
		if err != nil {
			return nil, nil, err
		}
		out.Write(line)
		out.WriteByte('\n')
		y, w := p.UpdatedAt.ISOWeek()
		weeks[fmt.Sprintf("%d-W%02d", y, w)]++
	}
	return out.Bytes(), weeks, nil
}

func (b *adaptiveRun) taskInput(weeks map[string]int, details, code, decisions map[int]string) any {
	a := b.state
	return struct {
		Decisions         map[int]string              `json:"decisions"`
		Coverage          *BuildCoverage              `json:"coverage"`
		Catalog           string                      `json:"catalog"`
		Weeks             map[string]int              `json:"counts_by_week"`
		Details           map[int]string              `json:"details"`
		Code              map[int]string              `json:"code"`
		Scopes            map[string]adaptive.Scope   `json:"scopes"`
		Assignments       map[int]adaptive.Assignment `json:"assignments"`
		ExplorationClosed bool                        `json:"exploration_closed"`
		Feedback          string                      `json:"feedback"`
	}{decisions, a.coverage(), adaptiveCatalogFile, weeks, details, code, a.Scopes, a.Assignments, a.ExplorationClosed, a.Feedback}
}

func (b *adaptiveRun) step(ctx context.Context) (adaptive.Reply, error) {
	details, code, decisions, err := b.evidenceFiles(ctx)
	if err != nil {
		return adaptive.Reply{}, err
	}
	catalog, weeks, err := b.catalogLines()
	if err != nil {
		return adaptive.Reply{}, err
	}
	data, err := json.MarshalIndent(b.taskInput(weeks, details, code, decisions), "", "  ")
	if err != nil {
		return adaptive.Reply{}, err
	}
	// Redact external PR text before an agent can read it, as for extraction.
	for name, input := range map[string][]byte{"state.json": data, adaptiveCatalogFile: catalog} {
		masked, err := agent.RedactTaskOutput(ctx, b.w.redactor, input, &agent.Outcome{})
		if err != nil {
			return adaptive.Reply{}, err
		}
		if err = writeAdaptiveInput(b.source.Directory, name, masked); err != nil {
			return adaptive.Reply{}, err
		}
	}
	p, reason := b.calls.plan(ctx, b.prefs, b.options.Backend, b.options.Model, 1, FeatureConsolidateOutputTokens)
	if reason != "" {
		return adaptive.Reply{}, fmt.Errorf("feature build backend: %s", reason)
	}
	selected, err := generate.SelectDesktop(ctx, config.Backend(p.Backend), p.Model, p.backends, io.Discard)
	if err != nil {
		return adaptive.Reply{}, err
	}
	attempt, err := b.w.Store.Begin(ctx, Attempt{Repository: b.repository, Backend: p.Backend, Model: p.Model, Stage: StageDiscovery, Item: fmt.Sprintf("adaptive:%d:%d", b.version, b.state.Sequence)})
	if err != nil {
		return adaptive.Reply{}, err
	}
	cfg := b.prefs.Resolve(b.w.Config)
	check := func(data []byte) string {
		reply, err := adaptive.Parse(data)
		if err == nil {
			err = b.validate(reply)
		}
		if err != nil {
			return err.Error()
		}
		return ""
	}
	// There is no outer run deadline. This unit can recover at the last accepted
	// checkpoint if a provider/tool stalls or its generous context limit is hit.
	out, document, runErr := generate.RunTask(ctx, generate.TaskOptions{Backend: selected.Backend, Redactor: b.w.redactor,
		Task: agent.Task{Directory: b.source.Directory, Label: fmt.Sprintf("feature-step-%d", b.state.Sequence), Prompt: sessions.GenerationRunMarker + "\n" + adaptive.Prompt, Schema: adaptive.Schema, Check: check,
			Budgets: agent.Budgets{WallClock: time.Duration(cmp.Or(cfg.Budgets.PassTimeout, config.Default().Budgets.PassTimeout)), MaxCalls: cfg.Budgets.MaxCalls, MaxTokens: cfg.Budgets.MaxTokens, MaxProviderAttempts: 3, MaxRepairAttempts: 2, StallTimeout: 2 * time.Minute, ToolTimeout: 5 * time.Minute}}})
	cost := b.calls.prices.BackendOutcomeCost(p.Backend, cmpModel(out.Model, p.Model), out)
	state, why := AttemptSucceeded, ""
	if runErr != nil {
		state, why = AttemptFailed, "task_failed"
		if out.Reason == agent.ReasonValidationRejected {
			why = "unusable_reply"
		}
		if ctx.Err() != nil {
			state, why = AttemptAbandoned, "cancelled"
		}
	}
	write := context.WithoutCancel(ctx)
	settleErr := b.w.Store.settle(write, attempt, state, why, cost, decisionUsage(out.Usage), out.StructuredOutputRejections+out.ValidationRepairs)
	b.spent.add(write, b.w.Store, b.w.logger(), cost)
	b.notify(BuildDiscovering)
	if runErr != nil && out.Reason == agent.ReasonValidationRejected && ctx.Err() == nil && settleErr == nil {
		findings := []rune(out.Findings)
		return adaptive.Reply{}, &adaptiveRejection{Findings: string(findings[:min(len(findings), 1000)])}
	}
	if runErr != nil {
		return adaptive.Reply{}, errors.Join(runErr, settleErr)
	}
	if settleErr != nil {
		return adaptive.Reply{}, settleErr
	}
	reply, err := adaptive.Parse(document)
	if err == nil {
		err = b.validate(reply)
	}
	return reply, err
}

func (b *adaptiveRun) validate(reply adaptive.Reply) error {
	a := b.state
	selected := map[int]bool{}
	for n := range a.Selected {
		selected[n] = true
	}
	for _, n := range reply.Selected {
		if _, ok := a.Catalog[n]; !ok {
			return fmt.Errorf("selected PR %d is not in the pinned catalog", n)
		}
		selected[n] = true
	}
	scopes := map[string]adaptive.Scope{}
	for k, v := range a.Scopes {
		scopes[k] = v
	}
	for _, scope := range reply.Scopes {
		for _, path := range scope.Paths {
			info, err := os.Lstat(filepath.Join(b.source.Directory, "head", filepath.FromSlash(strings.TrimSuffix(path, "/"))))
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				available := false
				for _, evidence := range a.Code {
					for _, changed := range evidence.Paths {
						if changed == strings.TrimSuffix(path, "/") || strings.HasPrefix(changed, strings.TrimSuffix(path, "/")+"/") {
							available = true
						}
					}
				}
				if !available {
					return fmt.Errorf("scope path %q is not in the pinned source or requested PR code", path)
				}
			}
		}
		for _, n := range scope.Examples {
			if _, ok := a.Catalog[n]; !ok {
				return fmt.Errorf("scope example PR %d is not catalogued", n)
			}
		}
		scopes[scope.Key] = scope
	}
	assignments := map[int]adaptive.Assignment{}
	for n, v := range a.Assignments {
		assignments[n] = v
	}
	if len(scopes) > 300 {
		return errors.New("the source map exceeds its scope bound; reconcile duplicate scopes")
	}
	for _, assignment := range reply.Assignments {
		if _, ok := a.Catalog[assignment.Number]; !ok {
			return fmt.Errorf("assigned PR %d is not in the pinned catalog", assignment.Number)
		}
		// An assignment selects its pull request. In a checkpoint, one that
		// names a scope nobody defined is left out when the reply is applied,
		// and reported: a large batch is not refused whole for it. A finish
		// has no later step to repair it in.
		if assignment.Scope != "" {
			if _, ok := scopes[assignment.Scope]; !ok {
				if reply.Action == "finish" {
					return fmt.Errorf("unknown scope %q", assignment.Scope)
				}
				continue
			}
		}
		selected[assignment.Number] = true
		assignments[assignment.Number] = assignment
	}
	switch reply.Action {
	case "catalog":
		since, err := time.Parse(time.DateOnly, reply.Since)
		if err != nil || since.After(b.w.now()) {
			return errors.New("catalog since must be a past YYYY-MM-DD date")
		}
	case "details", "code", "decisions":
		if len(reply.Numbers) == 0 {
			return errors.New("an evidence request needs PR numbers")
		}
		for _, n := range reply.Numbers {
			if _, ok := a.Catalog[n]; !ok && reply.Action != "details" {
				return fmt.Errorf("PR %d is not catalogued", n)
			}
			if n < 1 {
				return errors.New("PR numbers must be positive")
			}
			if reply.Action != "details" && !selected[n] {
				return fmt.Errorf("evidence request PR %d must be selected first", n)
			}
		}
	case "finish":
		for n := range selected {
			assignment, ok := assignments[n]
			if !ok {
				return fmt.Errorf("selected PR %d remains incomplete", n)
			}
			if assignment.Scope != "" && strings.TrimSpace(scopes[assignment.Scope].Digest) == "" {
				return fmt.Errorf("scope %s needs its final concise digest", assignment.Scope)
			}
		}
	}
	return nil
}

func (b *adaptiveRun) apply(ctx context.Context, reply adaptive.Reply) (bool, error) {
	a := b.state
	before := adaptiveProgress(a)
	for _, n := range reply.Selected {
		a.Selected[n] = true
	}
	for _, scope := range reply.Scopes {
		a.Scopes[scope.Key] = scope
	}
	undefined, unsaved := []string{}, []string{}
	for _, v := range reply.Assignments {
		if _, ok := a.Scopes[v.Scope]; v.Scope != "" && !ok {
			undefined, unsaved = append(undefined, v.Scope), append(unsaved, strconv.Itoa(v.Number))
			continue
		}
		a.Selected[v.Number] = true
		a.Assignments[v.Number] = v
	}
	changed := before != adaptiveProgress(a)
	a.Feedback = reply.Reason
	if len(unsaved) > 0 {
		slices.Sort(undefined)
		a.Feedback += " Host note: the assignments of pull requests " + strings.Join(unsaved, ", ") + " were not saved, because no scope has the key " +
			strings.Join(slices.Compact(undefined), ", ") + ". Define the scope in the same reply, or use an existing key."
	}
	if reply.Action == "finish" {
		// A result that leaves most of a sizeable catalog unselected replaces
		// the whole feature history with a sample of it. The agent is asked
		// once whether that is meant; its changes are kept as a checkpoint and
		// a second finish stands.
		if unselected := len(a.Catalog) - len(a.Selected); !a.CoverageAsked && unselected >= adaptiveCoverageAsk && unselected*2 > len(a.Catalog) {
			a.CoverageAsked = true
			a.Feedback += fmt.Sprintf(" Host note: this finish was saved as a checkpoint, not promoted: %d of the %d catalogued pull requests are unselected, and the result replaces the repository's whole feature history. Lines of catalog.jsonl without \"selected\" are the unselected ones. Select and assign those that belong to substantial outcomes, in large checkpoints, then finish. If every one of them is small unrelated maintenance, finish again and the result stands.", unselected, len(a.Catalog))
			return true, b.w.Store.saveAdaptive(ctx, b.version, b.repository, a, false, b.w.now())
		}
		a.Complete = true
		return true, b.w.Store.saveAdaptive(ctx, b.version, b.repository, a, true, b.w.now())
	}
	if err := b.w.Store.saveAdaptive(ctx, b.version, b.repository, a, false, b.w.now()); err != nil {
		return false, err
	}
	if reply.Action == "checkpoint" {
		b.notify(BuildPlacing)
		return changed, nil
	}
	oldDecisions := len(a.Decisions)
	oldDetails, oldCode, oldCatalog := len(a.Details), len(a.Code), len(a.Catalog)
	oldPatches := 0
	for _, c := range a.Code {
		oldPatches += len(c.Patches)
	}
	if err := b.request(ctx, reply); err != nil {
		return false, err
	}
	patches := 0
	for _, c := range a.Code {
		patches += len(c.Patches)
	}
	if fresh := oldDecisions != len(a.Decisions) || oldDetails != len(a.Details) || oldCode != len(a.Code) || oldCatalog != len(a.Catalog) || patches != oldPatches; fresh || changed {
		return true, nil
	}
	// An agent that asks again for what it has is told so, rather than left
	// to repeat itself until the run stops for no progress.
	a.Feedback = strings.TrimSpace(a.Feedback + " Host note: this request added nothing, because all of it was supplied before: catalog.jsonl holds the window, and the details, code and decisions maps of state.json name the evidence files. Do not ask for it again; settle the selected work with a checkpoint.")
	return false, b.w.Store.saveAdaptive(ctx, b.version, b.repository, a, false, b.w.now())
}

func (b *adaptiveRun) notify(phase string) {
	if b.options.Progress != nil {
		b.options.Progress(BuildProgress{Phase: phase, Placed: len(b.state.Assignments), Total: len(b.state.Selected), Calls: b.spent.calls, SpentUSD: b.spent.run, Unpriced: b.spent.unpriced, Coverage: b.state.coverage()})
	}
}

// request checkpoints the intention first, then a completed read. On failure
// the exact evidence request remains pending for a future resume.
func (b *adaptiveRun) request(ctx context.Context, reply adaptive.Reply) error {
	a := b.state
	b.notes = nil
	a.Pending = &reply
	if err := b.w.Store.saveAdaptive(ctx, b.version, b.repository, a, false, b.w.now()); err != nil {
		return err
	}
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		operation, cancel := context.WithTimeout(ctx, CallTimeout)
		switch reply.Action {
		case "catalog":
			err = b.catalog(operation, reply.Since)
		case "details":
			err = b.details(operation, reply.Numbers)
		case "code":
			err = b.code(operation, reply.Numbers, reply.Paths)
		case "decisions":
			err = b.decisions(operation, reply.Numbers)
		}
		cancel()
		if _, wait := errors.AsType[*adaptiveWaitError](err); err == nil || ctx.Err() != nil || wait {
			break
		}
		if attempt == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
	if err != nil {
		a.Failures++
		if wait, ok := errors.AsType[*adaptiveWaitError](err); ok {
			a.RetryAt = wait.At
		}
		a.Feedback = "Evidence request failed; selected work and accepted checkpoints are retained."
		saveErr := b.w.Store.saveAdaptive(context.WithoutCancel(ctx), b.version, b.repository, a, false, b.w.now())
		return errors.Join(err, saveErr)
	}
	a.Pending = nil
	a.RetryAt = time.Time{}
	if a.Failures > 0 {
		// The failure the feedback reported is over.
		a.Feedback = "The evidence request that failed earlier has now been served."
	}
	a.Failures = 0
	if len(b.notes) > 0 {
		a.Feedback = strings.TrimSpace(a.Feedback + " Host note: " + strings.Join(b.notes, " "))
		b.notes = nil
	}
	return b.w.Store.saveAdaptive(ctx, b.version, b.repository, a, false, b.w.now())
}

func (b *adaptiveRun) catalog(ctx context.Context, sinceText string) error {
	a := b.state
	if a.Windows[sinceText] {
		return nil
	}
	since, err := time.Parse(time.DateOnly, sinceText)
	if err != nil {
		return err
	}
	if b.w.GitHub == nil {
		return errors.New("GitHub catalog reader unavailable")
	}
	repository, err := github.ParseRepository(b.repository)
	if err != nil {
		return err
	}
	reader, err := b.w.GitHub(ctx, repository)
	if err != nil {
		return err
	}
	reader = adaptiveReadGuard{Reader: reader, now: b.w.now()}
	list, err := github.ListPullRequests(ctx, reader, repository, "", since)
	if err != nil {
		return err
	}
	open, err := github.ListOpenPullRequests(ctx, reader, repository)
	if err != nil {
		return err
	}
	rows := append(list.Pulls, open.Pulls...)
	for _, p := range rows {
		if p.Bot || (p.State != "open" && p.MergedAt == nil) {
			continue
		}
		// First observation pins the metadata, even when the live sync advances it.
		if _, seen := a.Catalog[p.Number]; !seen {
			a.Catalog[p.Number] = p
		}
	}
	a.Truncated = a.Truncated || list.Truncated || open.Truncated
	a.Windows[sinceText] = true
	if a.Since == "" || sinceText < a.Since {
		a.Since = sinceText
	}
	return nil
}

func (b *adaptiveRun) code(ctx context.Context, numbers []int, paths []string) error {
	clone, remote, err := openClone(ctx, b.dir, b.repository)
	if err != nil {
		return err
	}
	defer func() { _ = clone.Close() }()
	matched := map[string]bool{}
	defer func() {
		if unmatched := slices.DeleteFunc(slices.Clone(paths), func(path string) bool { return matched[path] }); len(unmatched) > 0 {
			b.notes = append(b.notes, "no requested pull request changes "+strings.Join(unmatched, ", ")+", so no patch was supplied for those paths.")
		}
	}()
	for _, n := range numbers {
		p := b.state.Catalog[n]
		if err := b.w.Store.savePulls(ctx, b.repository, []github.ListedPull{p}, true, func(*sql.Tx) error { return nil }); err != nil {
			return err
		}
		known, ok := b.state.Code[n]
		if !ok {
			if err := clone.FetchPulls(ctx, remote, []gitlocal.PullRequest{{Number: n, HeadSHA: p.HeadSHA, BaseSHA: p.BaseSHA, BaseRef: p.BaseRef}}); err != nil {
				return err
			}
			commits, err := clone.Commits(ctx, p.BaseSHA, p.HeadSHA)
			if err != nil {
				return err
			}
			set := map[string]bool{}
			known = adaptiveCode{Paths: []string{}, Commits: []PullCommit{}, Patches: map[string]string{}}
			for _, c := range commits {
				known.Commits = append(known.Commits, PullCommit{SHA: c.OID, CommittedAt: func() time.Time { at, _ := time.Parse(time.RFC3339, c.CommittedAt); return at }()})
				for _, path := range c.Files {
					set[path] = true
				}
			}
			for path := range set {
				known.Paths = append(known.Paths, path)
			}
			slices.Sort(known.Paths)
		}
		// One request names the paths of all its pull requests together, so
		// each gets the patches of those it changes.
		for _, path := range paths {
			if !slices.Contains(known.Paths, path) {
				continue
			}
			matched[path] = true
			if _, ok := known.Patches[path]; ok {
				continue
			}
			resolved, err := clone.ResolvePull(ctx, gitlocal.PullRequest{Number: n, HeadSHA: p.HeadSHA, BaseSHA: p.BaseSHA, BaseRef: p.BaseRef, Remote: remote})
			if err != nil {
				return err
			}
			patch, err := clone.FilePatch(ctx, resolved.MergeBaseOID, p.HeadSHA, 64<<10, path)
			if err != nil {
				return err
			}
			known.Patches[path] = string(patch)
		}
		b.state.Code[n] = known
		// Do not overwrite newer live heads when committing historical evidence.
		if err := b.w.Store.adaptivePaths(ctx, b.repository, n, p.HeadSHA, known.Paths, known.Commits); err != nil {
			return err
		}
	}
	return nil
}

// Inputs remain read-only to the agent. Replace atomically between work steps
// instead of opening the previous read-only file for writing.
func writeAdaptiveInput(dir, file string, data []byte) error {
	f, err := os.CreateTemp(dir, "input-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(0400); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	target := filepath.Join(dir, file)
	if err := os.Chmod(target, 0600); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(name, target)
}

// adaptiveRejection is a work step whose reply the validator refused after its
// repairs. It is the step's failure, not the build's.
type adaptiveRejection struct{ Findings string }

func (e *adaptiveRejection) Error() string { return "feature build reply refused: " + e.Findings }

// A rate-limit response preserves a recoverable wait instead of immediately
// consuming the second evidence attempt. A later resume observes RetryAt.
type adaptiveWaitError struct{ At time.Time }

func (e *adaptiveWaitError) Error() string {
	return "GitHub rate limited; retry after " + e.At.UTC().Format(time.RFC3339)
}

type adaptiveReadGuard struct {
	github.Reader
	now time.Time
}

func (r adaptiveReadGuard) Read(ctx context.Context, path, etag string) (int, http.Header, []byte, error) {
	status, headers, data, err := r.Reader.Read(ctx, path, etag)
	if err == nil && (status == http.StatusTooManyRequests || (status == http.StatusForbidden && (headers.Get("X-RateLimit-Remaining") == "0" || headers.Get("Retry-After") != ""))) {
		at := r.now.Add(time.Minute)
		if seconds, e := strconv.Atoi(headers.Get("Retry-After")); e == nil && seconds > 0 {
			at = r.now.Add(time.Duration(seconds) * time.Second)
		} else if date, e := http.ParseTime(headers.Get("Retry-After")); e == nil && date.After(r.now) {
			at = date
		} else if seconds, e := strconv.ParseInt(headers.Get("X-RateLimit-Reset"), 10, 64); e == nil && time.Unix(seconds, 0).After(r.now) {
			at = time.Unix(seconds, 0)
		}
		return status, headers, nil, &adaptiveWaitError{At: at}
	}
	return status, headers, data, err
}

// adaptiveMissingGuard reports a number GitHub has no pull request for as
// github.ErrNotFound: an agent may ask for a reference that is an issue.
type adaptiveMissingGuard struct{ github.Reader }

func (r adaptiveMissingGuard) Read(ctx context.Context, path, etag string) (int, http.Header, []byte, error) {
	status, headers, data, err := r.Reader.Read(ctx, path, etag)
	if err == nil && status == http.StatusNotFound {
		return status, headers, nil, github.ErrNotFound
	}
	return status, headers, data, err
}

func (b *adaptiveRun) details(ctx context.Context, numbers []int) error {
	for _, n := range numbers {
		if _, known := b.state.Catalog[n]; !known {
			if b.w.GitHub == nil {
				return errors.New("GitHub catalog reader unavailable")
			}
			repository, err := github.ParseRepository(b.repository)
			if err != nil {
				return err
			}
			reader, err := b.w.GitHub(ctx, repository)
			if err != nil {
				return err
			}
			p, err := github.ReadPullRequest(ctx, adaptiveMissingGuard{adaptiveReadGuard{Reader: reader, now: b.w.now()}}, repository, n)
			if errors.Is(err, github.ErrNotFound) {
				b.notes = append(b.notes, fmt.Sprintf("#%d is not a pull request of this repository, so it has no details.", n))
				continue
			}
			if err != nil {
				return err
			}
			if p.Bot || (p.State != "open" && p.MergedAt == nil) {
				b.notes = append(b.notes, fmt.Sprintf("#%d was opened by a bot or closed without merging, so it is not catalogued and has no details.", n))
				continue
			}
			b.state.Catalog[n] = p
		}
		b.state.Details[n] = true
	}
	return nil
}

// Evidence stays addressable without embedding every accumulated patch into the
// next task input. Hash names are immutable within a source export and safe on
// platforms which prohibit replacing read-only files.
func (b *adaptiveRun) evidenceFiles(ctx context.Context) (map[int]string, map[int]string, map[int]string, error) {
	details, code, decisions := map[int]string{}, map[int]string{}, map[int]string{}
	write := func(kind string, number int, value any) (string, error) {
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return "", err
		}
		data, err = agent.RedactTaskOutput(ctx, b.w.redactor, data, &agent.Outcome{})
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(data)
		name := fmt.Sprintf("evidence-%s-%d-%x.json", kind, number, sum[:8])
		path := filepath.Join(b.source.Directory, name)
		if _, err := os.Stat(path); err == nil {
			return name, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if err := os.WriteFile(path, data, 0400); err != nil {
			return "", err
		}
		return name, nil
	}
	for n := range b.state.Details {
		name, err := write("details", n, b.state.Catalog[n].Body)
		if err != nil {
			return nil, nil, nil, err
		}
		details[n] = name
	}
	for n, value := range b.state.Code {
		name, err := write("code", n, value)
		if err != nil {
			return nil, nil, nil, err
		}
		code[n] = name
	}
	for n, value := range b.state.Decisions {
		name, err := write("decisions", n, value)
		if err != nil {
			return nil, nil, nil, err
		}
		decisions[n] = name
	}
	return details, code, decisions, nil
}

// Narrative edits remain durable but cannot keep a run alive indefinitely.
// Structural reconciliation, new validated references and first digest supply
// are meaningful checkpoint progress; paraphrasing text is not.
func adaptiveProgress(a *adaptiveState) string {
	type scopeProgress struct {
		Paths     []string
		Examples  []int
		HasDigest bool
	}
	scopes := map[string]scopeProgress{}
	assignments := map[int]string{}
	for key, s := range a.Scopes {
		paths := slices.Clone(s.Paths)
		examples := slices.Clone(s.Examples)
		slices.Sort(paths)
		slices.Sort(examples)
		scopes[key] = scopeProgress{slices.Compact(paths), slices.Compact(examples), strings.TrimSpace(s.Digest) != ""}
	}
	for n, v := range a.Assignments {
		assignments[n] = v.Scope
	}
	data, _ := json.Marshal(struct {
		Scopes      map[string]scopeProgress
		Selected    map[int]bool
		Assignments map[int]string
	}{scopes, a.Selected, assignments})
	return string(data)
}
