package uiserver

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/automatic"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/inboxes"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewgroups"
	"github.com/baum-ai/baum/internal/uistate"
)

// Proposers take the changes a chat proposes through the MCP server, one
// agentcontext.Proposer for each page that saves something (Pages). A
// proposal's save is the page's own save: the same check of the input, the
// same change through the same function its route calls, and what the route
// does after a save. Nothing is saved until the person saves the proposal.
//
// Each target's revision is a keyed hash (HMAC-SHA256 with a key made per
// daemon) of the stored state the target's save reads, or its store's own
// revision (inboxes, the pending review record). Current reads it; Apply
// compares it again inside the transaction that writes, so a change that
// lands between the proposal and its save, or between Save's check and the
// write, refuses the save with agentcontext.ErrStale and writes nothing. The
// key keeps a revision from telling anything of the state it covers, which
// for the agent environment includes the values.
type Proposers struct {
	// API is the daemon's LocalAPI: the registry, the config file and what
	// its routes do after a save.
	API LocalAPI
	// Automatic saves the automatic review and fix choices
	// (PUT /api/automatic); nil takes no such proposal.
	Automatic *automatic.Scheduler
	// CodexApprovals reads whether the app answers Codex's permission
	// requests, and SetCodexApprovals saves it as
	// PUT /api/agent-requests/settings does when check accepts the setting in
	// force, the check and the save one step (agentrequests.Store's Settings
	// and SetSettingsIf). Nil takes no such proposal.
	CodexApprovals    func() bool
	SetCodexApprovals func(on bool, check func(current bool) error) error
	// Inboxes saves the saved inboxes (PUT /api/inboxes); nil takes no
	// inbox proposal.
	Inboxes *inboxes.API
	// Groups saves review groups (PATCH /api/groups/{id}); nil takes no
	// group proposal.
	Groups *reviewgroups.API
	// UIState keeps the person's pending reviews; nil takes no review
	// comment proposal.
	UIState *uistate.Store

	// beforeWrite, a test hook, runs where a save that reads outside its
	// store's transaction (an inbox, a pending review) has read and not yet
	// written, as a change landing then would.
	beforeWrite func()

	once     sync.Once
	key      []byte
	client   string
	sequence atomic.Uint64
}

// Pages are the proposers by page name, for agentcontext.Options.Proposers.
func (p *Proposers) Pages() map[string]agentcontext.Proposer {
	return map[string]agentcontext.Proposer{
		agentcontext.PageSettings:     pageProposer{p, p.settingsTarget},
		agentcontext.PageRepositories: pageProposer{p, p.repositoryTarget},
		agentcontext.PageInbox:        pageProposer{p, p.inboxTarget},
		agentcontext.PageGroup:        pageProposer{p, p.groupTarget},
		agentcontext.PagePullRequest:  pageProposer{p, p.reviewCommentTarget},
	}
}

func (p *Proposers) init() {
	p.once.Do(func() {
		p.key = make([]byte, 32)
		client := make([]byte, 16)
		if _, err := rand.Read(p.key); err != nil {
			panic(err)
		}
		if _, err := rand.Read(client); err != nil {
			panic(err)
		}
		p.client = "proposal" + hex.EncodeToString(client)
	})
}

// revision is the keyed hash of a target's state.
func (p *Proposers) revision(state any) string {
	p.init()
	raw, err := json.Marshal(state)
	if err != nil {
		// Every state is plain data; one that does not encode matches no
		// revision, so nothing is saved over it.
		return "unencodable"
	}
	mac := hmac.New(sha256.New, p.key)
	mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil))
}

// mutation is a ui-state mutation id of the proposals' own client.
func (p *Proposers) mutation() string {
	p.init()
	return fmt.Sprintf("%s:%d", p.client, p.sequence.Add(1))
}

// target is one thing a page takes proposals for.
type target struct {
	// read returns the value the person compares with (never sent to the
	// agent) and the state the revision covers.
	read func(ctx context.Context) (value, state any, err error)
	// validate refuses an After the page would not save, with the page's
	// own reason.
	validate func(ctx context.Context, after json.RawMessage) error
	// apply saves After through the page's own save. unchanged refuses a
	// state that is not the one the proposal was read at; apply calls it on
	// the state the writing transaction reads.
	apply func(ctx context.Context, after json.RawMessage, unchanged func(state any) error, input agentcontext.SaveInput) error
	// takesValue marks the one target whose save takes a value the person
	// types (an agent environment variable's).
	takesValue bool
}

// pageProposer is a page's Proposer: resolve finds the target a proposal
// names, and refuses one that is not on the page as it is open.
type pageProposer struct {
	p       *Proposers
	resolve func(page agentcontext.PageContext, name string) (*target, error)
}

func (pp pageProposer) Current(ctx context.Context, page agentcontext.PageContext, name string) (json.RawMessage, string, error) {
	t, err := pp.resolve(page, name)
	if err != nil {
		return nil, "", err
	}
	value, state, err := t.read(ctx)
	if err != nil {
		return nil, "", err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	return raw, pp.p.revision(state), nil
}

func (pp pageProposer) Validate(ctx context.Context, page agentcontext.PageContext, proposal agentcontext.Proposal) error {
	t, err := pp.resolve(page, proposal.Target)
	if err != nil {
		return err
	}
	return t.validate(ctx, proposal.After)
}

var errNoValue = errors.New("this proposal takes no value")

func (pp pageProposer) Apply(ctx context.Context, page agentcontext.PageContext, proposal agentcontext.Proposal, revision string, input agentcontext.SaveInput) error {
	t, err := pp.resolve(page, proposal.Target)
	if err != nil {
		return err
	}
	if input.Value != nil && !t.takesValue {
		return errNoValue
	}
	return t.apply(ctx, proposal.After, func(state any) error {
		if pp.p.revision(state) != revision {
			return agentcontext.ErrStale
		}
		return nil
	}, input)
}

// decodeAfter decodes a proposal's After strictly: only the fields the
// target takes, and one JSON value.
func decodeAfter(after json.RawMessage, v any) error {
	d := json.NewDecoder(bytes.NewReader(after))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.More() {
		return errors.New("the proposal is not a change this page takes")
	}
	return nil
}

// afterKeys are the keys After sets, so a null is told from a key left out.
func afterKeys(after json.RawMessage) map[string]bool {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(after, &fields)
	out := map[string]bool{}
	for k := range fields {
		out[k] = true
	}
	return out
}

// The Settings page's targets, by the section that shows each (the app's
// settingsSections; an open page without a section shows General).
var settingsTargetSections = []struct{ prefix, section string }{
	{agentcontext.TargetAppearance, "general"},
	{agentcontext.TargetPlanUsage, "general"},
	{agentcontext.TargetReviews, "reviews"},
	{agentcontext.TargetDecisions, "reviews"},
	{agentcontext.TargetFixes, "fixes"},
	{agentcontext.TargetTimeLimits, "computer"},
	{agentcontext.TargetJobConcurrency, "computer"},
	{agentcontext.TargetCodexApprovals, "computer"},
	{agentcontext.TargetAgentEnv, "computer"},
	{agentcontext.TargetPrompt, "prompts"},
}

var sectionTitles = map[string]string{"general": "General", "reviews": "Reviews", "fixes": "Fixes", "connections": "Connections",
	"computer": "This computer", "prompts": "Prompts", "registration": "Registration"}

// checkSection refuses a target of another section than the one open, which
// is open when unnamed.
func checkSection(page agentcontext.PageContext, want, unnamed, where string) error {
	open := page.Section
	if open == "" {
		open = unnamed
	}
	if open != want {
		return fmt.Errorf("this is a change to %s's %s section, and the person has its %s section open: a proposal is for the section they see", where, sectionTitles[want], sectionTitles[open])
	}
	return nil
}

func (p *Proposers) settingsTarget(page agentcontext.PageContext, name string) (*target, error) {
	section := ""
	for _, s := range settingsTargetSections {
		if name == s.prefix || (strings.HasSuffix(s.prefix, "/") && strings.HasPrefix(name, s.prefix) && len(name) > len(s.prefix)) {
			section = s.section
			break
		}
	}
	if section == "" {
		return nil, fmt.Errorf("Settings has no setting %q that takes proposals", name)
	}
	if err := checkSection(page, section, "general", "Settings"); err != nil {
		return nil, err
	}
	switch {
	case name == agentcontext.TargetAppearance:
		return p.appearanceTarget(), nil
	case strings.HasPrefix(name, agentcontext.TargetPlanUsage):
		return p.planUsageTarget(strings.TrimPrefix(name, agentcontext.TargetPlanUsage)), nil
	case name == agentcontext.TargetReviews:
		return p.automaticTarget(false)
	case name == agentcontext.TargetFixes:
		return p.automaticTarget(true)
	case name == agentcontext.TargetDecisions:
		return p.decisionsTarget(), nil
	case name == agentcontext.TargetTimeLimits:
		return p.timeLimitsTarget(), nil
	case name == agentcontext.TargetJobConcurrency:
		return p.jobConcurrencyTarget(), nil
	case name == agentcontext.TargetCodexApprovals:
		return p.codexApprovalsTarget()
	case strings.HasPrefix(name, agentcontext.TargetAgentEnv):
		return p.agentEnvTarget(strings.TrimPrefix(name, agentcontext.TargetAgentEnv)), nil
	default:
		return p.promptTarget(repos.GlobalPrompts, strings.TrimPrefix(name, agentcontext.TargetPrompt))
	}
}

// prefsTarget is a target kept in the registry's preferences: value and
// state project the preferences, validate checks an After against the
// preferences as they are, and save saves it through the route's own save,
// calling check on the preferences its transaction reads.
func (p *Proposers) prefsTarget(value, state func(repos.Preferences) any, validate func(json.RawMessage, repos.Preferences) error,
	save func(ctx context.Context, after json.RawMessage, check func(repos.Preferences) error) error) *target {
	read := func(ctx context.Context) (repos.Preferences, error) {
		prefs, err := p.API.Registry.Preferences(ctx)
		if err != nil {
			return prefs, errors.New("the settings could not be read")
		}
		return prefs, nil
	}
	return &target{
		read: func(ctx context.Context) (any, any, error) {
			prefs, err := read(ctx)
			if err != nil {
				return nil, nil, err
			}
			return value(prefs), state(prefs), nil
		},
		validate: func(ctx context.Context, after json.RawMessage) error {
			prefs, err := read(ctx)
			if err != nil {
				return err
			}
			return validate(after, prefs)
		},
		apply: func(ctx context.Context, after json.RawMessage, unchanged func(any) error, _ agentcontext.SaveInput) error {
			return save(ctx, after, func(prefs repos.Preferences) error { return unchanged(state(prefs)) })
		},
	}
}

func (p *Proposers) appearanceTarget() *target {
	decode := func(after json.RawMessage) (appearanceInput, error) {
		var in appearanceInput
		if err := decodeAfter(after, &in); err != nil {
			return in, err
		}
		if in == (appearanceInput{}) {
			return in, errors.New("propose a theme, key_hints or page_modifier")
		}
		return in, in.check()
	}
	return p.prefsTarget(
		func(prefs repos.Preferences) any { return appearanceOf(prefs) },
		func(prefs repos.Preferences) any { return prefs.Appearance },
		func(after json.RawMessage, _ repos.Preferences) error { _, err := decode(after); return err },
		func(ctx context.Context, after json.RawMessage, check func(repos.Preferences) error) error {
			in, err := decode(after)
			if err != nil {
				return err
			}
			_, err = p.API.updatePreferences(ctx, check, in.edit)
			return err
		})
}

// planShown is one plan's choice: shown or hidden, nil following baum's
// backends.
type planShown struct {
	Shown *bool `json:"shown"`
}

func (p *Proposers) planUsageTarget(provider string) *target {
	decode := func(after json.RawMessage) (*bool, error) {
		var in planShown
		if err := decodeAfter(after, &in); err != nil {
			return nil, err
		}
		if !afterKeys(after)["shown"] {
			return nil, errors.New("shown is required: true, false, or null to follow the harnesses in use")
		}
		return in.Shown, checkPlanProvider(provider)
	}
	shown := func(prefs repos.Preferences) any {
		if v, ok := prefs.PlanUsage.Show[provider]; ok {
			return planShown{&v}
		}
		return planShown{}
	}
	return p.prefsTarget(shown, shown,
		func(after json.RawMessage, _ repos.Preferences) error { _, err := decode(after); return err },
		func(ctx context.Context, after json.RawMessage, check func(repos.Preferences) error) error {
			value, err := decode(after)
			if err != nil {
				return err
			}
			_, err = p.API.updatePreferences(ctx, check, func(prefs repos.Preferences) repos.Preferences {
				return setPlanShown(prefs, provider, value)
			})
			return err
		})
}

// automaticTarget is the Reviews section's automatic review choices, or with
// fixes the Fixes section's harness for fixes: both are saved by
// PUT /api/automatic (Scheduler.SavePreferences).
func (p *Proposers) automaticTarget(fixes bool) (*target, error) {
	if p.Automatic == nil {
		return nil, errors.New("automatic reviews are not available in this daemon")
	}
	type reviewsChange struct {
		Enabled      *bool   `json:"enabled,omitempty"`
		PullRequests *string `json:"pull_requests,omitempty"`
		Backend      *string `json:"backend,omitempty"`
		Model        *string `json:"model,omitempty"`
		Effort       *string `json:"effort,omitempty"`
	}
	type fixesChange struct {
		Backend *string `json:"backend,omitempty"`
		Model   *string `json:"model,omitempty"`
		Effort  *string `json:"effort,omitempty"`
	}
	decode := func(after json.RawMessage) (automatic.PreferencesInput, error) {
		if fixes {
			var in fixesChange
			if err := decodeAfter(after, &in); err != nil {
				return automatic.PreferencesInput{}, err
			}
			if in == (fixesChange{}) {
				return automatic.PreferencesInput{}, errors.New("propose a backend, model or effort")
			}
			return automatic.PreferencesInput{FixBackend: in.Backend, FixModel: in.Model, FixEffort: in.Effort}, nil
		}
		var in reviewsChange
		if err := decodeAfter(after, &in); err != nil {
			return automatic.PreferencesInput{}, err
		}
		if in == (reviewsChange{}) {
			return automatic.PreferencesInput{}, errors.New("propose enabled, pull_requests, backend, model or effort")
		}
		return automatic.PreferencesInput{Enabled: in.Enabled, PullRequests: in.PullRequests, Backend: in.Backend, Model: in.Model, Effort: in.Effort}, nil
	}
	type reviewsState struct {
		Automatic    bool
		PullRequests string
		Backend      *config.Backend
		Model        *string
		Effort       *string
	}
	type fixesState struct {
		Backend *config.Backend
		Model   *string
		Effort  *string
	}
	state := func(prefs repos.Preferences) any {
		if fixes {
			return fixesState{prefs.FixBackend, prefs.FixModel, prefs.FixEffort}
		}
		return reviewsState{prefs.Automatic, prefs.PullRequests, prefs.Backend, prefs.Model, prefs.Effort}
	}
	value := func(prefs repos.Preferences) any {
		if fixes {
			out := struct {
				Backend string `json:"backend"`
				Model   string `json:"model"`
				Effort  string `json:"effort"`
			}{}
			if prefs.FixBackend != nil {
				out.Backend = string(*prefs.FixBackend)
			}
			if prefs.FixModel != nil {
				out.Model = *prefs.FixModel
			}
			if prefs.FixEffort != nil {
				out.Effort = *prefs.FixEffort
			}
			return out
		}
		cfg := prefs.Resolve(p.API.Config)
		pullRequests := prefs.PullRequests
		if pullRequests == "" {
			pullRequests = "both"
		}
		return struct {
			Enabled      bool   `json:"enabled"`
			PullRequests string `json:"pull_requests"`
			Backend      string `json:"backend"`
			Model        string `json:"model"`
			Effort       string `json:"effort"`
		}{prefs.Automatic, pullRequests, string(cfg.Backend), cfg.Model, cfg.Effort}
	}
	return p.prefsTarget(value, state,
		func(after json.RawMessage, prefs repos.Preferences) error {
			in, err := decode(after)
			if err != nil {
				return err
			}
			_, err = automatic.EditPreferences(prefs, in)
			return err
		},
		func(ctx context.Context, after json.RawMessage, check func(repos.Preferences) error) error {
			in, err := decode(after)
			if err != nil {
				return err
			}
			return p.Automatic.SavePreferences(ctx, in, check)
		}), nil
}

// decisionsTarget is decision capture, on the Reviews section. The page
// sends its whole state on each save, so the fields an After leaves out are
// the ones the page shows.
func (p *Proposers) decisionsTarget() *target {
	type change struct {
		Enabled *bool   `json:"enabled,omitempty"`
		Backend *string `json:"backend,omitempty"`
		Model   *string `json:"model,omitempty"`
		Effort  *string `json:"effort,omitempty"`
	}
	input := func(after json.RawMessage, prefs repos.Preferences) (decisionsInput, error) {
		var in change
		if err := decodeAfter(after, &in); err != nil {
			return decisionsInput{}, err
		}
		if in == (change{}) {
			return decisionsInput{}, errors.New("propose enabled, backend, model or effort")
		}
		current := decisionSettingsOf(prefs)
		out := decisionsInput{Enabled: &current.Enabled, Backend: &current.Backend, Model: &current.Model, Effort: &current.Effort}
		if in.Enabled != nil {
			out.Enabled = in.Enabled
		}
		if in.Backend != nil {
			out.Backend = in.Backend
		}
		if in.Model != nil {
			out.Model = in.Model
		}
		if in.Effort != nil {
			out.Effort = in.Effort
		}
		return out, nil
	}
	return p.prefsTarget(
		func(prefs repos.Preferences) any { return decisionSettingsOf(prefs) },
		func(prefs repos.Preferences) any { return prefs.Decisions },
		func(after json.RawMessage, prefs repos.Preferences) error {
			in, err := input(after, prefs)
			if err != nil {
				return err
			}
			_, err = in.edit(prefs)
			return err
		},
		func(ctx context.Context, after json.RawMessage, check func(repos.Preferences) error) error {
			_, err := p.API.Registry.UpdatePreferencesIf(ctx, func(prefs repos.Preferences) (repos.Preferences, error) {
				if err := check(prefs); err != nil {
					return prefs, err
				}
				in, err := input(after, prefs)
				if err != nil {
					return prefs, err
				}
				return in.edit(prefs)
			})
			if err != nil {
				return err
			}
			p.API.decisionsChanged()
			return nil
		})
}

// timeLimits are the time limits in force, in whole minutes, as the This
// computer section shows and sends them.
type timeLimits struct {
	Review *int `json:"review_time_limit_minutes,omitempty"`
	Run    *int `json:"run_time_limit_minutes,omitempty"`
}

func (p *Proposers) timeLimitsTarget() *target {
	inForce := func(prefs repos.Preferences) (int, int) {
		budgets := prefs.Resolve(p.API.Config).Budgets
		return minutes(budgets.ReviewTimeout), minutes(budgets.RunTimeout)
	}
	// The section sends both limits; one the After leaves out is the one in
	// force.
	limits := func(after json.RawMessage, prefs repos.Preferences) (int, int, error) {
		var in timeLimits
		if err := decodeAfter(after, &in); err != nil {
			return 0, 0, err
		}
		if in.Review == nil && in.Run == nil {
			return 0, 0, errors.New("propose review_time_limit_minutes, run_time_limit_minutes or both")
		}
		review, run := inForce(prefs)
		if in.Review != nil {
			review = *in.Review
		}
		if in.Run != nil {
			run = *in.Run
		}
		return review, run, checkTimeLimits(review, run)
	}
	return p.prefsTarget(
		func(prefs repos.Preferences) any { review, run := inForce(prefs); return timeLimits{&review, &run} },
		func(prefs repos.Preferences) any { return [2]int{prefs.ReviewTimeLimit, prefs.RunTimeLimit} },
		func(after json.RawMessage, prefs repos.Preferences) error {
			_, _, err := limits(after, prefs)
			return err
		},
		func(ctx context.Context, after json.RawMessage, check func(repos.Preferences) error) error {
			_, err := p.API.Registry.UpdatePreferencesIf(ctx, func(prefs repos.Preferences) (repos.Preferences, error) {
				if err := check(prefs); err != nil {
					return prefs, err
				}
				review, run, err := limits(after, prefs)
				if err != nil {
					return prefs, err
				}
				return setTimeLimits(prefs, review, run), nil
			})
			return err
		})
}

func (p *Proposers) jobConcurrencyTarget() *target {
	type change struct {
		JobConcurrency *int `json:"job_concurrency"`
	}
	decode := func(after json.RawMessage) (int, error) {
		var in change
		if err := decodeAfter(after, &in); err != nil {
			return 0, err
		}
		if in.JobConcurrency == nil {
			return 0, errors.New("job_concurrency is required")
		}
		return *in.JobConcurrency, checkJobConcurrency(*in.JobConcurrency)
	}
	return p.prefsTarget(
		func(prefs repos.Preferences) any { return change{new(prefs.Concurrency())} },
		func(prefs repos.Preferences) any { return prefs.JobConcurrency },
		func(after json.RawMessage, _ repos.Preferences) error { _, err := decode(after); return err },
		func(ctx context.Context, after json.RawMessage, check func(repos.Preferences) error) error {
			n, err := decode(after)
			if err != nil {
				return err
			}
			_, err = p.API.saveJobConcurrency(ctx, n, check)
			return err
		})
}

func (p *Proposers) codexApprovalsTarget() (*target, error) {
	if p.CodexApprovals == nil || p.SetCodexApprovals == nil {
		return nil, errors.New("agent approvals are not available in this daemon")
	}
	type change struct {
		CodexApprovals *bool `json:"codex_approvals"`
	}
	decode := func(after json.RawMessage) (bool, error) {
		var in change
		if err := decodeAfter(after, &in); err != nil {
			return false, err
		}
		if in.CodexApprovals == nil {
			return false, errors.New("codex_approvals is required")
		}
		return *in.CodexApprovals, nil
	}
	return &target{
		read: func(context.Context) (any, any, error) {
			on := p.CodexApprovals()
			return change{&on}, on, nil
		},
		validate: func(_ context.Context, after json.RawMessage) error { _, err := decode(after); return err },
		apply: func(_ context.Context, after json.RawMessage, unchanged func(any) error, _ agentcontext.SaveInput) error {
			on, err := decode(after)
			if err != nil {
				return err
			}
			return p.SetCodexApprovals(on, func(current bool) error { return unchanged(current) })
		},
	}, nil
}

// agentEnvState is what an agent environment proposal's revision covers:
// the environment in force, values included, and whether the app or the
// config file sets it. It is hashed with the daemon's key, never shown.
type agentEnvState struct {
	Env map[string]string
	App bool
}

// agentEnvTarget is one variable of the agent environment, by name. The
// agent proposes adding the name, whose value the person types when saving
// (SaveInput), or removing it; the value is never in the proposal, its
// Before (whether the name is set) or anything the agent reads.
func (p *Proposers) agentEnvTarget(name string) *target {
	env := func(prefs repos.Preferences) map[string]string {
		return maps.Clone(prefs.Resolve(p.API.Config).AgentEnv)
	}
	decode := func(after json.RawMessage) (agentcontext.AgentEnvChange, error) {
		var in agentcontext.AgentEnvChange
		if err := decodeAfter(after, &in); err != nil {
			return in, err
		}
		if in.Name != name || in.NeedsValue == in.Remove {
			return in, errors.New("the proposal is not a change this page takes")
		}
		return in, nil
	}
	// change is the environment the save makes of the one in force, with
	// value (the person's) for an addition.
	change := func(in agentcontext.AgentEnvChange, current map[string]string, value string) (map[string]string, error) {
		next := maps.Clone(current)
		if next == nil {
			next = map[string]string{}
		}
		_, set := next[name]
		switch {
		case in.Remove && !set:
			return nil, fmt.Errorf("%s is not set", name)
		case in.Remove:
			delete(next, name)
		case set:
			return nil, fmt.Errorf("%s is set already: the person changes its value in Settings", name)
		default:
			next[name] = value
		}
		return next, config.ValidateAgentEnv(next)
	}
	t := p.prefsTarget(
		func(prefs repos.Preferences) any {
			_, set := env(prefs)[name]
			return struct {
				Name string `json:"name"`
				Set  bool   `json:"set"`
			}{name, set}
		},
		func(prefs repos.Preferences) any { return agentEnvState{env(prefs), len(prefs.AgentEnv) > 0} },
		func(after json.RawMessage, prefs repos.Preferences) error {
			in, err := decode(after)
			if err != nil {
				return err
			}
			_, err = change(in, env(prefs), "")
			return err
		}, nil)
	t.takesValue = true
	t.apply = func(ctx context.Context, after json.RawMessage, unchanged func(any) error, input agentcontext.SaveInput) error {
		in, err := decode(after)
		if err != nil {
			return err
		}
		switch {
		case in.Remove && input.Value != nil:
			return errors.New("removing a variable takes no value")
		case in.NeedsValue && input.Value == nil:
			return fmt.Errorf("adding %s needs its value, which the person types when saving", name)
		}
		value := ""
		if input.Value != nil {
			value = *input.Value
		}
		_, err = p.API.Registry.UpdatePreferencesIf(ctx, func(prefs repos.Preferences) (repos.Preferences, error) {
			if err := unchanged(agentEnvState{env(prefs), len(prefs.AgentEnv) > 0}); err != nil {
				return prefs, err
			}
			next, err := change(in, env(prefs), value)
			if err != nil {
				return prefs, err
			}
			return setAgentEnv(prefs, next), nil
		})
		return err
	}
	return t
}
