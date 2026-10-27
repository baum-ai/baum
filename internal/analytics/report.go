package analytics

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/fix"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/internal/repos"
)

var errUnknownRepository = errors.New("unknown repository")

// Report is the answer of GET /api/analytics. Every figure that can be
// unknown is a pointer, null when unknown, never 0, with the reason beside it
// where one is known.
type Report struct {
	Window    Window          `json:"window"`
	Filters   Filters         `json:"filters"`
	Options   Options         `json:"options"`
	Indexing  Progress        `json:"indexing"`
	Summary   Summary         `json:"summary"`
	Daily     []Day           `json:"daily"`
	Hours     []Hour          `json:"hours"`
	ByAgent   []Row           `json:"by_agent"`
	ByModel   []ModelRow      `json:"by_model"`
	ByRepo    []Row           `json:"by_repository"`
	Longest   []SessionRow    `json:"longest"`
	Commits   CommitShare     `json:"commits"`
	Baum      BaumWork        `json:"baum"`
	Delivery  DeliveryReport  `json:"delivery"`
	PlanUsage PlanUsageReport `json:"plan_usage"`
}

// Window is the report's span: whole local days, today included.
type Window struct {
	Days  int       `json:"days"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Filters echoes what the report was narrowed to; "" is everything.
type Filters struct {
	Repository string `json:"repository"`
	Agent      string `json:"agent"`
}

// Options are the filter choices: the person's repositories, and the agents
// any indexed session ran.
type Options struct {
	Repositories []string `json:"repositories"`
	Agents       []string `json:"agents"`
}

// Cost is an estimate of what the usage would cost at API prices. USD is nil
// when nothing was priced; Incomplete says part of the usage has no price or
// reported no tokens, so USD is a floor; Reasons say why, in words.
type Cost struct {
	USD        *float64 `json:"usd"`
	Incomplete bool     `json:"incomplete"`
	Reasons    []string `json:"reasons"`
}

func (c *Cost) add(other Cost) {
	if other.USD != nil {
		if c.USD == nil {
			c.USD = new(float64)
		}
		*c.USD += *other.USD
	}
	c.Incomplete = c.Incomplete || other.Incomplete
	for _, r := range other.Reasons {
		c.reason(r)
	}
}

func (c *Cost) reason(r string) {
	if c.Reasons == nil {
		c.Reasons = []string{}
	}
	if !slices.Contains(c.Reasons, r) && len(c.Reasons) < 5 {
		c.Reasons = append(c.Reasons, r)
	}
}

// Summary is the page's headline figures.
type Summary struct {
	Sessions int `json:"sessions"`
	// ActiveSeconds is nil when no session in the window recorded its
	// activity; ActiveIncomplete when some did not.
	ActiveSeconds    *int64   `json:"active_seconds"`
	ActiveIncomplete bool     `json:"active_incomplete"`
	Prompts          int      `json:"prompts"`
	Subagents        int      `json:"subagents"`
	Cost             Cost     `json:"cost"`
	AgentShare       *float64 `json:"agent_share"`
}

// Day is one local day's sessions (by when they started) and active time
// (by when it happened), per agent.
type Day struct {
	Date   string              `json:"date"`
	Agents map[string]DayAgent `json:"agents"`
}

// DayAgent is one agent's part of a day.
type DayAgent struct {
	Sessions      int   `json:"sessions"`
	ActiveSeconds int64 `json:"active_seconds"`
}

// Hour is the active time that fell in one local hour of the day, over the
// window.
type Hour struct {
	Hour          int   `json:"hour"`
	ActiveSeconds int64 `json:"active_seconds"`
}

// Row is one agent's or repository's figures.
type Row struct {
	Key             string   `json:"key"`
	Sessions        int      `json:"sessions"`
	Prompts         int      `json:"prompts"`
	ToolCalls       int      `json:"tool_calls"`
	FailedToolCalls int      `json:"failed_tool_calls"`
	FailedRate      *float64 `json:"failed_rate"`
	ActiveSeconds   *int64   `json:"active_seconds"`
	// Tokens is nil when no session reported any; TokensIncomplete when
	// some did not.
	Tokens           *int64 `json:"tokens"`
	TokensIncomplete bool   `json:"tokens_incomplete"`
	Cost             Cost   `json:"cost"`
}

// ModelRow is one model's figures; a session using two models counts in both.
type ModelRow struct {
	Model    string `json:"model"`
	Agent    string `json:"agent"`
	Sessions int    `json:"sessions"`
	Calls    int64  `json:"calls"`
	Tokens   int64  `json:"tokens"`
	Cost     Cost   `json:"cost"`
}

// SessionRow is one session among the longest.
type SessionRow struct {
	Agent           string     `json:"agent"`
	Repository      string     `json:"repository"`
	Branch          string     `json:"branch"`
	StartedAt       *time.Time `json:"started_at"`
	EndedAt         *time.Time `json:"ended_at"`
	ActiveSeconds   *int64     `json:"active_seconds"`
	Prompts         int        `json:"prompts"`
	ToolCalls       int        `json:"tool_calls"`
	FailedToolCalls int        `json:"failed_tool_calls"`
	Subagents       int        `json:"subagents"`
	Tokens          *int64     `json:"tokens"`
	Cost            Cost       `json:"cost"`
}

// BaumWork is what baum itself ran: reviews and fixes from the jobs ledger,
// and the other writes from the actions ledger.
type BaumWork struct {
	Available       bool          `json:"available"`
	Reviews         int           `json:"reviews"`
	GroupReviews    int           `json:"group_reviews"`
	Fixes           int           `json:"fixes"`
	Automatic       int           `json:"automatic"`
	Completed       int           `json:"completed"`
	Failed          int           `json:"failed"`
	Cancelled       int           `json:"cancelled"`
	Active          int           `json:"active"`
	Tokens          int           `json:"tokens"`
	Cost            Cost          `json:"cost"`
	Failures        []ReasonCount `json:"failures"`
	FixesPushed     int           `json:"fixes_pushed"`
	FixesFailed     int           `json:"fixes_failed"`
	ThreadsResolved int           `json:"threads_resolved"`
	ThreadsReplied  int           `json:"threads_replied"`
	// Merges, BranchUpdates and CheckReruns are nil without the ledger.
	Merges        *int `json:"merges"`
	BranchUpdates *int `json:"branch_updates"`
	CheckReruns   *int `json:"check_reruns"`
}

// ReasonCount is how many failed jobs failed for one reason (a job reason
// code, which the app puts in words).
type ReasonCount struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// DeliveryReport is the person's pull requests opened in the window, from
// GitHub. Status is "loading" while a search has not answered, "unavailable"
// when none could, and "ready".
type DeliveryReport struct {
	Status       string     `json:"status"`
	Reasons      []string   `json:"reasons"`
	FetchedAt    *time.Time `json:"fetched_at"`
	Repositories int        `json:"repositories"`
	// Opened and the figures below cover pull requests that are not drafts;
	// Drafts counts the drafts opened in the window.
	Opened                   int      `json:"opened"`
	Merged                   int      `json:"merged"`
	Drafts                   int      `json:"drafts"`
	MedianMergeSeconds       *float64 `json:"median_merge_seconds"`
	Reviewed                 int      `json:"reviewed"`
	MedianFirstReviewSeconds *float64 `json:"median_first_review_seconds"`
	ConflictOrFailing        int      `json:"conflict_or_failing"`
	ConflictOrFailingShare   *float64 `json:"conflict_or_failing_share"`
}

// PlanUsageReport is the sampled plan usage in the window.
type PlanUsageReport struct {
	Available bool         `json:"available"`
	Series    []PlanSeries `json:"series"`
}

// PlanSeries is one provider's window over time.
type PlanSeries struct {
	Provider string      `json:"provider"`
	Window   string      `json:"window"`
	Points   []PlanPoint `json:"points"`
	Peak     float64     `json:"peak"`
	// LimitHits counts the times the window reached 100%.
	LimitHits int `json:"limit_hits"`
}

// PlanPoint is one sampled figure.
type PlanPoint struct {
	At          time.Time `json:"at"`
	UsedPercent float64   `json:"used_percent"`
}

// planProviders maps the agent filter to the plan whose usage it is.
var planProviders = map[string]string{"claude_code": "claude", "codex": "codex"}

// jobAgents maps a job's harness to the agent filter's value.
var jobAgents = map[string]string{"claude": "claude_code", "codex": "codex", "cursor-agent": "cursor"}

// Report builds the report for q.
func (s *Service) Report(ctx context.Context, q Query) (Report, error) {
	loc := s.location()
	now := s.now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	start := today.AddDate(0, 0, -(q.Days - 1))
	report := Report{
		Window:  Window{Days: q.Days, Start: start, End: now},
		Filters: Filters{Repository: q.Repository, Agent: q.Agent},
		Daily:   []Day{}, Hours: []Hour{}, ByAgent: []Row{}, ByModel: []ModelRow{}, ByRepo: []Row{}, Longest: []SessionRow{},
	}

	clones, err := s.Registry.List(ctx)
	if err != nil {
		return Report{}, err
	}
	picked, err := s.Registry.Repositories(ctx)
	if err != nil {
		return Report{}, err
	}
	var repositories []string
	addRepository := func(name string) {
		if name = strings.ToLower(name); name != "" && !slices.Contains(repositories, name) {
			repositories = append(repositories, name)
		}
	}
	for _, r := range picked {
		addRepository(r.Repository)
	}
	for _, c := range clones {
		addRepository(c.Repository)
	}
	slices.Sort(repositories)
	if q.Repository != "" && !slices.Contains(repositories, q.Repository) {
		return Report{}, errUnknownRepository
	}

	var all []Entry
	if s.Index != nil {
		all, report.Indexing = s.Index.Snapshot()
	}
	entries := latestReadings(all)
	report.Options = Options{Repositories: repositories, Agents: []string{}}
	for _, agent := range Agents {
		if slices.ContainsFunc(entries, func(e Entry) bool { return e.Source == agent }) {
			report.Options.Agents = append(report.Options.Agents, agent)
		}
	}
	if report.Options.Repositories == nil {
		report.Options.Repositories = []string{}
	}

	var inWindow []Entry
	for _, e := range entries {
		if q.Repository != "" && e.Repository != q.Repository || q.Agent != "" && e.Source != q.Agent {
			continue
		}
		if at := startOf(e); !at.Before(start) {
			inWindow = append(inWindow, e)
		}
	}
	s.sessions(&report, inWindow, start, today, loc)

	// The share counts commits of the clones in scope against every indexed
	// session of the repository, whenever it ran.
	var scoped []repos.Clone
	for _, c := range clones {
		if c.Available && (q.Repository == "" || strings.EqualFold(c.Repository, q.Repository)) {
			scoped = append(scoped, c)
		}
	}
	if s.commits.now == nil {
		s.commits.now = s.now
	}
	byClone := map[string]cloneCommits{}
	for _, c := range scoped {
		byClone[c.ID] = s.commits.read(ctx, c)
	}
	report.Commits = share(byClone, scoped, entries, start, q.Agent)
	if len(scoped) == 0 {
		report.Commits.Reasons = append(report.Commits.Reasons, "No local clone to read commits from.")
	}
	report.Summary.AgentShare = report.Commits.Share

	report.Baum = s.baumWork(start, q)
	report.Delivery = s.delivery(start, q, picked)
	report.PlanUsage = s.planUsage(start, q)
	return report, nil
}

// latestReadings keeps one entry per session, the latest written, and none of
// baum's own runs.
func latestReadings(all []Entry) []Entry {
	best := map[string]Entry{}
	for _, e := range all {
		key := e.Source + "\x00" + e.SessionID
		if current, ok := best[key]; !ok || e.ModifiedAt.After(current.ModifiedAt) {
			best[key] = e
		}
	}
	out := make([]Entry, 0, len(best))
	for _, e := range best {
		if !e.Generation {
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b Entry) int { return startOf(a).Compare(startOf(b)) })
	return out
}

func startOf(e Entry) time.Time {
	if e.StartedAt != nil {
		return *e.StartedAt
	}
	return e.ModifiedAt
}

// sessions fills the session sections from the entries in the window.
func (s *Service) sessions(report *Report, entries []Entry, start, today time.Time, loc *time.Location) {
	prices := pricing.Active()
	days := map[string]*Day{}
	for d := start; !d.After(today); d = d.AddDate(0, 0, 1) {
		day := &Day{Date: d.Format(time.DateOnly), Agents: map[string]DayAgent{}}
		days[day.Date] = day
	}
	hours := make([]int64, 24)
	agents := map[string]*Row{}
	repositories := map[string]*Row{}
	models := map[string]*ModelRow{}
	var sessionRows []SessionRow
	summary := &report.Summary
	known := 0
	for _, e := range entries {
		cost, tokens := sessionCost(prices, e)
		summary.Sessions++
		summary.Prompts += e.Prompts
		summary.Subagents += e.Subagents
		summary.Cost.add(cost)
		if e.ActiveSeconds != nil {
			known++
		} else {
			summary.ActiveIncomplete = true
		}
		for _, row := range []*Row{rowFor(agents, e.Source), rowFor(repositories, e.Repository)} {
			row.Sessions++
			row.Prompts += e.Prompts
			row.ToolCalls += e.ToolCalls
			row.FailedToolCalls += e.FailedTools
			if e.ActiveSeconds != nil {
				row.ActiveSeconds = addInt(row.ActiveSeconds, *e.ActiveSeconds)
			}
			if tokens != nil {
				row.Tokens = addInt(row.Tokens, *tokens)
			}
			row.TokensIncomplete = row.TokensIncomplete || e.TokensUnknown
			row.Cost.add(cost)
		}
		seenModel := map[string]bool{}
		for _, g := range e.Usage {
			name := modelName(prices, g.Model, e.Source)
			key := e.Source + "\x00" + name
			m := models[key]
			if m == nil {
				m = &ModelRow{Model: name, Agent: e.Source}
				models[key] = m
			}
			if !seenModel[key] {
				seenModel[key] = true
				m.Sessions++
			}
			m.Calls += g.Calls
			m.Tokens += g.Tokens(e.Source)
			m.Cost.add(groupCost(prices, e.Source, g))
		}
		date := startOf(e).In(loc).Format(time.DateOnly)
		if day := days[date]; day != nil {
			part := day.Agents[e.Source]
			part.Sessions++
			day.Agents[e.Source] = part
		}
		for hour, seconds := range e.Hours {
			at := time.Unix(hour*3600, 0).In(loc)
			hours[at.Hour()] += seconds
			if day := days[at.Format(time.DateOnly)]; day != nil {
				part := day.Agents[e.Source]
				part.ActiveSeconds += seconds
				day.Agents[e.Source] = part
			}
		}
		sessionRows = append(sessionRows, SessionRow{
			Agent: e.Source, Repository: e.Repository, Branch: e.Branch, StartedAt: e.StartedAt, EndedAt: e.EndedAt,
			ActiveSeconds: e.ActiveSeconds, Prompts: e.Prompts, ToolCalls: e.ToolCalls, FailedToolCalls: e.FailedTools,
			Subagents: e.Subagents, Tokens: tokens, Cost: cost,
		})
	}
	if known > 0 {
		var total int64
		for _, e := range entries {
			if e.ActiveSeconds != nil {
				total += *e.ActiveSeconds
			}
		}
		summary.ActiveSeconds = &total
	}
	if summary.Cost.Reasons == nil {
		summary.Cost.Reasons = []string{}
	}
	for d := start; !d.After(today); d = d.AddDate(0, 0, 1) {
		report.Daily = append(report.Daily, *days[d.Format(time.DateOnly)])
	}
	for h, seconds := range hours {
		report.Hours = append(report.Hours, Hour{Hour: h, ActiveSeconds: seconds})
	}
	report.ByAgent = rows(agents, func(a, b Row) int {
		return cmp.Compare(slices.Index(Agents, a.Key), slices.Index(Agents, b.Key))
	})
	report.ByRepo = rows(repositories, func(a, b Row) int {
		return cmp.Or(cmp.Compare(b.Sessions, a.Sessions), strings.Compare(a.Key, b.Key))
	})
	for _, m := range models {
		if m.Cost.Reasons == nil {
			m.Cost.Reasons = []string{}
		}
		report.ByModel = append(report.ByModel, *m)
	}
	slices.SortFunc(report.ByModel, func(a, b ModelRow) int {
		return cmp.Or(cmp.Compare(b.Tokens, a.Tokens), strings.Compare(a.Model, b.Model))
	})
	slices.SortStableFunc(sessionRows, func(a, b SessionRow) int {
		return cmp.Compare(deref64(b.ActiveSeconds), deref64(a.ActiveSeconds))
	})
	for _, row := range sessionRows {
		if len(report.Longest) == 5 || row.ActiveSeconds == nil || *row.ActiveSeconds == 0 {
			break
		}
		report.Longest = append(report.Longest, row)
	}
}

func rowFor(rows map[string]*Row, key string) *Row {
	row := rows[key]
	if row == nil {
		row = &Row{Key: key, Cost: Cost{Reasons: []string{}}}
		rows[key] = row
	}
	return row
}

func rows(m map[string]*Row, order func(a, b Row) int) []Row {
	out := make([]Row, 0, len(m))
	for _, row := range m {
		if row.ToolCalls > 0 {
			row.FailedRate = ratio(row.FailedToolCalls, row.ToolCalls)
		}
		out = append(out, *row)
	}
	slices.SortFunc(out, order)
	return out
}

func addInt(total *int64, n int64) *int64 {
	if total == nil {
		total = new(int64)
	}
	*total += n
	return total
}

// modelName is the price list's name for a model, or what the agent recorded
// when the list does not know it.
func modelName(prices *pricing.List, raw, vendor string) string {
	if model := prices.Normalize(raw, vendor); model.Known() {
		return model.Canonical
	}
	if strings.TrimSpace(raw) == "" {
		return "Unknown model"
	}
	return raw
}

// sessionCost prices a session's usage at read time, so a corrected price
// corrects history, and sums its tokens (nil when none were reported).
func sessionCost(prices *pricing.List, e Entry) (Cost, *int64) {
	cost := Cost{Reasons: []string{}}
	var tokens *int64
	for _, g := range e.Usage {
		cost.add(groupCost(prices, e.Source, g))
		tokens = addInt(tokens, g.Tokens(e.Source))
	}
	if e.TokensUnknown {
		cost.Incomplete = true
		cost.reason("Some model calls reported no token usage.")
	}
	return cost, tokens
}

// groupCost prices one usage group. A group with no price has no cost, never
// $0.00, and makes the total incomplete.
func groupCost(prices *pricing.List, vendor string, g Usage) Cost {
	model := prices.Normalize(g.Model, vendor)
	usage := pricing.Usage{
		Vendor: vendor, InputTokens: g.Input, OutputTokens: g.Output, CacheReadTokens: g.CacheRead,
		CacheWriteTokens: g.CacheWrite, CacheWrite1hTokens: g.CacheWrite1h, Speed: pricing.Speed(g.Speed),
		PromptRangeKnown: true, MinPromptTokens: g.MinPrompt, MaxPromptTokens: g.MaxPrompt,
	}
	if usd, ok := prices.Cost(model, usage); ok {
		return Cost{USD: &usd, Reasons: []string{}}
	}
	cost := Cost{Incomplete: true}
	switch {
	case strings.TrimSpace(g.Model) == "":
		cost.reason("Some model calls did not record their model.")
	case !model.Known():
		cost.reason("The price list has no price for " + g.Model + ".")
	case pricing.UnpricedReason(model) != "":
		cost.reason(model.Canonical + " has no price: " + pricing.UnpricedReason(model) + ".")
	default:
		cost.reason("Some usage of " + model.Canonical + " cannot be priced (a speed or prompt length the price list has no rate for).")
	}
	return cost
}

// baumWork counts the jobs created in the window.
func (s *Service) baumWork(start time.Time, q Query) BaumWork {
	work := BaumWork{Failures: []ReasonCount{}, Cost: Cost{Reasons: []string{}}}
	if s.Jobs == nil {
		return work
	}
	work.Available = true
	failures := map[string]int{}
	for _, j := range s.Jobs() {
		if j.CreatedAt.Before(start) || q.Repository != "" && !strings.EqualFold(j.Repository, q.Repository) || q.Agent != "" && jobAgents[j.Backend] != q.Agent {
			continue
		}
		switch {
		case j.Kind == jobs.KindFix:
			work.Fixes++
		case j.Group != "":
			work.GroupReviews++
		default:
			work.Reviews++
		}
		if j.Automatic {
			work.Automatic++
		}
		work.Tokens += j.Tokens
		switch j.State {
		case "completed":
			work.Completed++
		case "failed":
			work.Failed++
			failures[cmp.Or(j.Reason, "internal")]++
		case "cancelled", "interrupted":
			work.Cancelled++
		default:
			work.Active++
		}
		if j.ActualCostUSD != nil {
			usd := *j.ActualCostUSD
			work.Cost.add(Cost{USD: &usd})
		} else if j.Calls > 0 || j.Tokens > 0 || j.State == "completed" {
			work.Cost.Incomplete = true
			work.Cost.reason("Some runs have no price, or reported no usage.")
		}
		if j.Kind == jobs.KindFix {
			if j.Commit != "" {
				work.FixesPushed++
			} else if j.State == "failed" {
				work.FixesFailed++
			}
			for _, t := range j.ThreadOutcomes {
				switch t.Outcome {
				case fix.ThreadResolved:
					work.ThreadsResolved++
				case fix.ThreadReplied:
					work.ThreadsReplied++
				}
			}
		}
	}
	for reason, n := range failures {
		work.Failures = append(work.Failures, ReasonCount{Reason: reason, Count: n})
	}
	slices.SortFunc(work.Failures, func(a, b ReasonCount) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), strings.Compare(a.Reason, b.Reason))
	})
	if s.Actions != nil {
		merges, updates, reruns := 0, 0, 0
		for _, a := range s.Actions.Since(start) {
			// The writes are the person's, whichever agent they use.
			if q.Repository != "" && a.Repository != q.Repository {
				continue
			}
			switch a.Kind {
			case ActionMerge:
				merges++
			case ActionUpdateBranch:
				updates++
			case ActionRerunChecks:
				reruns++
			}
		}
		work.Merges, work.BranchUpdates, work.CheckReruns = &merges, &updates, &reruns
	}
	return work
}

// delivery aggregates the cached searches of the picked repositories in
// scope; the agent filter does not apply to pull requests.
func (s *Service) delivery(start time.Time, q Query, picked []repos.Repository) DeliveryReport {
	report := DeliveryReport{Status: "unavailable", Reasons: []string{}}
	if s.Delivery == nil {
		report.Reasons = append(report.Reasons, "GitHub is not available to the daemon.")
		return report
	}
	var names []string
	for _, r := range picked {
		if name := strings.ToLower(r.Repository); q.Repository == "" || name == q.Repository {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		report.Status = "ready"
		return report
	}
	report.Repositories = len(names)
	var merge, review []float64
	loading, loaded := false, false
	for _, r := range s.Delivery.Pulls(names) {
		if r.Reason != "" && !slices.Contains(report.Reasons, r.Reason) {
			report.Reasons = append(report.Reasons, r.Reason)
		}
		if !r.Loaded {
			loading = loading || r.Reason == ""
			continue
		}
		loaded = true
		if at := r.FetchedAt; report.FetchedAt == nil || at.Before(*report.FetchedAt) {
			report.FetchedAt = &at
		}
		for _, pr := range r.Pulls {
			if pr.CreatedAt.Before(start) {
				continue
			}
			if pr.Draft {
				report.Drafts++
				continue
			}
			report.Opened++
			if pr.MergedAt != nil {
				report.Merged++
				merge = append(merge, pr.MergedAt.Sub(pr.CreatedAt).Seconds())
			}
			if pr.FirstReviewAt != nil {
				report.Reviewed++
				review = append(review, max(0, pr.FirstReviewAt.Sub(pr.CreatedAt).Seconds()))
			}
			if pr.Conflict || pr.ChecksFailing {
				report.ConflictOrFailing++
			}
		}
	}
	switch {
	case loading:
		report.Status = "loading"
	case loaded:
		report.Status = "ready"
	}
	report.MedianMergeSeconds = median(merge)
	report.MedianFirstReviewSeconds = median(review)
	if report.Opened > 0 {
		report.ConflictOrFailingShare = ratio(report.ConflictOrFailing, report.Opened)
	}
	return report
}

func median(values []float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	slices.Sort(values)
	m := values[len(values)/2]
	if len(values)%2 == 0 {
		m = (values[len(values)/2-1] + m) / 2
	}
	return &m
}

// planUsage groups the samples in the window by provider and window.
func (s *Service) planUsage(start time.Time, q Query) PlanUsageReport {
	report := PlanUsageReport{Series: []PlanSeries{}}
	if s.Plans == nil {
		return report
	}
	report.Available = true
	index := map[string]int{}
	for _, sample := range s.Plans.Since(start) {
		if q.Agent != "" && planProviders[q.Agent] != sample.Provider {
			continue
		}
		key := sample.Provider + "\x00" + sample.Window
		i, ok := index[key]
		if !ok {
			i = len(report.Series)
			index[key] = i
			report.Series = append(report.Series, PlanSeries{Provider: sample.Provider, Window: sample.Window})
		}
		series := &report.Series[i]
		previous := -1.0
		if n := len(series.Points); n > 0 {
			previous = series.Points[n-1].UsedPercent
		}
		if sample.UsedPercent >= 100 && previous < 100 {
			series.LimitHits++
		}
		series.Peak = max(series.Peak, sample.UsedPercent)
		series.Points = append(series.Points, PlanPoint{At: sample.At, UsedPercent: sample.UsedPercent})
	}
	slices.SortStableFunc(report.Series, func(a, b PlanSeries) int {
		return cmp.Or(strings.Compare(a.Provider, b.Provider), cmp.Compare(windowOrder(a.Window), windowOrder(b.Window)), strings.Compare(a.Window, b.Window))
	})
	return report
}

func windowOrder(name string) int {
	switch name {
	case "5-hour":
		return 0
	case "weekly":
		return 1
	}
	return 2
}
