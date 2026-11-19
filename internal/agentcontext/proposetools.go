package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/baum-ai/baum/internal/themes"
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
//     TargetThemeChoice (Appearance),
//     TargetReviews and TargetDecisions (Reviews), TargetFixes (Fixes),
//     TargetTimeLimits, TargetJobConcurrency, TargetCodexApprovals and
//     TargetAgentEnv+NAME (This computer), TargetPrompt+id (Prompts, the
//     global prompts).
//   - any page (ProposalAnywhere, not a page): TargetTheme, a colour theme the
//     app previews at once and the person saves into a slot from wherever
//     they are. Every page lists propose_theme.
//   - repositories, showing a repository: RepositoryTarget(repository,
//     TargetRepositorySettings) (Reviews), RepositoryTarget(repository,
//     TargetPrompt+id) (Prompts).
//   - inbox: TargetNewInbox, or TargetInbox+id for a saved inbox.
//   - group: TargetGroup+id, the group the page shows.
//   - pull_request: PullRequestTarget(repository, number), the person's
//     pending review of the pull request the page shows.
const (
	TargetAppearance         = "appearance"
	TargetThemeChoice        = "theme_choice"
	TargetTheme              = "theme"
	TargetPlanUsage          = "plan_usage/"
	TargetReviews            = "reviews"
	TargetDecisions          = "decisions"
	TargetFixes              = "fixes"
	TargetTimeLimits         = "time_limits"
	TargetJobConcurrency     = "job_concurrency"
	TargetLabs               = "labs"
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
		PageSettings:     {"propose_appearance", "propose_theme_choice", "propose_plan_usage", "propose_reviews", "propose_decision_capture", "propose_labs", "propose_fixes", "propose_time_limits", "propose_job_concurrency", "propose_codex_approvals", "propose_agent_env", "propose_prompt", "propose_prompt_reset"},
		PageRepositories: {"propose_repository_settings", "propose_prompt", "propose_prompt_reset"},
		PageInbox:        {"propose_inbox"},
		PageGroup:        {"propose_group"},
		PagePullRequest:  {"propose_review_comment"},
	}
	// settingsSectionProposals and repositorySectionProposals are the
	// propose_* tools of each section, which current_page lists with the
	// section's tools.
	settingsSectionProposals = map[string][]string{
		"general":    {"propose_appearance", "propose_plan_usage"},
		"appearance": {"propose_theme_choice"},
		"reviews":    {"propose_reviews", "propose_decision_capture", "propose_labs"},
		"fixes":      {"propose_fixes"},
		"computer":   {"propose_time_limits", "propose_job_concurrency", "propose_codex_approvals", "propose_agent_env"},
		"prompts":    {"propose_prompt", "propose_prompt_reset"},
		SectionTiles: {"propose_tile_type"},
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

// themeAnswer is what propose_theme answers: the usual receipt and status, and
// the pairs of the proposed colours that are hard to read, so the agent can
// propose again.
type themeAnswer struct {
	proposed
	ContrastWarnings []themes.Warning `json:"contrast_warnings"`
}

// themeSeedList names the seeds for propose_theme's description.
func themeSeedList() string { return strings.Join(themes.Seeds, ", ") }

func proposeThemeTool() Tool {
	return Tool{
		Name: "propose_theme",
		Description: "Proposes a custom colour theme for the baum app: a name, a base (light or dark) and colour overrides of the app's palette tokens. Works on any page. The app previews the proposal at once; nothing is saved until the person saves it, and the preview ends when they dismiss it. A new theme is saved with the name and the light or dark slot the person chooses (the slot the theme's base names unless they pick the other). " +
			"When you are changing a custom theme the person already has, pass its id as theme_id (from settings_appearance; usually the custom theme in the slot in use): saving then updates that theme in place, keeping its slots, instead of adding another, and name should be its current name unless you are renaming it. Omit theme_id when the person asks for a new theme, or when you start from a built-in theme, which cannot change: a built-in or unknown theme_id is refused. " +
			"Use settings_appearance to read the person's themes, the tokens a theme may set (theme_tokens) and what each resolves to by default (theme_defaults) in the light and dark themes. Prefer to change seeds, which the rest of the palette derives from: " + themeSeedList() + ". On a dark base the text colour comes from --color-paper (the text ramp mixes it toward --color-shadow) and the surfaces from --color-shadow, so set --color-paper to a light colour; --color-ink does not colour dark-theme text, so setting it there does nothing for text. On a light base --color-ink is the text and --color-paper the page. Set a role (the other tokens) only where a seed cannot reach. Each value is one colour: #rgb, #rrggbb, #rrggbbaa, rgb(), hsl() or oklch(). " +
			"The answer lists contrast_warnings, the pairs of text and background colours under 4.5:1 contrast; propose again with different colours to fix them.",
		InputSchema: json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{`+
			`"theme_id":{"type":"string","maxLength":64,"description":"The id of the custom theme this changes, from settings_appearance. Omit for a new theme."},`+
			`"name":{"type":"string","maxLength":%d,"description":"The theme's name, as the person will see it in their themes."},`+
			`"base":{"type":"string","enum":["light","dark"],"description":"The built-in palette the tokens change: light or dark."},`+
			`"tokens":{"type":"object","maxProperties":%d,"additionalProperties":{"type":"string","maxLength":%d},"description":"Palette token to colour, for example {\"--color-paper\":\"#fdf6e3\"}. Only the tokens settings_appearance lists in theme_tokens."}},`+
			`"required":["name","base","tokens"],"additionalProperties":false}`, themes.MaxNameRunes, len(themes.Tokens), themes.MaxValue)),
		Proposes: true,
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			var a struct {
				ThemeID string            `json:"theme_id,omitempty"`
				Name    string            `json:"name"`
				Base    string            `json:"base"`
				Tokens  map[string]string `json:"tokens"`
			}
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			if a.Tokens == nil {
				a.Tokens = map[string]string{}
			}
			theme := themes.Theme{Name: a.Name, Base: a.Base, Tokens: a.Tokens}
			if err := themes.Validate(theme); err != nil {
				return "", err
			}
			text, err := propose(ctx, c, ProposalAnywhere, TargetTheme, a)
			if err != nil {
				return "", err
			}
			var receipt proposed
			if err := json.Unmarshal([]byte(text), &receipt); err != nil {
				return "", err
			}
			warnings := themes.Contrast(theme)
			if warnings == nil {
				warnings = []themes.Warning{}
			}
			out, err := json.Marshal(themeAnswer{receipt, warnings})
			return string(out), err
		},
	}
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
		Description: "Proposes a change to the Settings page's General section appearance: whether key hints show, the modifier held with 1 to 8 for the sidebar's pages, and the review page (the full review or tiles). The theme mode and colour themes are propose_theme_choice's. The person must have Settings open at General. Nothing changes until the person saves the proposal; you get its id, never the current values.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"key_hints":{"type":"boolean"},` +
			`"page_modifier":{"type":"string","maxLength":16,"description":"meta, ctrl or alt; empty is the platform's command key."},` +
			`"review_page":{"type":"string","maxLength":16,"description":"full or tiles: the page a review opens on."}},` +
			`"minProperties":1,"additionalProperties":false}`),
		Proposes: true,
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			var a struct {
				KeyHints     *bool   `json:"key_hints,omitempty"`
				PageModifier *string `json:"page_modifier,omitempty"`
				ReviewPage   *string `json:"review_page,omitempty"`
			}
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			return propose(ctx, c, PageSettings, TargetAppearance, a)
		},
	}, {
		Name:        "propose_theme_choice",
		Description: "Proposes a change to the Settings page's Appearance section: the theme mode (theme: system, light or dark) and which colour theme the light and dark slots use (light_theme, dark_theme: theme ids from settings_appearance; to make a new theme use propose_theme). The person must have Settings open at Appearance. Nothing changes until the person saves the proposal; you get its id, never the current values.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"theme":{"type":"string","maxLength":16,"description":"system, light or dark."},` +
			`"light_theme":{"type":"string","maxLength":64,"description":"The id of the colour theme for the light slot, from settings_appearance's themes; empty is the built-in default."},` +
			`"dark_theme":{"type":"string","maxLength":64,"description":"The id of the colour theme for the dark slot, from settings_appearance's themes; empty is the built-in default."}},` +
			`"minProperties":1,"additionalProperties":false}`),
		Proposes: true,
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			var a struct {
				Theme      *string `json:"theme,omitempty"`
				LightTheme *string `json:"light_theme,omitempty"`
				DarkTheme  *string `json:"dark_theme,omitempty"`
			}
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			return propose(ctx, c, PageSettings, TargetThemeChoice, a)
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
		Description: "Proposes a change to the Settings page's Reviews section: whether automatic reviews are on, how they follow later pushes (updates: always reviews every push; on_open reviews once; on_change reviews once, then again only when the agent finds a push would change the review), for which pull requests (both: the person's own and those they were asked to review; authored; requested; all), and the harness reviews use (backend: auto, claude or codex), its model (empty for the harness's default) and effort. Only the fields given change. The person must have Settings open at Reviews. Nothing changes until the person saves the proposal.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"enabled":{"type":"boolean","description":"Automatic reviews on or off."},` +
			`"pull_requests":{"type":"string","maxLength":32,"description":"both, authored, requested or all."},` +
			`"updates":{"type":"string","maxLength":32,"description":"always, on_open or on_change."},` +
			`"backend":{"type":"string","maxLength":32,"description":"auto, claude or codex."},` +
			`"model":{"type":"string","maxLength":256},` +
			`"effort":` + effort + `},` +
			`"minProperties":1,"additionalProperties":false}`),
		Proposes: true,
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			var a struct {
				Enabled      *bool   `json:"enabled,omitempty"`
				PullRequests *string `json:"pull_requests,omitempty"`
				Updates      *string `json:"updates,omitempty"`
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
		Name:        "propose_labs",
		Description: "Proposes turning the experimental setting that generates new reviews as tiles on or off (tile_reviews), on the Settings page's Reviews section: with it on, one more short model call after each new review of a pull request arranges its tile page, and a review may finish up to 90 seconds after its time limit. The person must have Settings open at Reviews. Nothing changes until the person saves the proposal.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"tile_reviews":{"type":"boolean"}},"required":["tile_reviews"],"additionalProperties":false}`),
		Proposes:    true,
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			var a struct {
				TileReviews bool `json:"tile_reviews"`
			}
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			return propose(ctx, c, PageSettings, TargetLabs, a)
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
		Description: "Proposes a comment for the person's own pending review of the pull request the page shows, which they send from the app: its body, what it is about (target: general, or a section, risk, file or tile of the review by id, with a title), and optionally the diff line it is placed on (path, line, side RIGHT or LEFT, optional start_line) at the commit head. Saving adds it to the review's comments and keeps the person's other drafts. " +
			"The person must have the pull request's page open. Nothing changes until the person saves the proposal.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"body":{"type":"string","minLength":1,"maxLength":65536},` +
			`"target":{"type":"object","properties":{"kind":{"type":"string","maxLength":16,"description":"general, section, risk, file or tile."},"id":{"type":"string","maxLength":4096},"title":{"type":"string","maxLength":1024}},"required":["kind","id","title"],"additionalProperties":false,"description":"Default: the pull request in general."},` +
			`"place":{"type":"object","properties":{"path":{"type":"string","minLength":1,"maxLength":4096},"line":{"type":"integer"},"side":{"type":"string","maxLength":8,"description":"RIGHT or LEFT."},"start_line":{"type":"integer"}},"required":["path","line","side"],"additionalProperties":false},` +
			`"head":{"type":"string","maxLength":64,"description":"The commit the place is on, as hex; empty when there is no place."}},` +
			`"required":["body"],"additionalProperties":false}`),
		Proposes: true,
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
	}, proposeThemeTool()}
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
