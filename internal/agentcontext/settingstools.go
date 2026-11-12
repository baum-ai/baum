package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/config"
)

// The tools of the Settings page and of a repository's page. Each returns a
// typed struct of what its section shows, filled from a decoded copy of the
// routes it reads; the Settings routes are unreadable but for the exact pairs
// in projectedReads. Never returned: the agent environment's values, the
// GitHub token, a sign-in's code or address, a check's folder, detail or fix,
// a clone's folder, or the home folder.

// settingsSectionTools are the tools that read each Settings section
// (settingsSections), and repositorySectionTools those of each section of a
// repository's page (repositorySections). current_page names the open
// section's as section_tools, and a test fails when a section of the app has
// none.
var (
	settingsSectionTools = map[string][]string{
		"general":     {"settings_general"},
		"reviews":     {"settings_reviews"},
		"fixes":       {"settings_fixes"},
		"connections": {"settings_connections"},
		"computer":    {"settings_computer", "diagnostics_summary"},
		"prompts":     {"prompts", "prompt"},
	}
	repositorySectionTools = map[string][]string{
		"reviews":      {"repository_reviews"},
		"prompts":      {"repository_prompts"},
		"registration": {"repository_registration"},
	}
)

// repositoryPageTools are the tools of a repository's page, which the
// Repositories page lists only while it shows a repository.
func repositoryPageTools() []string {
	var out []string
	for _, section := range repositorySections {
		out = append(out, repositorySectionTools[section]...)
		out = append(out, repositorySectionProposals[section]...)
	}
	return out
}

// sectionTools are the tools of the page's open section, if it has one: the
// tools that read it, then those that propose changes to it.
func sectionTools(p PageContext) []string {
	switch {
	case p.Section == "":
		return nil
	case p.Page == PageSettings:
		return slices.Concat(settingsSectionTools[p.Section], settingsSectionProposals[p.Section])
	case p.Page == PageRepositories && p.Repository != "":
		return slices.Concat(repositorySectionTools[p.Section], repositorySectionProposals[p.Section])
	}
	return nil
}

// discarded decodes any JSON value and keeps none of it, so a value that must
// never be passed on (an agent environment variable's, a sign-in's code) is
// not copied into the decoded struct. The route's raw body is in memory for
// the call's length, as any read's is; it is never kept, logged or quoted in
// an error.
type discarded struct{}

func (*discarded) UnmarshalJSON([]byte) error { return nil }

// appSettings is what the Settings tools read of GET /api/settings. The
// agent environment is decoded for its names alone, and the budgets and caps
// not at all.
type appSettings struct {
	Backend         string               `json:"backend"`
	Model           string               `json:"model"`
	Effort          string               `json:"effort"`
	FixBackend      string               `json:"fix_backend"`
	FixModel        string               `json:"fix_model"`
	FixEffort       string               `json:"fix_effort"`
	JobConcurrency  int                  `json:"job_concurrency"`
	ReviewTimeLimit int                  `json:"review_time_limit_minutes"`
	RunTimeLimit    int                  `json:"run_time_limit_minutes"`
	AgentEnv        map[string]discarded `json:"agent_env"`
	AgentEnvSource  string               `json:"agent_env_source"`
}

// readSettings reads /api/settings, a projected route. What went wrong is
// never quoted: the route's own error text is not the tool's to pass on.
func readSettings(ctx context.Context, c CallContext) (appSettings, error) {
	var out appSettings
	body, _, err := c.get(ctx, "/api/settings", nil)
	if err != nil {
		return out, errors.New("the settings are unavailable now")
	}
	if json.Unmarshal(body, &out) != nil {
		return out, errors.New("the settings are not readable")
	}
	return out, nil
}

// envName is a variable name as config.ValidateAgentEnv takes one; any other
// key is left out rather than passed on.
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,255}$`)

func (s *Server) settingsTools() []Tool {
	return []Tool{{
		Name: "settings_general",
		Description: "What the Settings page's General section shows: the theme (system, light or dark), whether key hints show, the modifier of the page shortcuts (meta, ctrl or alt; empty is the platform's default), and for each plan (Claude Code, Codex) whether its usage shows in the sidebar and tray, what baum would show by default and whether the person chose. " +
			"The plan choices are worked out from what the daemon already holds; nothing is fetched.",
		Scope:     ScopeGlobal,
		Projected: []string{"/api/settings/appearance"},
		Call:      s.callSettingsGeneral,
	}, {
		Name: "settings_reviews",
		Description: "What the Settings page's Reviews section shows: whether automatic reviews are on and for which pull requests (pull_requests: both, authored, requested or all), the harness reviews use (backend), its model and effort, and decision capture: on or off, its backend, model and effort as chosen (empty follows the reviews'), and what capture resolves them to (effective_backend, effective_model, whether the backend could not be determined in time, whether the model is ignored). " +
			"Resolving an auto backend runs the harnesses' version checks on this computer, as opening the section in the app does; the answer is reused for a short while (45 seconds in the daemon) while the settings are unchanged.",
		Scope:     ScopeGlobal,
		Reads:     []string{"/api/automatic"},
		Projected: []string{"/api/settings"},
		Call:      s.callSettingsReviews,
	}, {
		Name:        "settings_fixes",
		Description: "What the Settings page's Fixes section shows: the harness fixes use (backend), its model and effort; an empty backend is the same as reviews' (same_as_reviews), whose model and effort then apply.",
		Scope:       ScopeGlobal,
		Projected:   []string{"/api/settings"},
		Call:        callSettingsFixes,
	}, {
		Name: "settings_connections",
		Description: "What the Settings page's Connections section shows. GitHub: the connection's state (connected, disconnected, pending while a sign-in waits, loading), its host (github.com, or other host), the signed-in login, where the token in use comes from (source: baum for the app's sign-in, environment or config_file) and whether that overrides the app's sign-in, whether the saved sign-in was granted workflow access, an error code and message, and the organizations the sign-in cannot reach. Never the token, nor a sign-in's code or address. " +
			"Codex hooks: the state the app last checked (trusted, untrusted, hooks_disabled, disabled, missing, unknown, not_installed) and when; it is never checked afresh, so it can be stale. " +
			"The desktop app's agent panel (Claude Code and Codex plugins, their versions and sign-in) is the desktop app's, not the daemon's, so it is not here.",
		Scope:     ScopeGlobal,
		Projected: []string{"/api/github-auth"},
		Call:      s.callSettingsConnections,
	}, {
		Name: "settings_computer",
		Description: "What the Settings page's This computer section shows: how many jobs run at once, the review and run time limits in minutes, the names of the agent environment variables (never their values) and whether the app or the config file sets them, whether the app answers Codex's permission requests (codex_approvals), and the agent setup: each harness check of the diagnostics (claude, codex, cursor-agent, backend-auto, backend-configured, anthropic-api-key) with its label, status and version, and how many problems the diagnostics found. No folders, details or fixes. " +
			"What only the desktop app knows (updates, zoom, the baum command line tool, whether each agent CLI is signed in) is not in the daemon, so it is not here.",
		Scope:     ScopeGlobal,
		Reads:     []string{"/api/agent-requests"},
		Projected: []string{"/api/settings", "/api/diagnostics"},
		Call:      callSettingsComputer,
	}, {
		Name: "prompts",
		Description: "The prompts the Settings page's Prompts section lists, or with repository that repository's page's: each prompt's id, title, group, when it is used, its built-in version, whether it is customised and from which built-in version, whether that is outdated or invalid (and why), its safety rules (id and label) and the ones a custom text dropped, and for a repository which text runs (source: default, global or repository). " +
			"Not the texts: read one prompt's with prompt. Prompts have no template variables: what a run is given (the pull request, its checks, a conversation) is appended after the prompt as data.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"repository":{"type":"string","maxLength":200,"description":"A repository baum watches, as owner/name: its prompts. Default: the global prompts."}},"additionalProperties":false}`),
		Scope:       ScopeGlobal,
		Reads:       []string{"/api/repositories"},
		Projected:   []string{"/api/settings/prompts"},
		Call:        callPrompts,
	}, {
		Name:        "prompt",
		Description: "One prompt as the Settings page's Prompts section shows it, by id (see prompts), with every field: title, group, when it is used, the built-in text and version, the custom text and the built-in version it was based on, whether it is outdated or invalid and why, its safety rules with their text and the ones the custom text dropped; with repository, that repository's custom text, the text it inherits and which text runs (source).",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"id":{"type":"string","minLength":1,"maxLength":64,"description":"The prompt's id, such as fix or review-group."},` +
			`"repository":{"type":"string","maxLength":200,"description":"A repository baum watches, as owner/name: its prompt. Default: the global one."}},"required":["id"],"additionalProperties":false}`),
		Scope:     ScopeGlobal,
		Reads:     []string{"/api/repositories"},
		Projected: []string{"/api/settings/prompts"},
		Call:      callPrompt,
	}}
}

// repositoryPageToolList are the tools of a repository's page. They read one
// repository through CallContext.Target and actionRoutes, so an action reads
// its own.
func repositoryPageToolList() []Tool {
	repository := `"repository":{"type":"string","maxLength":200,"description":"The repository as owner/name. Default: the open page's."}`
	return []Tool{{
		Name: "repository_reviews",
		Description: "What a repository page's Reviews section shows: the repository's automatic review choice (true or false, null when it follows the global switch), its decision capture choice (false when off, null when it follows the global switch), and its automatic review status: whether automatic reviews are on for it in effect, the latest reason a pull request got no automatic review (notice) and how many pull requests wait for one. " +
			"Defaults to the open page's repository.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` + repository + `},"additionalProperties":false}`),
		Scope:       ScopeRepository,
		Reads:       []string{"/api/repositories", "/api/automatic"},
		Call:        callRepositoryReviews,
	}, {
		Name: "repository_prompts",
		Description: "The prompts of a repository page's Prompts section: without id, each prompt as prompts lists it (no texts); with id, that prompt with every field as prompt gives it, including the repository's custom text, the text it inherits and which text runs (source). " +
			"An agent working on a pull request is not given the inherited text, nor the safety rules a global text dropped: those are the person's global Settings (withheld says so). " +
			"Defaults to the open page's repository.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` + repository + `,` +
			`"id":{"type":"string","minLength":1,"maxLength":64,"description":"One prompt's id, for all its fields and texts."}},"additionalProperties":false}`),
		Scope: ScopeRepository,
		Reads: []string{"/api/repositories"},
		Call:  callRepositoryPrompts,
	}, {
		Name: "repository_registration",
		Description: "What a repository page's Registration section shows: whether baum watches the repository, its local clones as a count, how many are available, each one's id and availability, and the earlier names GitHub forwards to it. No folders. " +
			"Defaults to the open page's repository.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` + repository + `},"additionalProperties":false}`),
		Scope:       ScopeRepository,
		Reads:       []string{"/api/repositories"},
		Call:        callRepositoryRegistration,
	}}
}

// planChoice is one plan of the General section's plan usage choice.
type planChoice struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
	Shown    bool   `json:"shown"`
	Default  bool   `json:"default"`
	Explicit bool   `json:"explicit"`
}

func (s *Server) callSettingsGeneral(ctx context.Context, c CallContext, _ json.RawMessage) (string, error) {
	body, _, err := c.get(ctx, "/api/settings/appearance", nil)
	if err != nil {
		return "", errors.New("the appearance settings are unavailable now")
	}
	var appearance struct {
		Theme        string `json:"theme"`
		KeyHints     bool   `json:"key_hints"`
		PageModifier string `json:"page_modifier"`
	}
	if json.Unmarshal(body, &appearance) != nil {
		return "", errors.New("the appearance settings are not readable")
	}
	out := struct {
		Theme         string       `json:"theme"`
		KeyHints      bool         `json:"key_hints"`
		PageModifier  string       `json:"page_modifier"`
		PlanUsage     []planChoice `json:"plan_usage"`
		PlanUsageNote string       `json:"plan_usage_note,omitempty"`
	}{Theme: appearance.Theme, KeyHints: appearance.KeyHints, PageModifier: appearance.PageModifier, PlanUsage: []planChoice{}}
	var choices []json.RawMessage
	if s.planChoices != nil {
		choices, err = s.planChoices(ctx)
	}
	if s.planChoices == nil || err != nil {
		out.PlanUsageNote = "The plan usage choices are unavailable now."
	}
	for _, raw := range choices {
		var p planChoice
		if json.Unmarshal(raw, &p) == nil && p.Provider != "" {
			out.PlanUsage = append(out.PlanUsage, p)
		}
	}
	return compactJSON(out)
}

func (s *Server) callSettingsReviews(ctx context.Context, c CallContext, _ json.RawMessage) (string, error) {
	settings, err := readSettings(ctx, c)
	if err != nil {
		return "", err
	}
	type automatic struct {
		Enabled      bool   `json:"enabled"`
		PullRequests string `json:"pull_requests"`
	}
	type capture struct {
		Enabled             bool   `json:"enabled"`
		Backend             string `json:"backend"`
		Model               string `json:"model"`
		Effort              string `json:"effort"`
		EffectiveBackend    string `json:"effective_backend"`
		BackendUndetermined bool   `json:"backend_undetermined"`
		EffectiveModel      string `json:"effective_model"`
		ModelIgnored        bool   `json:"model_ignored"`
	}
	out := struct {
		Automatic       *automatic `json:"automatic"`
		Backend         string     `json:"backend"`
		Model           string     `json:"model"`
		Effort          string     `json:"effort"`
		DecisionCapture *capture   `json:"decision_capture"`
		Unavailable     []string   `json:"unavailable,omitempty"`
	}{Backend: settings.Backend, Model: settings.Model, Effort: settings.Effort}
	if body, _, err := c.get(ctx, "/api/automatic", nil); err == nil && !disabled(body) {
		var a automatic
		if json.Unmarshal(body, &a) == nil {
			out.Automatic = &a
		}
	}
	if out.Automatic == nil {
		out.Unavailable = append(out.Unavailable, "automatic")
	}
	// Decision capture, as /api/settings/decisions answers it, from the
	// accessor: resolving an auto backend runs the harnesses' version checks,
	// which the accessor does at most once per short while for the same
	// preferences, however often the tool is called.
	var body json.RawMessage
	err = errors.New("unavailable")
	if s.decisionSettings != nil {
		body, err = s.decisionSettings(ctx)
	}
	if err == nil {
		var d struct {
			Decisions *capture `json:"decisions"`
		}
		if json.Unmarshal(body, &d) == nil {
			out.DecisionCapture = d.Decisions
		}
	}
	if out.DecisionCapture == nil {
		out.Unavailable = append(out.Unavailable, "decision_capture")
	}
	return compactJSON(out)
}

func callSettingsFixes(ctx context.Context, c CallContext, _ json.RawMessage) (string, error) {
	settings, err := readSettings(ctx, c)
	if err != nil {
		return "", err
	}
	return compactJSON(struct {
		Backend       string `json:"backend"`
		Model         string `json:"model"`
		Effort        string `json:"effort"`
		SameAsReviews bool   `json:"same_as_reviews"`
	}{settings.FixBackend, settings.FixModel, settings.FixEffort, settings.FixBackend == ""})
}

// tokenSourceKind is the kind of place a GitHub token comes from, never the
// place: an environment variable's name or a file's folder is not passed on.
func tokenSourceKind(source string) string {
	switch {
	case source == "":
		return ""
	case source == config.SourceBaum:
		return "baum"
	case source == config.SourceConfigFile:
		return "config_file"
	case strings.HasPrefix(source, "environment variable "):
		return "environment"
	}
	return "other"
}

func (s *Server) callSettingsConnections(ctx context.Context, c CallContext, _ json.RawMessage) (string, error) {
	type github struct {
		State         string   `json:"state"`
		Host          string   `json:"host,omitempty"`
		Login         string   `json:"login,omitempty"`
		Source        string   `json:"source,omitempty"`
		Override      bool     `json:"override"`
		Saved         bool     `json:"saved"`
		Workflow      bool     `json:"workflow"`
		Error         string   `json:"error,omitempty"`
		Message       string   `json:"message,omitempty"`
		Organizations []string `json:"organizations_not_reachable,omitempty"`
	}
	type hooks struct {
		State     string    `json:"state"`
		CheckedAt time.Time `json:"checked_at"`
	}
	out := struct {
		GitHub      *github  `json:"github"`
		CodexHooks  *hooks   `json:"codex_hooks"`
		Notes       []string `json:"notes,omitempty"`
		Unavailable []string `json:"unavailable,omitempty"`
	}{}
	// The connection, as the section reads it (a read may start the
	// manager's background check of the organizations, as opening the
	// section does). The pending sign-in is decoded only for whether there is
	// one: its code and address are not decoded, and the raw body is never
	// kept or quoted.
	if body, _, err := c.get(ctx, "/api/github-auth", nil); err == nil && !disabled(body) {
		var conn struct {
			State              string     `json:"state"`
			Host               string     `json:"host"`
			Login              string     `json:"login"`
			Source             string     `json:"source"`
			Override           bool       `json:"override"`
			Saved              bool       `json:"saved"`
			Workflow           bool       `json:"workflow"`
			Pending            *discarded `json:"pending"`
			Error              string     `json:"error"`
			Message            string     `json:"message"`
			OrganizationAccess *struct {
				Hidden []string `json:"hidden"`
			} `json:"organization_access"`
		}
		if json.Unmarshal(body, &conn) == nil {
			g := github{State: conn.State, Login: conn.Login, Source: tokenSourceKind(conn.Source), Override: conn.Override, Saved: conn.Saved, Workflow: conn.Workflow,
				Error: scrubText(conn.Error), Message: scrubText(conn.Message)}
			// A GitHub Enterprise host names the person's employer.
			switch conn.Host {
			case "", "github.com":
				g.Host = conn.Host
			default:
				g.Host = "other host"
			}
			if conn.Pending != nil {
				g.State = "pending"
			}
			if conn.OrganizationAccess != nil {
				g.Organizations = slices.Clone(conn.OrganizationAccess.Hidden)
			}
			out.GitHub = &g
		}
	}
	if out.GitHub == nil {
		out.Unavailable = append(out.Unavailable, "github")
	}
	// The Codex hooks: what the app last saw, never a new check, which starts
	// Codex processes.
	if s.codexHooks != nil {
		if state, at, ok := s.codexHooks(); ok {
			out.CodexHooks = &hooks{State: state, CheckedAt: at.UTC().Truncate(time.Second)}
		} else {
			out.Notes = append(out.Notes, "The app has not checked the Codex hooks since the daemon started.")
		}
	} else {
		out.Unavailable = append(out.Unavailable, "codex_hooks")
	}
	return compactJSON(out)
}

// agentCheckIDs are the diagnostics' checks of the agents and harnesses:
// what This computer's agent setup is.
var agentCheckIDs = []string{"claude", "codex", "cursor-agent", "backend-auto", "backend-configured", "anthropic-api-key"}

func callSettingsComputer(ctx context.Context, c CallContext, _ json.RawMessage) (string, error) {
	settings, err := readSettings(ctx, c)
	if err != nil {
		return "", err
	}
	type agentCheck struct {
		ID      string `json:"id"`
		Label   string `json:"label"`
		Status  string `json:"status"`
		Version string `json:"version,omitempty"`
	}
	out := struct {
		JobConcurrency      int          `json:"job_concurrency"`
		ReviewTimeLimit     int          `json:"review_time_limit_minutes"`
		RunTimeLimit        int          `json:"run_time_limit_minutes"`
		AgentEnvNames       []string     `json:"agent_env_names"`
		AgentEnvSource      string       `json:"agent_env_source,omitempty"`
		CodexApprovals      *bool        `json:"codex_approvals"`
		AgentSetup          []agentCheck `json:"agent_setup"`
		DiagnosticsStatus   string       `json:"diagnostics_status,omitempty"`
		DiagnosticsProblems int          `json:"diagnostics_problems"`
		Unavailable         []string     `json:"unavailable,omitempty"`
	}{JobConcurrency: settings.JobConcurrency, ReviewTimeLimit: settings.ReviewTimeLimit, RunTimeLimit: settings.RunTimeLimit,
		AgentEnvNames: []string{}, AgentEnvSource: settings.AgentEnvSource, AgentSetup: []agentCheck{}}
	for name := range settings.AgentEnv {
		if envName.MatchString(name) {
			out.AgentEnvNames = append(out.AgentEnvNames, name)
		}
	}
	slices.Sort(out.AgentEnvNames)
	// The agents' requests are read for the person's setting alone.
	if body, _, err := c.get(ctx, "/api/agent-requests", nil); err == nil && !disabled(body) {
		var requests struct {
			Settings *struct {
				CodexApprovals bool `json:"codex_approvals"`
			} `json:"settings"`
		}
		if json.Unmarshal(body, &requests) == nil && requests.Settings != nil {
			out.CodexApprovals = &requests.Settings.CodexApprovals
		}
	}
	if out.CodexApprovals == nil {
		out.Unavailable = append(out.Unavailable, "codex_approvals")
	}
	// The agent setup is the diagnostics' agent checks, scrubbed as
	// diagnostics_summary scrubs them, without where each was found.
	if status, checks, err := readDiagnostics(ctx, c); err == nil {
		out.DiagnosticsStatus = status
		for _, k := range checks {
			if k.Status == "problem" {
				out.DiagnosticsProblems++
			}
			if slices.Contains(agentCheckIDs, k.ID) {
				out.AgentSetup = append(out.AgentSetup, agentCheck{k.ID, k.Label, k.Status, k.Version})
			}
		}
	} else {
		out.Unavailable = append(out.Unavailable, "agent_setup")
	}
	return compactJSON(out)
}

// promptRule is a prompt's safety rule; its text only in a full entry.
type promptRule struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Text  string `json:"text,omitempty"`
}

// promptEntry is a prompt as the Prompts sections show it: every field of
// repos.PromptEntry.
type promptEntry struct {
	ID                 string       `json:"id"`
	Title              string       `json:"title"`
	Group              string       `json:"group"`
	UsedBy             string       `json:"used_by"`
	DefaultText        string       `json:"default_text"`
	DefaultVersion     string       `json:"default_version"`
	CustomText         *string      `json:"custom_text"`
	BasedOn            *string      `json:"based_on"`
	Outdated           bool         `json:"outdated"`
	Invalid            bool         `json:"invalid"`
	InvalidReason      *string      `json:"invalid_reason"`
	SafetyRules        []promptRule `json:"safety_rules"`
	MissingSafetyRules []string     `json:"missing_safety_rules"`
	Source             string       `json:"source,omitempty"`
	InheritedText      *string      `json:"inherited_text,omitempty"`
	// Withheld says what an action was not given (withheldFromActions).
	Withheld string `json:"withheld,omitempty"`
}

// withheldNote is what an action is told of the fields it does not get.
const withheldNote = "inherited_text, and missing_safety_rules where the global text runs, describe the person's global Settings, which an action does not read"

// withheldFromActions is entries as an action may read them: a repository's
// prompts carry the person's global custom text as inherited_text, and for a
// prompt that runs the global text (source global) the safety rules that text
// dropped. Those are Settings, which an action never reads, so they are left
// out (missing_safety_rules is null, not empty, so it does not read as none
// missing).
func withheldFromActions(entries []promptEntry) []promptEntry {
	out := make([]promptEntry, 0, len(entries))
	for _, e := range entries {
		e.InheritedText, e.Withheld = nil, withheldNote
		if e.Source == "global" {
			e.MissingSafetyRules = nil
		}
		out = append(out, e)
	}
	return out
}

// promptSummary is a prompt as prompts lists it: no texts.
type promptSummary struct {
	ID                 string       `json:"id"`
	Title              string       `json:"title"`
	Group              string       `json:"group"`
	UsedBy             string       `json:"used_by"`
	DefaultVersion     string       `json:"default_version"`
	Custom             bool         `json:"custom"`
	BasedOn            *string      `json:"based_on"`
	Outdated           bool         `json:"outdated"`
	Invalid            bool         `json:"invalid"`
	InvalidReason      *string      `json:"invalid_reason"`
	SafetyRules        []promptRule `json:"safety_rules"`
	MissingSafetyRules []string     `json:"missing_safety_rules"`
	Source             string       `json:"source,omitempty"`
	Withheld           string       `json:"withheld,omitempty"`
}

func (e promptEntry) summary() promptSummary {
	out := promptSummary{ID: e.ID, Title: e.Title, Group: e.Group, UsedBy: e.UsedBy, DefaultVersion: e.DefaultVersion, Custom: e.CustomText != nil,
		BasedOn: e.BasedOn, Outdated: e.Outdated, Invalid: e.Invalid, InvalidReason: e.InvalidReason, SafetyRules: []promptRule{},
		MissingSafetyRules: e.MissingSafetyRules, Source: e.Source, Withheld: e.Withheld}
	for _, r := range e.SafetyRules {
		out.SafetyRules = append(out.SafetyRules, promptRule{ID: r.ID, Label: r.Label})
	}
	return out
}

// readPrompts reads the global prompts (repository "") or a repository's.
func readPrompts(ctx context.Context, c CallContext, repository string) ([]promptEntry, error) {
	path := "/api/settings/prompts"
	if repository != "" {
		if !validRepository(repository) {
			return nil, errors.New("name the repository as owner/name")
		}
		owner, name, _ := strings.Cut(repository, "/")
		path = "/api/repositories/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/prompts"
	}
	body, missing, err := c.get(ctx, path, nil)
	switch {
	case errors.Is(err, errOtherRepository):
		return nil, err
	case err != nil:
		return nil, errors.New("the prompts are unavailable now")
	case missing:
		return nil, errors.New(repository + " is not a repository baum watches")
	}
	var answer struct {
		Prompts []promptEntry `json:"prompts"`
	}
	if json.Unmarshal(body, &answer) != nil {
		return nil, errors.New("the prompts are not readable")
	}
	for i, e := range answer.Prompts {
		if e.MissingSafetyRules == nil {
			answer.Prompts[i].MissingSafetyRules = []string{}
		}
		if e.SafetyRules == nil {
			answer.Prompts[i].SafetyRules = []promptRule{}
		}
	}
	return answer.Prompts, nil
}

func promptList(entries []promptEntry) (string, error) {
	out := []promptSummary{}
	for _, e := range entries {
		out = append(out, e.summary())
	}
	return compactJSON(map[string]any{"prompts": out})
}

func onePrompt(entries []promptEntry, id string) (string, error) {
	var ids []string
	for _, e := range entries {
		if e.ID == id {
			return compactJSON(e)
		}
		ids = append(ids, e.ID)
	}
	return "", fmt.Errorf("no prompt %q; the prompts are %s", id, strings.Join(ids, ", "))
}

func callPrompts(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		Repository string `json:"repository"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	entries, err := readPrompts(ctx, c, strings.TrimSpace(a.Repository))
	if err != nil {
		return "", err
	}
	return promptList(entries)
}

func callPrompt(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		ID         string `json:"id"`
		Repository string `json:"repository"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	entries, err := readPrompts(ctx, c, strings.TrimSpace(a.Repository))
	if err != nil {
		return "", err
	}
	return onePrompt(entries, a.ID)
}

func callRepositoryPrompts(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		Repository string `json:"repository"`
		ID         string `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	repository, _, err := c.Target(a.Repository, 0)
	if err != nil {
		return "", err
	}
	entries, err := readPrompts(ctx, c, repository)
	if err != nil {
		return "", err
	}
	if c.Consumer.Kind == Action {
		entries = withheldFromActions(entries)
	}
	if a.ID != "" {
		return onePrompt(entries, a.ID)
	}
	return promptList(entries)
}

// registeredRow is what a repository's page reads of its /api/repositories
// row; filterRepositories gives an action's reader the same fields.
type registeredRow struct {
	Repository string `json:"repository"`
	Settings   struct {
		Automatic *bool `json:"automatic"`
		Decisions *bool `json:"decisions"`
	} `json:"settings"`
	Clones []struct {
		ID        string `json:"id"`
		Available bool   `json:"available"`
	} `json:"clones"`
	EarlierNames []string `json:"earlier_names"`
}

// registered reads the row of repository in /api/repositories; ok is false
// when baum does not watch it.
func registered(ctx context.Context, c CallContext, repository string) (registeredRow, bool, error) {
	body, _, err := c.get(ctx, "/api/repositories", nil)
	if err != nil {
		if errors.Is(err, errOtherRepository) {
			return registeredRow{}, false, err
		}
		return registeredRow{}, false, errors.New("the repositories are unavailable now")
	}
	var list []registeredRow
	if disabled(body) || json.Unmarshal(body, &list) != nil {
		return registeredRow{}, false, errors.New("the repositories are not readable")
	}
	for _, r := range list {
		if strings.EqualFold(r.Repository, repository) {
			return r, true, nil
		}
	}
	return registeredRow{}, false, nil
}

// automaticRow is a repository's row of /api/automatic.
type automaticRow struct {
	Repository string `json:"repository"`
	Enabled    bool   `json:"enabled"`
	Notice     string `json:"notice,omitempty"`
	Pending    int    `json:"pending"`
}

func callRepositoryReviews(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		Repository string `json:"repository"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	repository, _, err := c.Target(a.Repository, 0)
	if err != nil {
		return "", err
	}
	row, ok, err := registered(ctx, c, repository)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New(repository + " is not a repository baum watches")
	}
	type status struct {
		Enabled bool   `json:"enabled"`
		Notice  string `json:"notice,omitempty"`
		Pending int    `json:"pending"`
	}
	out := struct {
		Repository      string   `json:"repository"`
		Automatic       *bool    `json:"automatic"`
		Decisions       *bool    `json:"decisions"`
		AutomaticStatus *status  `json:"automatic_status"`
		Unavailable     []string `json:"unavailable,omitempty"`
	}{Repository: row.Repository, Automatic: row.Settings.Automatic, Decisions: row.Settings.Decisions}
	body, _, err := c.get(ctx, "/api/automatic", nil)
	var overview struct {
		Repositories []automaticRow `json:"repositories"`
	}
	if err != nil || disabled(body) || json.Unmarshal(body, &overview) != nil {
		out.Unavailable = append(out.Unavailable, "automatic_status")
	}
	for _, r := range overview.Repositories {
		if strings.EqualFold(r.Repository, repository) {
			out.AutomaticStatus = &status{r.Enabled, scrubText(r.Notice), r.Pending}
		}
	}
	return compactJSON(out)
}

func callRepositoryRegistration(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		Repository string `json:"repository"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	repository, _, err := c.Target(a.Repository, 0)
	if err != nil {
		return "", err
	}
	row, ok, err := registered(ctx, c, repository)
	if err != nil {
		return "", err
	}
	type clone struct {
		ID        string `json:"id"`
		Available bool   `json:"available"`
	}
	out := struct {
		Repository   string   `json:"repository"`
		Registered   bool     `json:"registered"`
		Clones       int      `json:"clone_count"`
		Available    int      `json:"available_clones"`
		CloneList    []clone  `json:"clones"`
		EarlierNames []string `json:"earlier_names,omitempty"`
	}{Repository: repository, Registered: ok, CloneList: []clone{}}
	if ok {
		out.Repository, out.EarlierNames = row.Repository, row.EarlierNames
	}
	for _, k := range row.Clones {
		out.Clones++
		out.CloneList = append(out.CloneList, clone{k.ID, k.Available})
		if k.Available {
			out.Available++
		}
	}
	return compactJSON(out)
}
