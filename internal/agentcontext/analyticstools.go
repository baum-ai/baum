package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The tools of the Analytics page and the Jobs pages. Analytics and the
// global job list read across repositories, so an action never sees them;
// one job and its failure belong to one repository, so an action may read
// its own repository's (the reader checks the job's repository).

func (s *Server) analyticsTools() []Tool {
	return []Tool{{
		Name: "analytics",
		Description: "The Analytics page's report over a window of 7, 30 or 90 days (the page's own unless given): sessions, active time, prompts and what the agents' usage would cost at API prices (a floor when some usage is unpriced), by agent, model and repository, the longest sessions, the share of commits agents wrote, what baum itself ran (reviews, fixes, failures and their reasons, merges and updates made from the app), the pull requests the person opened and merged, and the pull request flow per day. " +
			"Narrow it to one repository or agent as the page's filters do.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"days":{"type":"integer","enum":[7,30,90],"description":"The window in days. Default: the page's, else 30."},` +
			`"repository":{"type":"string","maxLength":200,"description":"Only this repository, as owner/name. Default: the page's repo filter, else all."},` +
			`"agent":{"type":"string","enum":["claude_code","codex","cursor"],"description":"Only this agent. Default: the page's, else all."}},"additionalProperties":false}`),
		Scope: ScopeGlobal,
		Reads: []string{"/api/analytics"},
		Call:  callAnalytics,
	}, {
		Name: "plan_usage",
		Description: "How much of the Claude and Codex plans the person has used, as the app's plan usage shows it: for each plan shown, its name, its windows (5-hour, weekly) with the percent used and when each resets, and when the figures were read. " +
			"It reads only what the app last fetched and never asks Claude or Codex, so a plan the app has not read yet is not listed, and a figure can be as old as fetched_at says.",
		Scope: ScopeGlobal,
		Call:  s.callPlanUsage,
	}, {
		Name: "job_list",
		Description: "The Jobs page's list across every repository, newest first: each background job's type (review, review group, fix, question, details, feature build), state (queued, running, completed, failed, cancelled, interrupted), phase, repository and pull request or group, why it ended, who started it, its backend and model, tokens and cost, and when it was queued, started and last changed. " +
			"Pass state or repository to narrow; limit (at most 200) and before (the next a result ended with) page back. jobs lists one pull request's.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"state":{"type":"string","enum":["queued","running","completed","failed","cancelled","interrupted"],"description":"Only jobs in this state."},` +
			`"repository":{"type":"string","maxLength":200,"description":"Only this repository's jobs, as owner/name."},` +
			`"limit":{"type":"integer","minimum":1,"maximum":200,"description":"The most to return. Default 50."},` +
			`"before":{"type":"string","description":"The next a previous result ended with: read the jobs older than that one."}},"additionalProperties":false}`),
		Scope: ScopeGlobal,
		Reads: []string{"/api/jobs"},
		Call:  callJobList,
	}, {
		Name:        "job",
		Description: "One job as the job page shows it: its type, state, phase, repository and pull request or group, who started it, why it ended, the failure in words when there is one, a fix's summary and pushed commit, the review threads a fix addressed, its backend and model, tokens, calls and cost, and its times. Defaults to the open job page's.",
		InputSchema: jobSchema(""),
		Scope:       ScopeRepository,
		Reads:       []string{"/api/jobs"},
		Call:        callJob(false),
	}, {
		Name: "job_failure",
		Description: "Why a failed, cancelled or interrupted job ended, as the job page says it: the reason code and the failure in words, the sections or context a review was published without, and the backend and model it ran with. " +
			"The most tokens and calls it was allowed (max_tokens, max_calls) come from the job; the effort and time limits it ran under are settings, not part of the job. Defaults to the open job page's.",
		InputSchema: jobSchema(""),
		Scope:       ScopeRepository,
		Reads:       []string{"/api/jobs"},
		Call:        callJob(true),
	}}
}

func jobSchema(extra string) json.RawMessage {
	if extra != "" {
		extra = "," + extra
	}
	return json.RawMessage(`{"type":"object","properties":{"id":{"type":"string","description":"The job's id, from job_list or jobs. Default: the open job page's."}` + extra + `},"additionalProperties":false}`)
}

// analyticsQuery is the query of an Analytics read: the arguments given, else
// the page's own filters, else the page's defaults.
func (c CallContext) analyticsQuery(args json.RawMessage) (url.Values, error) {
	var a struct {
		Days       int    `json:"days"`
		Repository string `json:"repository"`
		Agent      string `json:"agent"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, err
	}
	days := a.Days
	if days == 0 {
		if page := c.pageFilter("days", PageAnalytics); page != "" {
			if n, err := strconv.Atoi(page); err == nil && slices.Contains([]int{7, 30, 90}, n) {
				days = n
			}
		}
	}
	if days == 0 {
		days = 30
	}
	repository := a.Repository
	if repository == "" {
		repository = c.pageFilter("repo", PageAnalytics)
	}
	agent := a.Agent
	if agent == "" {
		agent = c.pageFilter("agent", PageAnalytics)
	}
	query := url.Values{"days": {strconv.Itoa(days)}}
	if repository != "" {
		if strings.Count(repository, "/") != 1 {
			return nil, errors.New("name the repository as owner/name")
		}
		query.Set("repo", strings.ToLower(repository))
	}
	if agent != "" {
		if !slices.Contains([]string{"claude_code", "codex", "cursor"}, agent) {
			return nil, errors.New("agent must be claude_code, codex or cursor")
		}
		query.Set("agent", agent)
	}
	return query, nil
}

func callAnalytics(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	query, err := c.analyticsQuery(args)
	if err != nil {
		return "", err
	}
	body, _, err := c.get(ctx, "/api/analytics", query)
	if err != nil {
		return "", err
	}
	if disabled(body) {
		return "", errors.New("analytics are unavailable: the daemon keeps none")
	}
	var report map[string]json.RawMessage
	if err := json.Unmarshal(body, &report); err != nil {
		return "", errors.New("the report is not readable")
	}
	// What the page charts, less the points of the plan usage series (the
	// plan usage tool has the plans themselves) and each merged pull request's
	// time to merge, which the page reduces to medians.
	out := map[string]json.RawMessage{}
	for _, key := range []string{"window", "filters", "options", "indexing", "summary", "daily", "hours", "by_agent", "by_model", "by_repository", "longest", "commits", "baum", "delivery"} {
		if v, ok := report[key]; ok {
			out[key] = v
		}
	}
	var usage struct {
		Available bool              `json:"available"`
		Series    []json.RawMessage `json:"series"`
	}
	if json.Unmarshal(report["plan_usage"], &usage) == nil {
		series := []map[string]json.RawMessage{}
		for _, raw := range usage.Series {
			var one map[string]json.RawMessage
			if json.Unmarshal(raw, &one) == nil {
				delete(one, "points")
				series = append(series, one)
			}
		}
		out["plan_usage"], _ = json.Marshal(map[string]any{"available": usage.Available, "series": series})
	}
	var flow map[string]json.RawMessage
	if json.Unmarshal(report["flow"], &flow) == nil {
		var days []map[string]json.RawMessage
		if json.Unmarshal(flow["days"], &days) == nil {
			for _, day := range days {
				for _, who := range []string{"mine", "team"} {
					var counts map[string]json.RawMessage
					if json.Unmarshal(day[who], &counts) == nil {
						delete(counts, "merge_seconds")
						day[who], _ = json.Marshal(counts)
					}
				}
			}
			flow["days"], _ = json.Marshal(days)
		}
		out["flow"], _ = json.Marshal(flow)
	}
	return compactJSON(out)
}

// planWindow and planFigures are what the app's plan usage shows.
type planWindow struct {
	Name        string     `json:"name"`
	UsedPercent *float64   `json:"used_percent"`
	ResetsAt    *time.Time `json:"resets_at"`
	Reset       bool       `json:"reset,omitempty"`
}

type planFigures struct {
	Provider  string       `json:"provider"`
	Available bool         `json:"available"`
	Reason    string       `json:"reason,omitempty"`
	Plan      string       `json:"plan,omitempty"`
	Windows   []planWindow `json:"windows"`
	FetchedAt time.Time    `json:"fetched_at"`
}

func (s *Server) callPlanUsage(ctx context.Context, _ CallContext, _ json.RawMessage) (string, error) {
	if s.planUsage == nil {
		return "", errors.New("plan usage is unavailable: the daemon does not read it")
	}
	// The accessor returns what the daemon last read and never reads again.
	raws, err := s.planUsage(ctx)
	if err != nil {
		return "", errors.New("plan usage is unavailable: the settings that choose the plans could not be read")
	}
	plans := []planFigures{}
	for _, raw := range raws {
		var p planFigures
		if json.Unmarshal(raw, &p) == nil {
			p.Windows = emptyList(p.Windows)
			plans = append(plans, p)
		}
	}
	if len(plans) == 0 {
		return compactJSON(map[string]any{"plans": plans, "note": "No plan has been read yet, or none is shown: the app reads plan usage when a page opens."})
	}
	return compactJSON(map[string]any{"plans": plans})
}

// jobView is a job as the Jobs and job pages show it. Never its accounting,
// the head or its configuration digests.
type jobView struct {
	ID             string     `json:"id"`
	Type           string     `json:"type"`
	State          string     `json:"state"`
	Phase          string     `json:"phase,omitempty"`
	Repository     string     `json:"repository,omitempty"`
	PullRequest    int        `json:"pull_request,omitempty"`
	Group          string     `json:"group,omitempty"`
	GroupName      string     `json:"group_name,omitempty"`
	Automatic      bool       `json:"automatic,omitempty"`
	Trigger        string     `json:"trigger,omitempty"`
	Reason         string     `json:"reason,omitempty"`
	Detail         string     `json:"detail,omitempty"`
	Backend        string     `json:"backend,omitempty"`
	Model          string     `json:"model,omitempty"`
	Calls          int        `json:"calls"`
	Tokens         int        `json:"tokens"`
	Repairs        int        `json:"repairs,omitempty"`
	CostUSD        *float64   `json:"cost_usd"`
	CostReason     string     `json:"cost_reason,omitempty"`
	MaxTokens      int        `json:"max_tokens,omitempty"`
	MaxCalls       int        `json:"max_calls,omitempty"`
	FailedSections []string   `json:"failed_sections,omitempty"`
	ContextFailed  bool       `json:"context_failed,omitempty"`
	Commit         string     `json:"commit,omitempty"`
	CommitURL      string     `json:"commit_url,omitempty"`
	Summary        string     `json:"summary,omitempty"`
	HasTranscript  bool       `json:"has_transcript,omitempty"`
	CreatedAt      *time.Time `json:"created_at,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	UpdatedAt      *time.Time `json:"updated_at,omitempty"`
}

// rawJob is the fields of a job the views read.
type rawJob struct {
	ID             string     `json:"id"`
	Kind           string     `json:"kind"`
	State          string     `json:"state"`
	Phase          string     `json:"phase"`
	Repository     string     `json:"repository"`
	PR             int        `json:"pr"`
	Group          string     `json:"group"`
	GroupName      string     `json:"group_name"`
	Automatic      bool       `json:"automatic"`
	Trigger        string     `json:"trigger"`
	Reason         string     `json:"reason"`
	Detail         string     `json:"detail"`
	Backend        string     `json:"backend"`
	Model          string     `json:"model"`
	Calls          int        `json:"calls"`
	Tokens         int        `json:"tokens"`
	Repairs        int        `json:"repairs"`
	ActualCostUSD  *float64   `json:"actual_cost_usd"`
	CostReason     string     `json:"cost_reason"`
	MaxTokens      int        `json:"max_tokens"`
	MaxCalls       int        `json:"max_calls"`
	FailedSections []string   `json:"failed_sections"`
	ContextFailed  bool       `json:"context_failed"`
	Commit         string     `json:"commit"`
	CommitURL      string     `json:"commit_url"`
	Summary        string     `json:"summary"`
	HasTranscript  bool       `json:"has_transcript"`
	CreatedAt      *time.Time `json:"created_at"`
	StartedAt      *time.Time `json:"started_at"`
	UpdatedAt      *time.Time `json:"updated_at"`
}

// jobTypes name a job's kind as the Jobs page's tag does.
var jobTypes = map[string]string{"fix": "fix", "chat": "question", "details": "details", "feature_build": "feature build"}

func (j rawJob) view() jobView {
	kind := jobTypes[j.Kind]
	if kind == "" {
		kind = "review"
		if j.Group != "" {
			kind = "review group"
		}
	}
	return jobView{ID: j.ID, Type: kind, State: j.State, Phase: j.Phase, Repository: j.Repository, PullRequest: j.PR, Group: j.Group,
		GroupName: j.GroupName, Automatic: j.Automatic, Trigger: j.Trigger, Reason: j.Reason, Detail: scrubText(j.Detail), MaxTokens: j.MaxTokens, MaxCalls: j.MaxCalls, Backend: j.Backend,
		Model: j.Model, Calls: j.Calls, Tokens: j.Tokens, Repairs: j.Repairs, CostUSD: j.ActualCostUSD, CostReason: j.CostReason,
		FailedSections: j.FailedSections, ContextFailed: j.ContextFailed, Commit: j.Commit, CommitURL: j.CommitURL, Summary: j.Summary,
		HasTranscript: j.HasTranscript, CreatedAt: j.CreatedAt, StartedAt: j.StartedAt, UpdatedAt: j.UpdatedAt}
}

func callJobList(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		State      string `json:"state"`
		Repository string `json:"repository"`
		Limit      int    `json:"limit"`
		Before     string `json:"before"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if a.Before != "" {
		if err := segment("job", a.Before); err != nil {
			return "", err
		}
	}
	limit := boundRows(a.Limit, defaultRows, maxRows)
	// The page's rows are read newest first a page at a time, as the page
	// does; a narrowed list reads pages until it has enough or runs out (at
	// most maxJobPages of them).
	var (
		out    []jobView
		before = a.Before
	)
	for pages := 0; ; pages++ {
		query := url.Values{"limit": {strconv.Itoa(maxRows)}}
		if before != "" {
			query.Set("before", before)
		}
		body, _, err := c.get(ctx, "/api/jobs", query)
		if err != nil {
			return "", err
		}
		if disabled(body) {
			return "", errors.New("jobs are unavailable: the daemon runs none")
		}
		var page struct {
			Jobs []rawJob `json:"jobs"`
			Next string   `json:"next"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return "", errors.New("the jobs are not readable")
		}
		for _, j := range page.Jobs {
			if (a.State != "" && j.State != a.State) || (a.Repository != "" && !strings.EqualFold(j.Repository, a.Repository)) {
				continue
			}
			if len(out) == limit {
				// More match than were asked for: the next page resumes
				// after the last one returned.
				return jobListResult(out, out[len(out)-1].ID)
			}
			out = append(out, j.view())
		}
		if page.Next == "" {
			return jobListResult(out, "")
		}
		if pages+1 == maxJobPages {
			return jobListResult(out, page.Next)
		}
		before = page.Next
	}
}

// maxJobPages bounds how many pages of the job history one narrowed call reads.
const maxJobPages = 10

func jobListResult(jobs []jobView, next string) (string, error) {
	result := map[string]any{"jobs": emptyList(jobs)}
	if next != "" {
		result["next"] = next
	}
	return compactJSON(result)
}

// jobID is the job a call names, else the open job page's.
func (c CallContext) jobID(args json.RawMessage) (string, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	id := a.ID
	if id == "" && c.HasPage && c.Page.Page == PageJob {
		id = c.Page.Job
	}
	if id == "" {
		return "", errors.New("name a job: no job page is open")
	}
	return id, segment("job", id)
}

func callJob(failure bool) func(context.Context, CallContext, json.RawMessage) (string, error) {
	return func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
		id, err := c.jobID(args)
		if err != nil {
			return "", err
		}
		// An action's reader checks that the job is its repository's.
		body, missing, err := c.get(ctx, "/api/jobs/"+url.PathEscape(id), nil)
		if err != nil {
			return "", err
		}
		if missing {
			return "", fmt.Errorf("job %s is not loaded: it may be archived", id)
		}
		var j rawJob
		if json.Unmarshal(body, &j) != nil {
			return "", errors.New("the job is not readable")
		}
		view := j.view()
		if !failure {
			var fix struct {
				ThreadOutcomes json.RawMessage `json:"thread_outcomes"`
			}
			_ = json.Unmarshal(body, &fix)
			return compactJSON(struct {
				jobView
				ThreadOutcomes json.RawMessage `json:"thread_outcomes,omitempty"`
			}{view, fix.ThreadOutcomes})
		}
		switch j.State {
		case "failed", "cancelled", "interrupted":
		default:
			return "", fmt.Errorf("job %s is %s: it has not failed", id, j.State)
		}
		return compactJSON(struct {
			ID             string   `json:"id"`
			Type           string   `json:"type"`
			State          string   `json:"state"`
			Phase          string   `json:"phase,omitempty"`
			Reason         string   `json:"reason,omitempty"`
			Detail         string   `json:"detail,omitempty"`
			FailedSections []string `json:"failed_sections,omitempty"`
			ContextFailed  bool     `json:"context_failed,omitempty"`
			Backend        string   `json:"backend,omitempty"`
			Model          string   `json:"model,omitempty"`
			MaxTokens      int      `json:"max_tokens,omitempty"`
			MaxCalls       int      `json:"max_calls,omitempty"`
			Automatic      bool     `json:"automatic,omitempty"`
			Trigger        string   `json:"trigger,omitempty"`
		}{view.ID, view.Type, view.State, view.Phase, view.Reason, view.Detail, view.FailedSections, view.ContextFailed, view.Backend, view.Model, view.MaxTokens, view.MaxCalls, view.Automatic, view.Trigger})
	}
}
