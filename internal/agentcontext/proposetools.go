package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/baum-ai/baum/internal/uistate"
)

// The propose_* tools: a chat suggests a change to what the open page edits,
// as a diff the person saves or dismisses. Nothing changes until the person
// saves; the save is the page's own save (each page's Proposer, given in
// Options.Proposers). Each tool names its page and its target and passes the
// change the agent asked for as the proposal's After; the agent learns only a
// Receipt, never the target's current value. The tools are global, so an
// action never sees them, and Propose refuses any consumer but a chat, and
// any page but the one the person has open.
//
// The targets, by page:
//
//   - settings: TargetAppearance and TargetPlanUsage+provider (General),
//     TargetReviews and TargetDecisions (Reviews), TargetFixes (Fixes),
//     TargetTimeLimits, TargetJobConcurrency, TargetCodexApprovals and
//     TargetAgentEnv+NAME (This computer), TargetPrompt+id (Prompts, the
//     global prompts).
//   - repositories, showing a repository: RepositoryTarget(repository,
//     TargetRepositorySettings) (Reviews), RepositoryTarget(repository,
//     TargetPrompt+id) (Prompts).
//   - inbox: TargetNewInbox, or TargetInbox+id for a saved inbox.
//   - group: TargetGroup+id, the group the page shows.
//   - pull_request: PullRequestTarget(repository, number), the person's
//     pending review of the pull request the page shows.
const (
	TargetAppearance         = "appearance"
	TargetPlanUsage          = "plan_usage/"
	TargetReviews            = "reviews"
	TargetDecisions          = "decisions"
	TargetFixes              = "fixes"
	TargetTimeLimits         = "time_limits"
	TargetJobConcurrency     = "job_concurrency"
	TargetCodexApprovals     = "codex_approvals"
	TargetAgentEnv           = "agent_env/"
	TargetPrompt             = "prompt/"
	TargetRepositorySettings = "settings"
	TargetNewInbox           = "inbox:new"
	TargetInbox              = "inbox:"
	TargetGroup              = "group:"
	// targetReviewComment ends a pull request's target (PullRequestTarget).
	targetReviewComment = ":review_comment"
)

// RepositoryTarget is the target of what on a repository's page.
func RepositoryTarget(repository, what string) string { return repository + ":" + what }

// ParseRepositoryTarget splits a target of a repository's page.
func ParseRepositoryTarget(target string) (repository, what string, ok bool) {
	repository, what, ok = strings.Cut(target, ":")
	return repository, what, ok && repository != "" && what != ""
}

// PullRequestTarget is the target of a comment added to the person's
// pending review of a pull request.
func PullRequestTarget(repository string, number int) string {
	return repository + "#" + strconv.Itoa(number) + targetReviewComment
}

// ParsePullRequestTarget splits a PullRequestTarget.
func ParsePullRequestTarget(target string) (repository string, number int, ok bool) {
	rest, found := strings.CutSuffix(target, targetReviewComment)
	if !found {
		return "", 0, false
	}
	i := strings.LastIndexByte(rest, '#')
	if i <= 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(rest[i+1:])
	if err != nil || n < 1 {
		return "", 0, false
	}
	return rest[:i], n, true
}

var (
	// proposePageTools are the propose_* tools of each page.
	proposePageTools = map[string][]string{
		PageSettings:     {"propose_appearance", "propose_plan_usage", "propose_reviews", "propose_decision_capture", "propose_fixes", "propose_time_limits", "propose_job_concurrency", "propose_codex_approvals", "propose_agent_env", "propose_prompt", "propose_prompt_reset"},
		PageRepositories: {"propose_repository_settings", "propose_prompt", "propose_prompt_reset"},
		PageInbox:        {"propose_inbox"},
		PageGroup:        {"propose_group"},
		PagePullRequest:  {"propose_review_comment"},
	}
	// settingsSectionProposals and repositorySectionProposals are the
	// propose_* tools of each section, which current_page lists with the
	// section's tools.
	settingsSectionProposals = map[string][]string{
		"general":  {"propose_appearance", "propose_plan_usage"},
		"reviews":  {"propose_reviews", "propose_decision_capture"},
		"fixes":    {"propose_fixes"},
		"computer": {"propose_time_limits", "propose_job_concurrency", "propose_codex_approvals", "propose_agent_env"},
		"prompts":  {"propose_prompt", "propose_prompt_reset"},
	}
	repositorySectionProposals = map[string][]string{
		"reviews": {"propose_repository_settings"},
		"prompts": {"propose_prompt", "propose_prompt_reset"},
	}
)

// proposed is what a propose_* tool answers: the receipt and what happens
// next. Never the target's current value.
type proposed struct {
	Receipt
	Status string `json:"status"`
}

const proposedStatus = "proposed: the person sees the change in the app and saves or dismisses it; nothing changes until they save"

// propose records a proposal of after for target on page and answers the
// receipt.
func propose(ctx context.Context, c CallContext, page, target string, after any) (string, error) {
	if !c.HasPage {
		return "", errors.New("no page is open: the person has not opened a page in the baum app for this conversation")
	}
	raw, err := json.Marshal(after)
	if err != nil {
		return "", err
	}
	receipt, err := c.Propose(ctx, Proposal{Page: page, Target: target, After: raw})
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(proposed{receipt, proposedStatus})
	return string(out), err
}

// strictArgs decodes a tool's arguments, which its schema already checked.
func strictArgs(args json.RawMessage, v any) error {
	d := json.NewDecoder(strings.NewReader(string(args)))
	d.DisallowUnknownFields()
	return d.Decode(v)
}

// present reports which keys args sets, so a null is told from a key left
// out.
func present(args json.RawMessage) map[string]bool {
	var keys map[string]json.RawMessage
	_ = json.Unmarshal(args, &keys)
	out := map[string]bool{}
	for k := range keys {
		out[k] = true
	}
	return out
}

// promptTarget is the target of a prompt on the open page: a global prompt on
// Settings, the page's repository's on a repository's page. A repository
// named must be the page's.
func promptTarget(c CallContext, repository, id string) (page, target string, err error) {
	if !c.HasPage {
		return "", "", errors.New("no page is open: the person has not opened a page in the baum app for this conversation")
	}
	repository = strings.TrimSpace(repository)
	switch {
	case c.Page.Page == PageSettings:
		if repository != "" {
			return "", "", errors.New("a repository's prompts are on its page: the person has Settings open, whose prompts are the global ones")
		}
		return PageSettings, TargetPrompt + id, nil
	case c.Page.Page == PageRepositories && c.Page.Repository != "":
		if repository != "" && !strings.EqualFold(repository, c.Page.Repository) {
			return "", "", fmt.Errorf("the person has %s's page open: a proposal is for that repository's prompts", c.Page.Repository)
		}
		return PageRepositories, RepositoryTarget(c.Page.Repository, TargetPrompt+id), nil
	}
	return "", "", ErrNotCurrentPage
}

// proposeTools are the propose_* tools. Each is global (actions never see
// it) and reads nothing.
func proposeTools() []Tool {
	effort := `{"type":"string","maxLength":32,"description":"The reasoning effort: low, medium, high, xhigh, max or ultra; empty for the harness's own."}`
	return []Tool{{
		Name:        "propose_appearance",
		Description: "Proposes a change to the Settings page's General section appearance: the theme, whether key hints show, and the modifier held with 1 to 8 for the sidebar's pages. The person must have Settings open at General. Nothing changes until the person saves the proposal; you get its id, never the current values.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"theme":{"type":"string","maxLength":16,"description":"system, light or dark."},` +
			`"key_hints":{"type":"boolean"},` +
			`"page_modifier":{"type":"string","maxLength":16,"description":"meta, ctrl or alt; empty is the platform's command key."}},` +
			`"minProperties":1,"additionalProperties":false}`),
		Proposes: true,
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			var a struct {
				Theme        *string `json:"theme,omitempty"`
				KeyHints     *bool   `json:"key_hints,omitempty"`
				PageModifier *string `json:"page_modifier,omitempty"`
			}
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			return propose(ctx, c, PageSettings, TargetAppearance, a)
		},
	}, {
		Name:        "propose_plan_usage",
		Description: "Proposes showing or hiding a plan's usage (Claude Code or Codex) in the sidebar and tray, or null to follow the harnesses baum uses again, on the Settings page's General section. The person must have Settings open at General. Nothing changes until the person saves the proposal.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"provider":{"type":"string","maxLength":32,"description":"claude or codex."},` +
			`"shown":{"type":["boolean","null"],"description":"true shows the plan, false hides it, null follows the harnesses in use."}},` +
			`"required":["provider","shown"],"additionalProperties":false}`),
		Proposes: true,
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			var a struct {
				Provider string `json:"provider"`
				Shown    *bool  `json:"shown"`
			}
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			return propose(ctx, c, PageSettings, TargetPlanUsage+a.Provider, struct {
				Shown *bool `json:"shown"`
			}{a.Shown})
		},
	}, {
		Name:        "propose_reviews",
		Description: "Proposes a change to the Settings page's Reviews section: whether automatic reviews are on, for which pull requests (both: the person's own and those they were asked to review; authored; requested; all), and the harness reviews use (backend: auto, claude or codex), its model (empty for the harness's default) and effort. Only the fields given change. The person must have Settings open at Reviews. Nothing changes until the person saves the proposal.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"enabled":{"type":"boolean","description":"Automatic reviews on or off."},` +
			`"pull_requests":{"type":"string","maxLength":32,"description":"both, authored, requested or all."},` +
			`"backend":{"type":"string","maxLength":32,"description":"auto, claude or codex."},` +
			`"model":{"type":"string","maxLength":256},` +
			`"effort":` + effort + `},` +
			`"minProperties":1,"additionalProperties":false}`),
		Proposes: true,
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			var a struct {
				Enabled      *bool   `json:"enabled,omitempty"`
				PullRequests *string `json:"pull_requests,omitempty"`
				Backend      *string `json:"backend,omitempty"`
				Model        *string `json:"model,omitempty"`
				Effort       *string `json:"effort,omitempty"`
			}
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			return propose(ctx, c, PageSettings, TargetReviews, a)
		},
	}, {
		Name:        "propose_decision_capture",
		Description: "Proposes a change to decision capture on the Settings page's Reviews section: on or off, the harness it runs with (backend: claude or codex; empty follows the reviews'), its model (empty for the harness's default) and effort. Only the fields given change. The person must have Settings open at Reviews. Nothing changes until the person saves the proposal.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"enabled":{"type":"boolean"},` +
			`"backend":{"type":"string","maxLength":32},` +
			`"model":{"type":"string","maxLength":256},` +
			`"effort":` + effort + `},` +
			`"minProperties":1,"additionalProperties":false}`),
		Proposes: true,
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			var a struct {
				Enabled *bool   `json:"enabled,omitempty"`
				Backend *string `json:"backend,omitempty"`
				Model   *string `json:"model,omitempty"`
				Effort  *string `json:"effort,omitempty"`
			}
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			return propose(ctx, c, PageSettings, TargetDecisions, a)
		},
	}, {
		Name:        "propose_fixes",
		Description: "Proposes a change to the Settings page's Fixes section: the harness fixes use (backend: claude or codex; empty is the same as reviews'), its model (empty for the harness's default) and effort. Only the fields given change. The person must have Settings open at Fixes. Nothing changes until the person saves the proposal.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"backend":{"type":"string","maxLength":32},` +
			`"model":{"type":"string","maxLength":256},` +
			`"effort":` + effort + `},` +
			`"minProperties":1,"additionalProperties":false}`),
		Proposes: true,
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			var a struct {
				Backend *string `json:"backend,omitempty"`
				Model   *string `json:"model,omitempty"`
				Effort  *string `json:"effort,omitempty"`
			}
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			return propose(ctx, c, PageSettings, TargetFixes, a)
		},
	}, {
		Name:        "propose_time_limits",
		Description: "Proposes new time limits, in minutes, on the Settings page's This computer section: how long a review, and any other agent run, may take. One left out keeps its current limit. The person must have Settings open at This computer. Nothing changes until the person saves the proposal.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"review_time_limit_minutes":{"type":"integer","description":"1 to 240."},` +
			`"run_time_limit_minutes":{"type":"integer","description":"1 to 240."}},` +
			`"minProperties":1,"additionalProperties":false}`),
		Proposes: true,
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			var a struct {
				Review *int `json:"review_time_limit_minutes,omitempty"`
				Run    *int `json:"run_time_limit_minutes,omitempty"`
			}
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			return propose(ctx, c, PageSettings, TargetTimeLimits, a)
		},
	}, {
		Name:        "propose_job_concurrency",
		Description: "Proposes how many jobs baum runs at once (1 to 4), on the Settings page's This computer section. The person must have Settings open at This computer. Nothing changes until the person saves the proposal.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"job_concurrency":{"type":"integer","description":"1 to 4."}},"required":["job_concurrency"],"additionalProperties":false}`),
		Proposes:    true,
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			var a struct {
				JobConcurrency int `json:"job_concurrency"`
			}
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			return propose(ctx, c, PageSettings, TargetJobConcurrency, a)
		},
	}, {
		Name:        "propose_codex_approvals",
		Description: "Proposes whether the app answers Codex's permission requests (agent approvals), on the Settings page's This computer section. The person must have Settings open at This computer. Nothing changes until the person saves the proposal.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"codex_approvals":{"type":"boolean"}},"required":["codex_approvals"],"additionalProperties":false}`),
		Proposes:    true,
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			var a struct {
				CodexApprovals bool `json:"codex_approvals"`
			}
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			return propose(ctx, c, PageSettings, TargetCodexApprovals, a)
		},
	}, {
		Name: "propose_agent_env",
		Description: "Proposes adding a variable to the environment baum gives agents, by name only, or removing one, on the Settings page's This computer section. You never give, see or learn a value: the person types the value when saving an addition. Adding a name that is set, or removing one that is not, is refused. " +
			"The person must have Settings open at This computer. Nothing changes until the person saves the proposal.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"name":{"type":"string","minLength":1,"maxLength":256,"pattern":"^[A-Za-z_][A-Za-z0-9_]*$","description":"The variable's name."},` +
			`"action":{"type":"string","enum":["add","remove"]}},` +
			`"required":["name","action"],"additionalProperties":false}`),
		Proposes: true,
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			var a struct {
				Name   string `json:"name"`
				Action string `json:"action"`
			}
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			after := AgentEnvChange{Name: a.Name}
			if a.Action == "remove" {
				after.Remove = true
			} else {
				after.NeedsValue = true
			}
			return propose(ctx, c, PageSettings, TargetAgentEnv+a.Name, after)
		},
	}, {
		Name: "propose_prompt",
		Description: "Proposes a new custom text for one prompt (see prompts for the ids): on the Settings page's Prompts section a global prompt, on a repository page's Prompts section that repository's. The text is validated as the prompt editor does; a text that leaves out a safety rule the prompt in use has is refused. " +
			"The person must have that Prompts section open. Nothing changes until the person saves the proposal.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"id":{"type":"string","minLength":1,"maxLength":64},` +
			`"text":{"type":"string","minLength":1,"maxLength":65536},` +
			`"repository":{"type":"string","maxLength":200,"description":"The repository as owner/name, only to check it is the open page's. Default: the open page's, or the global prompts on Settings."}},` +
			`"required":["id","text"],"additionalProperties":false}`),
		Proposes: true,
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			var a struct {
				ID         string `json:"id"`
				Text       string `json:"text"`
				Repository string `json:"repository"`
			}
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			page, target, err := promptTarget(c, a.Repository, a.ID)
			if err != nil {
				return "", err
			}
			return propose(ctx, c, page, target, PromptChange{Text: &a.Text})
		},
	}, {
		Name:        "propose_prompt_reset",
		Description: "Proposes resetting one customised prompt: on the Settings page's Prompts section back to the built-in text, on a repository page's back to the text it inherits (the global custom text, else the built-in). Refused when the prompt is not customised there, or when the text it goes back to leaves out a safety rule the prompt in use has. The person must have that Prompts section open. Nothing changes until the person saves the proposal.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"id":{"type":"string","minLength":1,"maxLength":64},` +
			`"repository":{"type":"string","maxLength":200,"description":"The repository as owner/name, only to check it is the open page's."}},` +
			`"required":["id"],"additionalProperties":false}`),
		Proposes: true,
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			var a struct {
				ID         string `json:"id"`
				Repository string `json:"repository"`
			}
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			page, target, err := promptTarget(c, a.Repository, a.ID)
			if err != nil {
				return "", err
			}
			return propose(ctx, c, page, target, PromptChange{})
		},
	}, {
		Name:        "propose_repository_settings",
		Description: "Proposes a change to a repository page's Reviews section: its automatic review choice and its decision capture choice, each true, false, or null to follow the global switch. Only the fields given change. The person must have that repository's page open at Reviews. Nothing changes until the person saves the proposal.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"automatic":{"type":["boolean","null"]},` +
			`"decisions":{"type":["boolean","null"]},` +
			`"repository":{"type":"string","maxLength":200,"description":"The repository as owner/name, only to check it is the open page's."}},` +
			`"additionalProperties":false}`),
		Proposes: true,
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			var a struct {
				Automatic  *bool  `json:"automatic"`
				Decisions  *bool  `json:"decisions"`
				Repository string `json:"repository"`
			}
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			keys := present(args)
			if !keys["automatic"] && !keys["decisions"] {
				return "", errors.New("give automatic, decisions or both")
			}
			if !c.HasPage || c.Page.Page != PageRepositories || c.Page.Repository == "" {
				return "", ErrNotCurrentPage
			}
			if r := strings.TrimSpace(a.Repository); r != "" && !strings.EqualFold(r, c.Page.Repository) {
				return "", fmt.Errorf("the person has %s's page open: a proposal is for that repository", c.Page.Repository)
			}
			after := map[string]*bool{}
			if keys["automatic"] {
				after["automatic"] = a.Automatic
			}
			if keys["decisions"] {
				after["decisions"] = a.Decisions
			}
			return propose(ctx, c, PageRepositories, RepositoryTarget(c.Page.Repository, TargetRepositorySettings), after)
		},
	}, {
		Name: "propose_inbox",
		Description: "Proposes a new saved inbox, or with id a change to a saved one (see inboxes): its name, color, query (the Inbox filter syntax; test it with test_query) and whether the sidebar shows its count. Only the fields given change; a new inbox needs a name, and starts with no filter and a count. " +
			"The person must have the Inbox open. Nothing changes until the person saves the proposal.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"id":{"type":"string","pattern":"^[0-9a-f]{32}$","description":"The saved inbox to change; omit to propose a new one."},` +
			`"name":{"type":"string","minLength":1,"maxLength":40},` +
			`"color":{"type":"string","maxLength":16,"description":"blue, green, red, amber, purple or grey."},` +
			`"query":{"type":"string","maxLength":500},` +
			`"badge":{"type":"boolean"}},` +
			`"minProperties":1,"additionalProperties":false}`),
		Proposes: true,
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			var a struct {
				ID string `json:"id,omitempty"`
				InboxChange
			}
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			target := TargetNewInbox
			if a.ID != "" {
				target = TargetInbox + a.ID
			}
			return propose(ctx, c, PageInbox, target, a.InboxChange)
		},
	}, {
		Name:        "propose_group",
		Description: "Proposes a change to the review group the page shows: a new name, pull requests to add and pull requests to remove (by number, in the group's repository). A group keeps 2 to 8 pull requests. The person must have the group's page open. Nothing changes until the person saves the proposal.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"name":{"type":"string","minLength":1,"maxLength":80},` +
			`"add":{"type":"array","maxItems":8,"items":{"type":"integer","minimum":1}},` +
			`"remove":{"type":"array","maxItems":8,"items":{"type":"integer","minimum":1}}},` +
			`"minProperties":1,"additionalProperties":false}`),
		Proposes: true,
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			var a GroupChange
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			if !c.HasPage || c.Page.Page != PageGroup || c.Page.Group == "" {
				return "", ErrNotCurrentPage
			}
			return propose(ctx, c, PageGroup, TargetGroup+c.Page.Group, a)
		},
	}, {
		Name: "propose_review_comment",
		Description: "Proposes a comment for the person's own pending review of the pull request the page shows, which they send from the app: its body, what it is about (target: general, or a section, risk or file of the review by id, with a title), and optionally the diff line it is placed on (path, line, side RIGHT or LEFT, optional start_line) at the commit head. Saving adds it to the review's comments and keeps the person's other drafts. " +
			"The person must have the pull request's page open. Nothing changes until the person saves the proposal.",
		InputSchema: reviewCommentSchema,
		Proposes:    true,
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			var a ReviewCommentChange
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			if !c.HasPage || c.Page.Page != PagePullRequest || c.Page.Repository == "" || c.Page.PullRequest < 1 {
				return "", ErrNotCurrentPage
			}
			return propose(ctx, c, PagePullRequest, PullRequestTarget(c.Page.Repository, c.Page.PullRequest), a)
		},
	}}
}

// AgentEnvChange is the After of an agent environment proposal: a name to
// add, whose value the person types when saving (NeedsValue), or a name to
// remove. It never holds a value.
type AgentEnvChange struct {
	Name       string `json:"name"`
	NeedsValue bool   `json:"needs_value,omitempty"`
	Remove     bool   `json:"remove,omitempty"`
}

// PromptChange is the After of a prompt proposal: the new custom text, or
// nil to reset the prompt.
type PromptChange struct {
	Text *string `json:"text"`
}

// InboxChange is the After of a saved inbox proposal: the fields that change.
type InboxChange struct {
	Name  *string `json:"name,omitempty"`
	Color *string `json:"color,omitempty"`
	Query *string `json:"query,omitempty"`
	Badge *bool   `json:"badge,omitempty"`
}

// GroupChange is the After of a review group proposal.
type GroupChange struct {
	Name   *string `json:"name,omitempty"`
	Add    []int   `json:"add,omitempty"`
	Remove []int   `json:"remove,omitempty"`
}

// ReviewCommentChange is the After of a pending review comment proposal: the
// comment the person's save adds, which gets its id and time then.
type ReviewCommentChange struct {
	Body   string `json:"body"`
	Target *struct {
		Kind  string `json:"kind"`
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"target,omitempty"`
	Place *struct {
		Path      string `json:"path"`
		Line      int    `json:"line"`
		Side      string `json:"side"`
		StartLine *int   `json:"start_line,omitempty"`
	} `json:"place,omitempty"`
	Head string `json:"head,omitempty"`
}

// reviewCommentSchema is the arguments of a pending review comment
// (ReviewCommentChange), proposed as a change or as an operation.
var reviewCommentSchema = json.RawMessage(`{"type":"object","properties":{` +
	`"body":{"type":"string","minLength":1,"maxLength":65536},` +
	`"target":{"type":"object","properties":{"kind":{"type":"string","maxLength":16,"description":"general, section, risk or file."},"id":{"type":"string","maxLength":4096},"title":{"type":"string","maxLength":1024}},"required":["kind","id","title"],"additionalProperties":false,"description":"Default: the pull request in general."},` +
	`"place":{"type":"object","properties":{"path":{"type":"string","minLength":1,"maxLength":4096},"line":{"type":"integer"},"side":{"type":"string","maxLength":8,"description":"RIGHT or LEFT."},"start_line":{"type":"integer"}},"required":["path","line","side"],"additionalProperties":false},` +
	`"head":{"type":"string","maxLength":64,"description":"The commit the place is on, as hex; empty when there is no place."}},` +
	`"required":["body"],"additionalProperties":false}`)

// pendingComment is the comment an add_review_comment operation adds, as
// the route takes it. The agent's text is refused when a summary could not
// show it as it is, and the comment when a pending review would not hold it
// (uistate.CheckReviewComment, which the route applies again on Confirm).
func pendingComment(a ReviewCommentChange) (uistate.ReviewComment, error) {
	comment := uistate.ReviewComment{Body: a.Body, Head: a.Head}
	checked := []struct{ what, text string }{{"comment", a.Body}}
	target := uistate.CommentTarget{Kind: "general", Title: "The pull request"}
	if a.Target != nil {
		target = uistate.CommentTarget{Kind: a.Target.Kind, ID: a.Target.ID, Title: a.Target.Title}
		comment.Target = &target
		checked = append(checked, struct{ what, text string }{"comment's target id", a.Target.ID}, struct{ what, text string }{"comment's target title", a.Target.Title})
	}
	if a.Place != nil {
		comment.Place = &uistate.CommentPlace{Path: a.Place.Path, Line: a.Place.Line, Side: a.Place.Side, StartLine: a.Place.StartLine}
		checked = append(checked, struct{ what, text string }{"comment's file path", a.Place.Path})
	}
	for i, c := range checked {
		if err := checkAgentText(c.what, c.text, i == 0); err != nil {
			return uistate.ReviewComment{}, err
		}
	}
	raw, err := json.Marshal(struct {
		ID      string                `json:"id"`
		Target  uistate.CommentTarget `json:"target"`
		Body    string                `json:"body"`
		Place   *uistate.CommentPlace `json:"place"`
		Head    string                `json:"head"`
		Created int64                 `json:"created"`
	}{"proposal", target, comment.Body, comment.Place, comment.Head, 0})
	if err != nil {
		return uistate.ReviewComment{}, err
	}
	if err := uistate.CheckReviewComment(raw); err != nil {
		return uistate.ReviewComment{}, errors.New(strings.TrimPrefix(err.Error(), uistate.ErrInvalid.Error()+": "))
	}
	return comment, nil
}
