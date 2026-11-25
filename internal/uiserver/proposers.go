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
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/automatic"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/inboxes"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewgroups"
	"github.com/baum-ai/baum/internal/themes"
	"github.com/baum-ai/baum/internal/tilelibrary"
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
	// TileTypes saves the tile types a chat proposes; nil takes no such
	// proposal.
	TileTypes *tilelibrary.Store

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
		agentcontext.ProposalAnywhere: pageProposer{p, p.anywhereTarget},
		agentcontext.PageSettings:     pageProposer{p, p.settingsTarget},
		agentcontext.PageRepositories: pageProposer{p, p.repositoryTarget},
		agentcontext.PageInbox:        pageProposer{p, p.inboxTarget},
		agentcontext.PageGroup:        pageProposer{p, p.groupTarget},
		agentcontext.PagePullRequest:  pageProposer{p, p.pullRequestTarget},
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
		// Every state is plain data; one that does not encode gets a fresh
		// random revision each time, which matches no other, so nothing is
		// saved over it.
		fresh := make([]byte, 32)
		_, _ = rand.Read(fresh)
		return "unencodable-" + hex.EncodeToString(fresh)
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
	// takesTheme marks the one target whose save takes the name and slot the
	// person chose (a colour theme's).
	takesTheme bool
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

var (
	errNoValue = errors.New("this proposal takes no value")
	errNoTheme = errors.New("this proposal takes no theme name or slot")
)

func (pp pageProposer) Apply(ctx context.Context, page agentcontext.PageContext, proposal agentcontext.Proposal, revision string, input agentcontext.SaveInput) error {
	t, err := pp.resolve(page, proposal.Target)
	if err != nil {
		return err
	}
	if input.Value != nil && !t.takesValue {
		return errNoValue
	}
	if input.Theme != nil && !t.takesTheme {
		return errNoTheme
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
	{agentcontext.TargetLabs, "reviews"},
	{agentcontext.TargetFixes, "fixes"},
	{agentcontext.TargetTimeLimits, "computer"},
	{agentcontext.TargetJobConcurrency, "computer"},
	{agentcontext.TargetCodexApprovals, "computer"},
	{agentcontext.TargetAgentEnv, "computer"},
	{agentcontext.TargetTileType, agentcontext.SectionTiles},
}

var sectionTitles = map[string]string{"general": "General", "appearance": "Appearance", "reviews": "Reviews", "fixes": "Fixes", "connections": "Connections",
	"computer": "This computer", "prompts": "Prompts", "registration": "Registration", agentcontext.SectionTiles: "Tile library"}

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
		if name == s.prefix || ((strings.HasSuffix(s.prefix, "/") || strings.HasSuffix(s.prefix, ":")) && strings.HasPrefix(name, s.prefix) && len(name) > len(s.prefix)) {
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
	case name == agentcontext.TargetLabs:
		return p.labsTarget(), nil
	case name == agentcontext.TargetCodexApprovals:
		return p.codexApprovalsTarget()
	case strings.HasPrefix(name, agentcontext.TargetAgentEnv):
		return p.agentEnvTarget(strings.TrimPrefix(name, agentcontext.TargetAgentEnv)), nil
	case strings.HasPrefix(name, agentcontext.TargetTileType):
		return p.tileTypeTarget(strings.TrimPrefix(name, agentcontext.TargetTileType))
	}
	return nil, fmt.Errorf("Settings has no setting %q that takes proposals", name)
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

// anywhereTarget resolves the targets proposed under
// agentcontext.ProposalAnywhere, which is not a page: the colour theme, the
// choice of theme, and the prompts, global (TargetPrompt+id) or a
// repository's (RepositoryTarget(repository, TargetPrompt+id)).
func (p *Proposers) anywhereTarget(_ agentcontext.PageContext, name string) (*target, error) {
	switch name {
	case agentcontext.TargetTheme:
		return p.themeTarget(), nil
	case agentcontext.TargetThemeChoice:
		return p.themeChoiceTarget(), nil
	}
	scope, what := repos.GlobalPrompts, name
	if repository, rest, ok := agentcontext.ParseRepositoryTarget(name); ok {
		scope, what = repository, rest
	}
	if id, ok := strings.CutPrefix(what, agentcontext.TargetPrompt); ok && id != "" {
		return p.promptTarget(scope, id)
	}
	return nil, fmt.Errorf("only a colour theme or a prompt is proposed from any page; %q is a change to a page", name)
}

// themeChange is a theme proposal's After. ThemeID, when set, names the
// custom theme it edits.
type themeChange struct {
	ThemeID string            `json:"theme_id,omitempty"`
	Name    string            `json:"name"`
	Base    string            `json:"base"`
	Tokens  map[string]string `json:"tokens"`
}

// themeTargetState is the constant its revision is: a theme proposal adds or
// edits a theme, so nothing the person changes while previewing it (the
// slots, the other themes) makes the save stale.
const themeTargetState = "theme"

// errThemeGone is a theme proposal that edits a theme deleted since.
var errThemeGone = errors.New("the theme this edits was deleted: save it as a new theme instead")

// readThemeSlots reads the preferences for a theme target's "before".
func (p *Proposers) readThemeSlots(ctx context.Context) (appearanceSettings, func(id string) string, error) {
	prefs, err := p.API.Registry.Preferences(ctx)
	if err != nil {
		return appearanceSettings{}, nil, errors.New("the settings could not be read")
	}
	appearance := appearanceOf(prefs)
	name := func(id string) string {
		for _, t := range appearance.Themes {
			if t.ID == id {
				return t.Name
			}
		}
		return id
	}
	return appearance, name, nil
}

// themeTarget is a colour theme: a new custom theme, saved as POST
// /api/settings/appearance/themes is, or, when the proposal names a custom
// theme (theme_id), that theme replaced in place as PATCH
// /api/settings/appearance/themes/{id} does. The person's save picks which
// (SaveInput.Theme.Mode, update when theme_id is set else new), the name and,
// for a new theme, the slot; else the proposal's name and its base's slot.
func (p *Proposers) themeTarget() *target {
	decode := func(after json.RawMessage) (themeChange, error) {
		var in themeChange
		if err := decodeAfter(after, &in); err != nil {
			return in, err
		}
		return in, themes.Validate(themes.Theme{Name: in.Name, Base: in.Base, Tokens: in.Tokens})
	}
	return &target{
		takesTheme: true,
		read: func(ctx context.Context) (any, any, error) {
			appearance, name, err := p.readThemeSlots(ctx)
			if err != nil {
				return nil, nil, err
			}
			return struct {
				LightTheme string `json:"light_theme"`
				DarkTheme  string `json:"dark_theme"`
			}{name(appearance.LightTheme), name(appearance.DarkTheme)}, themeTargetState, nil
		},
		validate: func(ctx context.Context, after json.RawMessage) error {
			in, err := decode(after)
			if err != nil || in.ThemeID == "" {
				return err
			}
			if themes.IsBuiltin(in.ThemeID) {
				return fmt.Errorf("theme_id %q is a built-in theme, which cannot be changed: omit theme_id to propose a new theme", in.ThemeID)
			}
			prefs, err := p.API.Registry.Preferences(ctx)
			if err != nil {
				return errors.New("the settings could not be read")
			}
			if !slices.ContainsFunc(prefs.Appearance.Themes, func(t repos.CustomTheme) bool { return t.ID == in.ThemeID }) {
				return fmt.Errorf("theme_id %q is not one of the person's custom themes: omit theme_id to propose a new theme", in.ThemeID)
			}
			return nil
		},
		apply: func(ctx context.Context, after json.RawMessage, unchanged func(any) error, input agentcontext.SaveInput) error {
			in, err := decode(after)
			if err != nil {
				return err
			}
			name, slot, mode := in.Name, in.Base, agentcontext.ThemeModeNew
			if in.ThemeID != "" {
				mode = agentcontext.ThemeModeUpdate
			}
			if input.Theme != nil {
				if input.Theme.Name != "" {
					name = input.Theme.Name
				}
				if input.Theme.Slot != "" {
					slot = input.Theme.Slot
				}
				if input.Theme.Mode != "" {
					mode = input.Theme.Mode
				}
				if err := themes.ValidateName(name); err != nil {
					return err
				}
			}
			switch mode {
			case agentcontext.ThemeModeNew:
			case agentcontext.ThemeModeUpdate:
				if in.ThemeID == "" {
					return errors.New("this proposal does not edit a theme: save it as a new theme")
				}
				edit := themeEdit{Name: &name, Base: &in.Base, Tokens: in.Tokens}
				if edit.Tokens == nil {
					edit.Tokens = map[string]string{}
				}
				change := func(prefs repos.Preferences) (repos.Preferences, error) {
					prefs, err := editTheme(prefs, in.ThemeID, edit)
					if errors.Is(err, errThemeNotFound) {
						return prefs, errThemeGone
					}
					return prefs, err
				}
				_, err := p.API.updatePreferences(ctx, func(prefs repos.Preferences) error {
					if err := unchanged(themeTargetState); err != nil {
						return err
					}
					_, err := change(prefs)
					return err
				}, func(prefs repos.Preferences) repos.Preferences {
					prefs, _ = change(prefs)
					return prefs
				})
				return err
			default:
				return errors.New("the mode of a theme save is update or new")
			}
			// The id is made once, outside the transaction, which may run
			// its function again.
			id, err := newThemeID()
			if err != nil {
				return errors.New("could not make a theme id")
			}
			theme := repos.CustomTheme{ID: id, Name: name, Base: in.Base, Tokens: in.Tokens}
			_, err = p.API.updatePreferences(ctx, func(prefs repos.Preferences) error {
				if err := unchanged(themeTargetState); err != nil {
					return err
				}
				_, err := addTheme(prefs, theme, slot)
				return err
			}, func(prefs repos.Preferences) repos.Preferences {
				prefs, _ = addTheme(prefs, theme, slot)
				return prefs
			})
			return err
		},
	}
}

// themeChoiceChange is a theme choice's After: an existing theme for a slot,
// and/or the theme mode. Slot is light, dark or agentcontext.SlotInUse, which
// the daemon cannot resolve (it cannot see the operating system's
// appearance): the app does, and saves with an explicit slot.
type themeChoiceChange struct {
	ThemeID string `json:"theme_id,omitempty"`
	Slot    string `json:"slot,omitempty"`
	Theme   string `json:"theme,omitempty"`
}

// themeChoiceTarget switches the app to an existing theme (built-in or
// custom) in a slot and/or sets the theme mode, saved as PUT
// /api/settings/appearance is. Like the theme target its revision is a
// constant: the save writes onto the preferences as they are.
func (p *Proposers) themeChoiceTarget() *target {
	decode := func(after json.RawMessage, prefs *repos.Preferences) (themeChoiceChange, error) {
		var in themeChoiceChange
		if err := decodeAfter(after, &in); err != nil {
			return in, err
		}
		if in.ThemeID == "" && in.Theme == "" {
			return in, errors.New("propose a theme_id, a theme mode, or both")
		}
		if in.Theme != "" {
			if err := (appearanceInput{Theme: &in.Theme}).check(); err != nil {
				return in, err
			}
		}
		if in.ThemeID == "" {
			if in.Slot != "" {
				return in, errors.New("slot needs a theme_id")
			}
			return in, nil
		}
		if in.Slot != "" && in.Slot != "light" && in.Slot != "dark" && in.Slot != agentcontext.SlotInUse {
			return in, errors.New("slot must be light, dark or omitted for the slot in use")
		}
		if prefs != nil && !themeKnown(*prefs, in.ThemeID) {
			return in, fmt.Errorf("unknown theme %q: use an id from settings_appearance", in.ThemeID)
		}
		return in, nil
	}
	return &target{
		takesTheme: true,
		read: func(ctx context.Context) (any, any, error) {
			appearance, name, err := p.readThemeSlots(ctx)
			if err != nil {
				return nil, nil, err
			}
			return struct {
				Theme      string `json:"theme"`
				LightTheme string `json:"light_theme"`
				DarkTheme  string `json:"dark_theme"`
			}{appearance.Theme, name(appearance.LightTheme), name(appearance.DarkTheme)}, themeTargetState, nil
		},
		validate: func(ctx context.Context, after json.RawMessage) error {
			prefs, err := p.API.Registry.Preferences(ctx)
			if err != nil {
				return errors.New("the settings could not be read")
			}
			_, err = decode(after, &prefs)
			return err
		},
		apply: func(ctx context.Context, after json.RawMessage, unchanged func(any) error, input agentcontext.SaveInput) error {
			in, err := decode(after, nil)
			if err != nil {
				return err
			}
			change := appearanceInput{}
			if in.Theme != "" {
				change.Theme = &in.Theme
			}
			if in.ThemeID != "" {
				slot := in.Slot
				if slot == "" || slot == agentcontext.SlotInUse {
					slot = ""
					if input.Theme != nil {
						slot = input.Theme.Slot
					}
				}
				switch slot {
				case "light":
					change.LightTheme = &in.ThemeID
				case "dark":
					change.DarkTheme = &in.ThemeID
				default:
					return errors.New("the slot in use is the app's to resolve: save with slot light or dark")
				}
			}
			_, err = p.API.updatePreferences(ctx, func(prefs repos.Preferences) error {
				if err := unchanged(themeTargetState); err != nil {
					return err
				}
				return change.checkThemes(prefs)
			}, change.edit)
			return err
		},
	}
}

// appearanceTarget is General's appearance preferences: key hints, page
// modifier and review page. The theme mode and theme slots are the theme
// choice's (themeChoiceTarget), which any page takes.
func (p *Proposers) appearanceTarget() *target {
	decode := func(after json.RawMessage) (appearanceInput, error) {
		var in appearanceInput
		if err := decodeAfter(after, &in); err != nil {
			return in, err
		}
		if in == (appearanceInput{}) {
			return in, errors.New("propose key_hints, page_modifier or review_page")
		}
		if in.Theme != nil || in.LightTheme != nil || in.DarkTheme != nil {
			return in, errors.New("theme, light_theme and dark_theme are the theme choice's: propose them with propose_theme_choice")
		}
		return in, in.check()
	}
	return p.prefsTarget(
		func(prefs repos.Preferences) any { return appearanceOf(prefs) },
		func(prefs repos.Preferences) any { return prefs.Appearance },
		func(after json.RawMessage, prefs repos.Preferences) error {
			in, err := decode(after)
			if err != nil {
				return err
			}
			return in.checkThemes(prefs)
		},
		func(ctx context.Context, after json.RawMessage, check func(repos.Preferences) error) error {
			in, err := decode(after)
			if err != nil {
				return err
			}
			// The proposal's own check and the route's: a slot may name only
			// a theme the preferences hold.
			_, err = p.API.updatePreferences(ctx, func(prefs repos.Preferences) error {
				if check != nil {
					if err := check(prefs); err != nil {
						return err
					}
				}
				return in.checkThemes(prefs)
			}, in.edit)
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
		Updates      *string `json:"updates,omitempty"`
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
			return automatic.PreferencesInput{}, errors.New("propose enabled, pull_requests, updates, backend, model or effort")
		}
		return automatic.PreferencesInput{Enabled: in.Enabled, PullRequests: in.PullRequests, Updates: in.Updates, Backend: in.Backend, Model: in.Model, Effort: in.Effort}, nil
	}
	type reviewsState struct {
		Automatic    bool
		PullRequests string
		Updates      string
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
		return reviewsState{prefs.Automatic, prefs.PullRequests, prefs.Updates, prefs.Backend, prefs.Model, prefs.Effort}
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
			Updates      string `json:"updates"`
			Backend      string `json:"backend"`
			Model        string `json:"model"`
			Effort       string `json:"effort"`
		}{prefs.Automatic, pullRequests, prefs.ReviewUpdates(), string(cfg.Backend), cfg.Model, cfg.Effort}
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

// labsTarget is the labs settings (PUT /api/settings/labs).
func (p *Proposers) labsTarget() *target {
	decode := func(after json.RawMessage) (labsInput, error) {
		var in labsInput
		if err := decodeAfter(after, &in); err != nil {
			return in, err
		}
		if in.TileReviews == nil {
			return in, errors.New("tile_reviews is required")
		}
		return in, nil
	}
	return p.prefsTarget(
		func(prefs repos.Preferences) any { return labsOf(prefs) },
		func(prefs repos.Preferences) any { return labsOf(prefs) },
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
	// save is the preferences the route's save makes of prefs with the
	// change. It is refused when the environment in force would then differ
	// from the proposed one: an app environment left empty goes back to the
	// config file's, which would bring back what was removed, or more.
	save := func(in agentcontext.AgentEnvChange, prefs repos.Preferences, value string) (repos.Preferences, error) {
		next, err := change(in, env(prefs), value)
		if err != nil {
			return prefs, err
		}
		saved := setAgentEnv(prefs, next)
		if !maps.Equal(env(saved), next) {
			return prefs, fmt.Errorf("removing %s leaves the app no variables of its own, so the config file's agent_env would apply again and change more than %s: the person edits the config file for that", name, name)
		}
		return saved, nil
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
			_, err = save(in, prefs, "")
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
			return save(in, prefs, value)
		})
		return err
	}
	return t
}
