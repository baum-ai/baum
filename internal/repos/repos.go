// Package repos stores explicitly registered local clones and repository settings.
package repos

import (
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/baum-ai/baum/internal/agentdirs"
	"github.com/baum-ai/baum/internal/appdb"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/gitlocal"
)

// ErrNotFound reports a clone or picked repository the registry does not hold.
var ErrNotFound = errors.New("registered clone or picked repository not found")

// ErrInvalidRepository reports a name that is not a github.com owner/name.
var ErrInvalidRepository = errors.New("repository must be a github.com owner/name")

// Settings are shared by all clones of a repository. Automatic is the
// repository's choice under the global master switch (see AutomaticEnabled):
// nil follows it, false turns the repository off, true is an explicit on that
// still stops while the global switch is off. Registration itself starts no
// generation. A daily_cap, backend or model an older baum stored is ignored
// on read and dropped on the next write: baum sets no spending limits, since
// it cannot price what a subscription bills, and the harnesses and models are
// global (Preferences).
type Settings struct {
	Automatic *bool `json:"automatic"`
	// Decisions is the repository's choice for local decision capture under
	// the global switches (see DecisionsEnabled): nil and true follow them,
	// false turns the repository off.
	Decisions *bool `json:"decisions,omitempty"`
}

// AutomaticEnabled reports whether the daemon generates reviews of this
// repository automatically. The global preference is a master switch: off
// stops every repository, whatever its choice; on, only an explicit false
// turns a repository off.
func (s Settings) AutomaticEnabled(p Preferences) bool {
	if !p.Automatic {
		return false
	}
	return s.Automatic == nil || *s.Automatic
}

// DecisionsEnabled reports whether the daemon captures the decisions of this
// repository's agent sessions: the automatic master switch (which the tray's
// pause turns off) and the decisions switch are both on, and the repository
// has not turned capture off.
func (s Settings) DecisionsEnabled(p Preferences) bool {
	return p.Automatic && (p.Decisions.Enabled == nil || *p.Decisions.Enabled) && (s.Decisions == nil || *s.Decisions)
}

// DecisionPreferences are the global choices for local decision capture and
// the feature log, the data extraction the app's settings name. A nil Enabled
// is on. A nil Backend follows the reviews' backend. Model, when set,
// replaces the backend's default decision model. Effort is the reasoning
// effort (config.Efforts), empty or nil for the harness's own.
type DecisionPreferences struct {
	Enabled *bool           `json:"enabled,omitempty"`
	Backend *config.Backend `json:"backend,omitempty"`
	Model   *string         `json:"model,omitempty"`
	Effort  *string         `json:"effort,omitempty"`
	// ClaudeConfigDir is the Claude Code configuration directory extraction
	// runs with, as Preferences.FixClaudeConfigDir is for fixes: only with a
	// Backend of its own; without one extraction follows the reviews,
	// directory included.
	ClaudeConfigDir *string `json:"claude_config_dir,omitempty"`
	// CodexHome is the Codex home extraction runs with, under the same rule.
	CodexHome *string `json:"codex_home,omitempty"`
}

// AppearancePreferences are device choices kept independently of the daemon HTTP origin.
type AppearancePreferences struct {
	Theme    string `json:"theme,omitempty"`
	KeyHints *bool  `json:"key_hints,omitempty"`
	// PageModifier is the key held with 1 to 8 to open the sidebar's pages:
	// "meta", "ctrl" or "alt"; empty is the platform's command key.
	PageModifier string `json:"page_modifier,omitempty"`
	// Sidebar is whether the app's sidebar stays open beside the page
	// ("pinned") or folds to its glyphs and opens over the page under the
	// pointer ("folded"); empty is pinned.
	Sidebar string `json:"sidebar,omitempty"`
}

// PlanUsagePreferences are the plans the person chose to show or hide in plan
// usage, by provider ("claude", "codex"). A provider with no entry follows
// the backends baum uses, so a choice survives backend changes.
type PlanUsagePreferences struct {
	Show map[string]bool `json:"show,omitempty"`
}

// Preferences are the registry's global choices, edited in the app. Fields
// an older baum saved and this one dropped, such as
// cursor_agent_acknowledged, are ignored on read and gone after the next
// write.
type Preferences struct {
	// Automatic is the default for repositories without an explicit choice.
	Automatic bool `json:"automatic"`
	// Backend and Model, when set, replace the config file's for generation
	// in the app; a repository's own settings still override them.
	Backend *config.Backend `json:"backend,omitempty"`
	Model   *string         `json:"model,omitempty"`
	// Effort is the reviews' reasoning effort (config.Efforts), empty or nil
	// for the harness's own.
	Effort *string `json:"effort,omitempty"`
	// FixBackend, FixModel and FixEffort choose the agent that fixes pull
	// requests (ResolveFix). A nil FixBackend means the same as reviews; an
	// empty FixModel is the fix backend's default model and an empty
	// FixEffort its own effort.
	FixBackend *config.Backend `json:"fix_backend,omitempty"`
	FixModel   *string         `json:"fix_model,omitempty"`
	FixEffort  *string         `json:"fix_effort,omitempty"`
	// ClaudeConfigDir and FixClaudeConfigDir are the Claude Code
	// configuration directories reviews (with data extraction) and fixes run
	// Claude Code with (config.Config.ClaudeConfigDir), absolute paths; nil
	// or empty leaves it to the environment. FixClaudeConfigDir applies only
	// with a FixBackend: without one a fix follows the reviews, directory
	// included. Another agent ignores them.
	ClaudeConfigDir    *string `json:"claude_config_dir,omitempty"`
	FixClaudeConfigDir *string `json:"fix_claude_config_dir,omitempty"`
	// CodexHome and FixCodexHome are the same for Codex: the Codex homes
	// reviews and fixes run Codex with (config.Config.CodexHome), under the
	// same rules.
	CodexHome    *string `json:"codex_home,omitempty"`
	FixCodexHome *string `json:"fix_codex_home,omitempty"`
	// PullRequests narrows which pull requests get automatic reviews:
	// PullRequestsAuthored, PullRequestsRequested, PullRequestsAll, or empty
	// for both authored and requested.
	PullRequests string `json:"pull_requests,omitempty"`
	// AgentEnv, when it sets any variable, replaces the config file's
	// agent_env (config.Config.AgentEnv).
	AgentEnv map[string]string `json:"agent_env,omitempty"`
	// Appearance survives daemon restarts and changes to its loopback origin.
	Appearance AppearancePreferences `json:"appearance,omitzero"`
	// Decisions configures local decision capture.
	Decisions DecisionPreferences `json:"decisions,omitzero"`
	// PlanUsage chooses which plans plan usage shows.
	PlanUsage PlanUsagePreferences `json:"plan_usage,omitzero"`
	// JobConcurrency, when set, is how many jobs the daemon runs at once
	// besides questions to a review's agent; Concurrency resolves it.
	JobConcurrency int `json:"job_concurrency,omitempty"`
	// ReviewTimeLimit and RunTimeLimit, in minutes, when set replace the
	// config file's review_timeout and run_timeout (Resolve): how long a
	// review, and any other agent run, may take.
	ReviewTimeLimit int `json:"review_time_limit_minutes,omitempty"`
	RunTimeLimit    int `json:"run_time_limit_minutes,omitempty"`
	// Panel is what the session panel opens a conversation with
	// (ResolvePanel).
	Panel PanelPreferences `json:"panel,omitzero"`
}

// PanelPreferences are the session panel's defaults. An empty Agent follows
// the fix agent, model included; a nil Model is the fix agent's model when
// Agent is that agent, else the agent's own default. An empty Mode is
// PanelModeFull and an empty Position PanelBottom.
type PanelPreferences struct {
	Agent    string  `json:"agent,omitempty"`
	Model    *string `json:"model,omitempty"`
	Mode     string  `json:"mode,omitempty"`
	Position string  `json:"position,omitempty"`
}

// The values of PanelPreferences: the agents a panel conversation runs, as
// live sessions name them, its access, and where the panel opens.
const (
	PanelAgentClaudeCode = "claude-code"
	PanelAgentCodex      = "codex"
	PanelModeFull        = "full"
	PanelModeWorkspace   = "workspace"
	PanelBottom          = "bottom"
	PanelTop             = "top"
)

// Panel is the session panel's defaults in force; AgentSource and
// ModelSource say whether each is the person's choice ("panel") or follows
// the fix agent ("fix").
type Panel struct {
	Agent       string `json:"agent"`
	Model       string `json:"model"`
	Mode        string `json:"mode"`
	Position    string `json:"position"`
	AgentSource string `json:"agent_source"`
	ModelSource string `json:"model_source"`
}

// ResolvePanel gives the session panel's defaults from the preferences and
// review, the config already resolved for reviews. The fix agent
// (ResolveFix) is Codex when its backend is codex and Claude Code otherwise;
// its model counts only for Claude Code or Codex, since another backend's
// model names nothing these agents run.
func (p Preferences) ResolvePanel(review config.Config) Panel {
	fix := p.ResolveFix(review)
	fixAgent, fixModel := PanelAgentClaudeCode, ""
	switch fix.Backend {
	case config.BackendCodex:
		fixAgent, fixModel = PanelAgentCodex, fix.Model
	case config.BackendClaude:
		fixModel = fix.Model
	}
	out := Panel{Agent: fixAgent, Model: fixModel, Mode: cmp.Or(p.Panel.Mode, PanelModeFull), Position: cmp.Or(p.Panel.Position, PanelBottom),
		AgentSource: "fix", ModelSource: "fix"}
	if p.Panel.Agent != "" {
		out.Agent, out.AgentSource = p.Panel.Agent, "panel"
		if out.Agent != fixAgent {
			out.Model, out.ModelSource = "", "panel"
		}
	}
	if p.Panel.Model != nil {
		out.Model, out.ModelSource = *p.Panel.Model, "panel"
	}
	return out
}

// MaxTimeLimit is the longest time limit, in minutes, the app sets.
const MaxTimeLimit = 240

// MaxJobConcurrency is the most jobs the daemon runs at once besides
// questions to a review's agent, and the default. A person lowers it when
// parallel agents load the computer too much.
const MaxJobConcurrency = 4

// Concurrency is how many jobs run at once: JobConcurrency, or
// MaxJobConcurrency when it is unset or out of range (a hand edit, or a newer
// baum's).
func (p Preferences) Concurrency() int {
	if p.JobConcurrency < 1 || p.JobConcurrency > MaxJobConcurrency {
		return MaxJobConcurrency
	}
	return p.JobConcurrency
}

// The PullRequests choices besides both.
const (
	PullRequestsAuthored  = "authored"
	PullRequestsRequested = "requested"
	// PullRequestsAll covers every open pull request in the repository.
	PullRequestsAll = "all"
)

// Covers reports whether automatic reviews cover a pull request the viewer
// authored or was asked to review, as PullRequests chooses.
func (p Preferences) Covers(authored, requested bool) bool {
	switch p.PullRequests {
	case PullRequestsAuthored:
		return authored
	case PullRequestsRequested:
		return requested
	case PullRequestsAll:
		return true
	}
	return authored || requested
}

// ValidateChange is Validate for preferences saved over prev: the desktop app
// offers Claude Code and Codex only, so a backend chosen now must be one of
// them. A retired backend already stored (cursor-agent or anthropic, from a
// pre-release version) does not block saving other settings; the daemon
// refuses the jobs that use it.
func (p Preferences) ValidateChange(prev Preferences) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if p.Backend != nil && !p.Backend.Desktop() && (prev.Backend == nil || *prev.Backend != *p.Backend) {
		return errors.New("reviews run with auto, Claude Code or Codex only")
	}
	if b := p.Decisions.Backend; b != nil && *b == config.BackendAnthropic && (prev.Decisions.Backend == nil || *prev.Decisions.Backend != *b) {
		return errors.New("data extraction runs with Claude Code or Codex only")
	}
	return nil
}

// Validate accepts an empty Model, unlike Settings: it means the backend's
// default even when the config file names a model.
func (p Preferences) Validate() error {
	if p.Backend != nil && !p.Backend.Valid() {
		return errors.New("invalid backend")
	}
	if p.Model != nil && len(*p.Model) > 256 {
		return errors.New("model must be at most 256 bytes")
	}
	if m := p.Decisions.Model; m != nil && len(*m) > 256 {
		return errors.New("decisions model must be at most 256 bytes")
	}
	if b := p.Decisions.Backend; b != nil && *b != config.BackendClaude && *b != config.BackendCodex && *b != config.BackendAnthropic {
		return errors.New("data extraction runs with Claude Code, Codex or the Anthropic API only")
	}
	if p.FixBackend != nil && *p.FixBackend != config.BackendClaude && *p.FixBackend != config.BackendCodex {
		return errors.New("fixes run with Claude Code or Codex only")
	}
	if p.FixModel != nil && len(*p.FixModel) > 256 {
		return errors.New("fix model must be at most 256 bytes")
	}
	if p.FixBackend == nil && p.FixModel != nil && strings.TrimSpace(*p.FixModel) != "" {
		return errors.New("choose the agent for fixes before its model")
	}
	for _, effort := range []*string{p.Effort, p.FixEffort, p.Decisions.Effort} {
		if effort != nil && !config.ValidEffort(*effort) {
			return fmt.Errorf("unknown reasoning effort %q; use %s or leave it empty for the agent's own", *effort, strings.Join(config.Efforts, ", "))
		}
	}
	if p.FixBackend == nil && p.FixEffort != nil && *p.FixEffort != "" {
		return errors.New("choose the agent for fixes before its effort")
	}
	if p.FixBackend == nil && (chosen(p.FixClaudeConfigDir) || chosen(p.FixCodexHome)) {
		return errors.New("choose the agent for fixes before its configuration directory")
	}
	if p.Decisions.Backend == nil && (chosen(p.Decisions.ClaudeConfigDir) || chosen(p.Decisions.CodexHome)) {
		return errors.New("choose the agent for data extraction before its configuration directory")
	}
	for what, dirs := range map[string][]*string{
		agentdirs.Claude.What: {p.ClaudeConfigDir, p.FixClaudeConfigDir, p.Decisions.ClaudeConfigDir},
		agentdirs.Codex.What:  {p.CodexHome, p.FixCodexHome, p.Decisions.CodexHome},
	} {
		for _, dir := range dirs {
			if !chosen(dir) {
				continue
			}
			if clean, err := agentdirs.Normalize(*dir); err != nil || clean != *dir {
				return fmt.Errorf("%s %q must be an absolute, clean path", what, *dir)
			}
		}
	}
	if err := config.ValidateAgentEnv(p.AgentEnv); err != nil {
		return fmt.Errorf("agent environment: %w", err)
	}
	switch p.PullRequests {
	case "", PullRequestsAuthored, PullRequestsRequested, PullRequestsAll:
		return nil
	}
	return fmt.Errorf("unknown pull requests choice %q; use %s, %s, %s or leave it empty for both", p.PullRequests, PullRequestsAuthored, PullRequestsRequested, PullRequestsAll)
}

// ValidateTimeLimit accepts zero (the config file's) or 1 to MaxTimeLimit
// minutes.
func ValidateTimeLimit(minutes int) error {
	if minutes < 0 || minutes > MaxTimeLimit {
		return fmt.Errorf("must be between 1 and %d minutes", MaxTimeLimit)
	}
	return nil
}

// chosen reports whether s holds a non-empty choice.
func chosen(s *string) bool { return s != nil && *s != "" }

// Resolve overlays the app's backend, model, Claude Code configuration
// directory, Codex home, agent environment and time limits on the config
// file's.
func (p Preferences) Resolve(global config.Config) config.Config {
	if len(p.AgentEnv) > 0 {
		global.AgentEnv = p.AgentEnv
	}
	// A limit out of range (a hand edit, or a newer baum's) keeps the file's,
	// and does not block saving other choices.
	if ValidateTimeLimit(p.ReviewTimeLimit) == nil && p.ReviewTimeLimit > 0 {
		global.Budgets.ReviewTimeout = config.Duration(time.Duration(p.ReviewTimeLimit) * time.Minute)
	}
	if ValidateTimeLimit(p.RunTimeLimit) == nil && p.RunTimeLimit > 0 {
		global.Budgets.RunTimeout = config.Duration(time.Duration(p.RunTimeLimit) * time.Minute)
	}
	if p.Backend != nil {
		global.Backend = *p.Backend
	}
	if p.Model != nil {
		global.Model = strings.TrimSpace(*p.Model)
	}
	if p.Effort != nil {
		global.Effort = *p.Effort
	}
	if p.ClaudeConfigDir != nil {
		global.ClaudeConfigDir = *p.ClaudeConfigDir
	}
	if p.CodexHome != nil {
		global.CodexHome = *p.CodexHome
	}
	return global
}

// ResolveExtraction gives the config data extraction runs with, from cfg,
// the config resolved for reviews: the extraction's own effort, and with an
// agent of its own its own Claude Code configuration directory and Codex home
// (none when it chose none). Its backend and model are decisionlog's to resolve.
func (p Preferences) ResolveExtraction(cfg config.Config) config.Config {
	cfg.Effort = ""
	if p.Decisions.Effort != nil {
		cfg.Effort = *p.Decisions.Effort
	}
	if p.Decisions.Backend != nil {
		cfg.ClaudeConfigDir, cfg.CodexHome = "", ""
		if p.Decisions.ClaudeConfigDir != nil {
			cfg.ClaudeConfigDir = *p.Decisions.ClaudeConfigDir
		}
		if p.Decisions.CodexHome != nil {
			cfg.CodexHome = *p.Decisions.CodexHome
		}
	}
	return cfg
}

// ResolveFix gives the config a fix runs with, from review, the config
// already resolved for reviews. A set FixBackend replaces the review backend,
// model, effort, Claude Code configuration directory and Codex home, and takes
// FixModel, FixEffort, FixClaudeConfigDir and FixCodexHome (empty for the
// backend's own); without one the fix follows the reviews.
func (p Preferences) ResolveFix(review config.Config) config.Config {
	if p.FixBackend == nil {
		return review
	}
	review.Backend = *p.FixBackend
	review.Model, review.Effort, review.ClaudeConfigDir, review.CodexHome = "", "", "", ""
	if p.FixClaudeConfigDir != nil {
		review.ClaudeConfigDir = *p.FixClaudeConfigDir
	}
	if p.FixCodexHome != nil {
		review.CodexHome = *p.FixCodexHome
	}
	if p.FixModel != nil {
		review.Model = strings.TrimSpace(*p.FixModel)
	}
	if p.FixEffort != nil {
		review.Effort = *p.FixEffort
	}
	return review
}

// ClaudeConfigDirs are the Claude Code configuration directories chosen for
// the runs that use Claude Code, the reviews', the fixes' then data
// extraction's, once each, with global the config file's. A directory chosen
// for another agent, which ignores it, is left out; Auto counts, as it runs
// Claude Code first.
func (p Preferences) ClaudeConfigDirs(global config.Config) []string {
	return p.runDirs(global, func(cfg config.Config) string {
		if cfg.Backend == config.BackendClaude || cfg.Backend == config.BackendAuto || cfg.Backend == "" {
			return cfg.ClaudeConfigDir
		}
		return ""
	})
}

// CodexHomes are the Codex homes chosen for the runs that use Codex, in the
// same order and under the same rules as ClaudeConfigDirs; only a run with
// the Codex backend counts.
func (p Preferences) CodexHomes(global config.Config) []string {
	return p.runDirs(global, func(cfg config.Config) string {
		if cfg.Backend == config.BackendCodex {
			return cfg.CodexHome
		}
		return ""
	})
}

// runDirs is dir of the configs of reviews, fixes and data extraction (only
// while it is on), once each and without empty ones.
func (p Preferences) runDirs(global config.Config, dir func(config.Config) string) []string {
	review := p.Resolve(global)
	extraction := p.ResolveExtraction(review)
	if p.Decisions.Backend != nil {
		extraction.Backend = *p.Decisions.Backend
	}
	uses := []config.Config{review, p.ResolveFix(review)}
	if p.Decisions.Enabled == nil || *p.Decisions.Enabled {
		uses = append(uses, extraction)
	}
	var dirs []string
	for _, cfg := range uses {
		if d := dir(cfg); d != "" && !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// DefaultPreferences apply until the user changes them: automatic generation
// on.
func DefaultPreferences() Preferences { return Preferences{Automatic: true} }

type Clone struct {
	ID         string   `json:"id"`
	Path       string   `json:"path"`
	CommonDir  string   `json:"common_dir"`
	Repository string   `json:"repository"`
	Settings   Settings `json:"settings"`
	Available  bool     `json:"available"`
}
type Registry struct {
	// Registered, when set, is called with each clone a registration stored
	// (Add, AddFor, AddForwarded and AttachFound), after it is stored. It
	// must not block: the daemon uses it to start work on the new clone. A
	// registry the CLI opens has none.
	Registered func(Clone)
	// Removed, when set, is called with each clone Remove or Unpick deleted,
	// after it is deleted, and must not block.
	Removed func(Clone)
	db      *sql.DB
	// noSchema marks a read-only registry whose file has no tables yet.
	noSchema bool
	noPicked bool
	// noPreferences marks a read-only registry without saved preferences.
	noPreferences bool
}

func Path() (string, error) { return appdb.Path() }

// Open opens repository tables in the shared application database.
func Open(path string) (*Registry, error) {
	db, e := appdb.Open(path, false)
	if e != nil {
		return nil, e
	}
	_, e = db.Exec(`CREATE TABLE IF NOT EXISTS repositories(identity TEXT PRIMARY KEY,settings TEXT NOT NULL); CREATE TABLE IF NOT EXISTS clones(id TEXT PRIMARY KEY,path TEXT NOT NULL UNIQUE,common_dir TEXT NOT NULL,repository TEXT NOT NULL REFERENCES repositories(identity)); CREATE TABLE IF NOT EXISTS preferences(key TEXT PRIMARY KEY,value TEXT NOT NULL); CREATE TABLE IF NOT EXISTS picked(identity TEXT PRIMARY KEY REFERENCES repositories(identity) ON DELETE CASCADE); CREATE TABLE IF NOT EXISTS aliases(alias TEXT PRIMARY KEY,repository TEXT NOT NULL); CREATE TABLE IF NOT EXISTS prompt_overrides(repository TEXT NOT NULL,prompt_id TEXT NOT NULL,text TEXT NOT NULL,based_on TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(repository,prompt_id));`)

	if e == nil {
		e = loadAliases(db)
	}
	if e != nil {
		_ = db.Close()
		return nil, e
	}
	return &Registry{db: db}, nil
}

// OpenReadOnly opens an existing registry only to list it. Unlike Open it
// creates no directory, database or table and changes no file mode, and
// SQLite opens the file read-only, so it never writes it. In WAL mode SQLite
// creates its -shm and -wal coordination files beside the database when
// missing (see appdb.Open). A missing file is an error wrapping
// fs.ErrNotExist; a file without tables lists no clones.
func OpenReadOnly(path string) (*Registry, error) {
	db, e := appdb.Open(path, true)
	if e != nil {
		return nil, e
	}
	var tables, preferences, picked int
	if e = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('clones','repositories')`).Scan(&tables); e != nil {
		_ = db.Close()
		return nil, e
	}
	if e = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='preferences'`).Scan(&preferences); e != nil {
		_ = db.Close()
		return nil, e
	}
	if e = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='picked'`).Scan(&picked); e != nil {
		_ = db.Close()
		return nil, e
	}
	// A registry from before aliases has none to load.
	_ = loadAliases(db)
	return &Registry{db: db, noSchema: tables < 2, noPreferences: preferences == 0, noPicked: picked == 0}, nil
}

func (r *Registry) Close() error { return r.db.Close() }

// loadAliases makes remote URLs using a recorded earlier name read as the
// repository's current one, in this process (gitlocal.AddRenamed).
func loadAliases(db *sql.DB) error {
	rows, e := db.Query("SELECT alias,repository FROM aliases")
	if e != nil {
		return e
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var alias, repository string
		if e = rows.Scan(&alias, &repository); e != nil {
			return e
		}
		gitlocal.AddRenamed(alias, repository)
	}
	return rows.Err()
}

// RecheckAliases asks GitHub (current) again about the recorded earlier names
// of repository, or of every repository when it is "", and drops each one it
// now answers with another repository than the recorded one does, as when a
// new repository took the name: remotes using it then name that one. One it
// could not check or does not show the token is kept, and returned as
// unconfirmed: a repository the token cannot see may have taken it.
func (r *Registry) RecheckAliases(ctx context.Context, repository string, current func(context.Context, github.Repository) (string, error)) (unconfirmed []string, err error) {
	rows, e := r.db.QueryContext(ctx, "SELECT alias,repository FROM aliases WHERE ?1='' OR repository=lower(?1)", repository)
	if e != nil {
		return nil, e
	}
	var aliases [][2]string
	for rows.Next() {
		var alias [2]string
		if e = rows.Scan(&alias[0], &alias[1]); e != nil {
			_ = rows.Close()
			return nil, e
		}
		aliases = append(aliases, alias)
	}
	if e = errors.Join(rows.Err(), rows.Close()); e != nil {
		return nil, e
	}
	for _, alias := range aliases {
		earlier, e := github.ParseRepository(alias[0])
		if e != nil {
			continue
		}
		now, e := current(ctx, earlier)
		if e != nil {
			unconfirmed = append(unconfirmed, alias[0])
			continue
		}
		if strings.EqualFold(now, alias[1]) {
			continue
		}
		// Renamed again since: both names still lead to the same repository.
		if recorded, e := github.ParseRepository(alias[1]); e != nil {
			continue
		} else if then, e := current(ctx, recorded); e != nil {
			unconfirmed = append(unconfirmed, alias[0])
			continue
		} else if strings.EqualFold(now, then) {
			continue
		}
		// Unless AddForwarded recorded it anew meanwhile.
		result, e := r.db.ExecContext(ctx, "DELETE FROM aliases WHERE alias=? AND repository=?", alias[0], alias[1])
		if e != nil {
			return unconfirmed, e
		}
		if n, _ := result.RowsAffected(); n > 0 {
			gitlocal.RemoveRenamed(alias[0])
		}
	}
	return unconfirmed, nil
}

// AddForwarded registers a clone for repository, as AddFor does, whose remote
// uses alias, an earlier name GitHub forwards to repository, which the caller
// has checked. The alias is recorded first: from then on remote URLs using it
// read as repository (gitlocal.RenamedTo), so the clone stays a clone of
// repository everywhere, until RecheckAliases finds GitHub no longer
// forwards it.
func (r *Registry) AddForwarded(ctx context.Context, path, repository, alias string, settings *Settings) (Clone, error) {
	gh, e := github.ParseRepository(repository)
	old, oe := github.ParseRepository(alias)
	if e != nil || oe != nil || gh.Host != config.GitHubHost || old.Host != config.GitHubHost {
		return Clone{}, ErrInvalidRepository
	}
	current, earlier := strings.ToLower(gh.String()), strings.ToLower(old.String())
	// As gitlocal.AddRenamed: names forwarded to the earlier one follow it,
	// and the current name is no longer an earlier one.
	if e = r.exec(ctx, []string{"DELETE FROM aliases WHERE alias=?2", "UPDATE aliases SET repository=?2 WHERE repository=?1", "INSERT INTO aliases VALUES (?1,?2) ON CONFLICT(alias) DO UPDATE SET repository=excluded.repository"}, earlier, current); e != nil {
		return Clone{}, e
	}
	gitlocal.AddRenamed(earlier, current)
	c, e := r.AddFor(ctx, path, current, settings)
	if e != nil {
		return Clone{}, e
	}
	// The earlier name is the same repository: its clones move to the
	// current one, and it is not picked twice.
	// Settings chosen under a still-picked earlier name replace the current
	// name's default.
	if e = r.exec(ctx, []string{
		`UPDATE repositories SET settings=(SELECT settings FROM repositories WHERE identity=?1) WHERE identity=?2 AND settings='{"automatic":null}' AND EXISTS (SELECT 1 FROM picked WHERE identity=?1)`,
		// Custom prompts follow the same way, only onto a name with none.
		`INSERT INTO prompt_overrides SELECT ?2, prompt_id, text, based_on, updated_at FROM prompt_overrides WHERE repository=?1 AND NOT EXISTS (SELECT 1 FROM prompt_overrides WHERE repository=?2) AND EXISTS (SELECT 1 FROM picked WHERE identity=?1)`,
		// The earlier name is forwarded and unpicked, and another repository
		// may later take it, so its overrides do not stay behind.
		`DELETE FROM prompt_overrides WHERE repository=?1`,
		"UPDATE clones SET repository=?2 WHERE repository=?1", "DELETE FROM picked WHERE identity=?1",
	}, earlier, current); e != nil {
		return Clone{}, e
	}
	return c, nil
}

// exec runs statements with the same arguments in one transaction.
func (r *Registry) exec(ctx context.Context, statements []string, args ...any) error {
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range statements {
		if _, e = tx.ExecContext(ctx, statement, args...); e != nil {
			return e
		}
	}
	return tx.Commit()
}

// Inspect reports the clone a path would register as, or why it cannot be
// registered, without registering it.
func Inspect(ctx context.Context, path string) (Clone, error) { return inspect(ctx, path) }

func inspect(ctx context.Context, path string) (Clone, error) { return inspectFor(ctx, path, "") }

// inspectFor probes a working tree. With repository empty it files the clone
// under the first GitHub remote, origin first. Otherwise it files it under
// that repository, which any of its GitHub remotes may name (a fork clone's
// upstream), and fails when none does.
func inspectFor(ctx context.Context, path, repository string) (Clone, error) {
	repo, e := gitlocal.Open(ctx, path, gitlocal.Options{})
	if e != nil {
		return Clone{}, ErrNotWorkingTree
	}
	defer func() { _ = repo.Close() }()
	p, e := filepath.EvalSymlinks(repo.Dir())
	if e != nil {
		return Clone{}, e
	}
	common, e := filepath.EvalSymlinks(repo.CommonDir())
	if e != nil {
		return Clone{}, e
	}
	remotes, e := repo.RemoteURLs(ctx)
	if e != nil {
		return Clone{}, errors.New("cannot read git remotes")
	}
	names := map[string]bool{}
	for _, remote := range remotes {
		names[remote.Name] = true
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		if name != "origin" {
			ordered = append(ordered, name)
		}
	}
	sort.Strings(ordered)
	if names["origin"] {
		ordered = append([]string{"origin"}, ordered...)
	}
	var others []string
	for _, name := range ordered {
		remote, e := repo.RemoteFetchURL(ctx, name)
		if e != nil {
			return Clone{}, errors.New("cannot read git remote")
		}
		gh, e := github.RepositoryFromRemote(remote)
		if e != nil {
			continue
		}
		if repository != "" && !strings.EqualFold(gh.String(), repository) {
			if other := strings.ToLower(gh.String()); !slices.Contains(others, other) {
				others = append(others, other)
			}
			continue
		}
		sum := sha256.Sum256([]byte(p))
		return Clone{ID: hex.EncodeToString(sum[:]), Path: p, CommonDir: common, Repository: strings.ToLower(gh.String()), Available: true}, nil
	}
	if repository != "" {
		return Clone{}, &RemoteMismatchError{Repository: strings.ToLower(repository), Remotes: others}
	}
	return Clone{}, errors.New("working tree has no GitHub remote")
}

// ErrNotWorkingTree reports a path that is missing or not in a git working
// tree.
var ErrNotWorkingTree = errors.New("path is unavailable or is not a git working tree")

// RemoteMismatchError is ErrNoMatchingRemote, naming the repositories the
// working tree's GitHub remotes name instead, origin's first; none when it
// has no GitHub remote.
type RemoteMismatchError struct {
	Repository string
	Remotes    []string
	// Unchecked is set when GitHub could not be asked whether a remote uses
	// an earlier name of Repository.
	Unchecked bool
}

func (e *RemoteMismatchError) Error() string        { return ErrNoMatchingRemote.Error() }
func (e *RemoteMismatchError) Is(target error) bool { return target == ErrNoMatchingRemote }

// Add registers an existing clone. Nil settings preserve existing settings;
// explicit settings replace the settings shared by all its repository's clones.
func (r *Registry) Add(ctx context.Context, path string, settings *Settings) (Clone, error) {
	return r.AddPatch(ctx, path, func(previous Settings) (Settings, error) {
		if settings != nil {
			return *settings, nil
		}
		return previous, nil
	})
}

// AddPatch atomically registers a clone and applies a settings edit. The edit
// runs under the writer transaction, so omitted fields cannot lose another
// process's concurrent edits. It must not call back into the registry.
func (r *Registry) AddPatch(ctx context.Context, path string, edit func(Settings) (Settings, error)) (Clone, error) {
	return r.addPatch(ctx, path, "", addGuard{}, edit)
}

// ErrNoMatchingRemote reports a working tree none of whose GitHub remotes
// names the requested repository.
var ErrNoMatchingRemote = errors.New("working tree has no remote for that repository")

// AddFor registers an existing clone for repository (github.com owner/name)
// when any of its GitHub remotes names it, not only origin: a fork clone whose
// upstream remote is the repository is filed under the repository, and the
// fork's own repository is neither registered nor picked. It fails with
// ErrNoMatchingRemote when no remote names the repository. Settings are as
// for Add.
func (r *Registry) AddFor(ctx context.Context, path, repository string, settings *Settings) (Clone, error) {
	gh, e := github.ParseRepository(repository)
	if e != nil || gh.Host != config.GitHubHost {
		return Clone{}, ErrInvalidRepository
	}
	return r.addFor(ctx, path, strings.ToLower(gh.String()), settings, addGuard{})
}

// Errors of AttachFound.
var (
	// ErrNotPicked reports a repository that is not picked.
	ErrNotPicked = errors.New("repository is not picked")
	// ErrClonedElsewhere reports a working tree, or a repository directory,
	// already registered to another repository.
	ErrClonedElsewhere = errors.New("clone is registered to another repository")
)

// AttachFound registers a clone the daemon found for a picked repository, as
// AddFor does, but never changes what is already stored: it fails with
// ErrNotPicked unless the repository is picked (checked in the same
// transaction, so an unpick in between is not undone) and with
// ErrClonedElsewhere when the path or its repository directory is registered
// to another repository, which AddFor would move.
func (r *Registry) AttachFound(ctx context.Context, path, repository string) (Clone, error) {
	gh, e := github.ParseRepository(repository)
	if e != nil || gh.Host != config.GitHubHost {
		return Clone{}, ErrInvalidRepository
	}
	return r.addFor(ctx, path, strings.ToLower(gh.String()), nil, addGuard{requirePicked: true, keepOthers: true})
}

func (r *Registry) addFor(ctx context.Context, path, repository string, settings *Settings, guard addGuard) (Clone, error) {
	return r.addPatch(ctx, path, repository, guard, func(previous Settings) (Settings, error) {
		if settings != nil {
			return *settings, nil
		}
		return previous, nil
	})
}

// addGuard makes addPatch fail instead of picking a repository or moving a
// registered clone.
type addGuard struct{ requirePicked, keepOthers bool }

func (r *Registry) addPatch(ctx context.Context, path, repository string, guard addGuard, edit func(Settings) (Settings, error)) (Clone, error) {
	c, e := inspectFor(ctx, path, repository)
	if e != nil {
		return Clone{}, e
	}
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return Clone{}, e
	}
	defer func() { _ = tx.Rollback() }()
	// Acquire the write lock before reading shared settings.
	_, e = tx.ExecContext(ctx, "INSERT INTO repositories VALUES (?,?) ON CONFLICT DO NOTHING", c.Repository, `{"automatic":null}`)
	if e != nil {
		return Clone{}, e
	}
	if guard.requirePicked {
		var picked int
		if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM picked WHERE identity=?", c.Repository).Scan(&picked); e != nil {
			return Clone{}, e
		}
		if picked == 0 {
			return Clone{}, ErrNotPicked
		}
	}
	if guard.keepOthers {
		var others int
		if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM clones WHERE (path=? OR common_dir=?) AND repository<>?", c.Path, c.CommonDir, c.Repository).Scan(&others); e != nil {
			return Clone{}, e
		}
		if others > 0 {
			return Clone{}, ErrClonedElsewhere
		}
	}
	var raw string
	if e = tx.QueryRowContext(ctx, "SELECT settings FROM repositories WHERE identity=?", c.Repository).Scan(&raw); e != nil {
		return Clone{}, e
	}
	if e = json.Unmarshal([]byte(raw), &c.Settings); e != nil {
		return Clone{}, e
	}
	c.Settings, e = edit(c.Settings)
	if e != nil {
		return Clone{}, e
	}
	data, e := json.Marshal(c.Settings)
	if e != nil {
		return Clone{}, e
	}
	if _, e = tx.ExecContext(ctx, "UPDATE repositories SET settings=? WHERE identity=?", string(data), c.Repository); e != nil {
		return Clone{}, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO clones VALUES (?,?,?,?) ON CONFLICT(path) DO UPDATE SET common_dir=excluded.common_dir,repository=excluded.repository`, c.ID, c.Path, c.CommonDir, c.Repository)
	if e != nil {
		return Clone{}, e
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO picked VALUES (?) ON CONFLICT DO NOTHING", c.Repository); e != nil {
		return Clone{}, e
	}
	if e = tx.Commit(); e != nil {
		return Clone{}, e
	}
	if r.Registered != nil {
		r.Registered(c)
	}
	return c, nil
}

// Remove accepts an ID or path, including a path that no longer exists.
func (r *Registry) Remove(ctx context.Context, idOrPath string) error {
	p, e := filepath.Abs(idOrPath)
	if e != nil {
		return e
	}
	if resolved, e := filepath.EvalSymlinks(p); e == nil {
		p = resolved
	}
	var removed []Clone
	if r.Removed != nil {
		if all, e := r.cloneRows(ctx, ""); e == nil {
			for _, c := range all {
				if c.ID == idOrPath || c.Path == p {
					removed = append(removed, c)
				}
			}
		}
	}
	// The repository stays picked: its picked row is not touched.
	result, e := r.db.ExecContext(ctx, "DELETE FROM clones WHERE id=? OR path=?", idOrPath, p)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e == nil && n == 0 {
		return ErrNotFound
	}
	if e == nil {
		r.removed(removed)
	}
	return e
}

func (r *Registry) removed(clones []Clone) {
	if r.Removed == nil {
		return
	}
	for _, c := range clones {
		r.Removed(c)
	}
}
func (r *Registry) List(ctx context.Context) ([]Clone, error) { return r.clones(ctx, "") }

// clones lists the registered clones of one repository, or of all for an
// empty identity, probing only those.
func (r *Registry) clones(ctx context.Context, repository string) ([]Clone, error) {
	result, e := r.cloneRows(ctx, repository)
	if e != nil {
		return nil, e
	}
	return result, probe(ctx, result)
}

// CloneRows lists the registered clones exactly as stored, without probing
// them: Available is false throughout. It runs no git.
func (r *Registry) CloneRows(ctx context.Context) ([]Clone, error) { return r.cloneRows(ctx, "") }

// PickedWithoutClones lists the picked repositories that have no clone
// registered. It reads the registry only and runs no git, so it is cheap
// enough to ask for every hook event.
func (r *Registry) PickedWithoutClones(ctx context.Context) ([]string, error) {
	names := []string{}
	if r.noSchema {
		return names, nil
	}
	rows, e := r.db.QueryContext(ctx, "SELECT r.identity FROM repositories r WHERE ("+r.pickedSQL()+") AND NOT EXISTS (SELECT 1 FROM clones c WHERE c.repository=r.identity) ORDER BY r.identity")
	if e != nil {
		return nil, e
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name string
		if e = rows.Scan(&name); e != nil {
			return nil, e
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func (r *Registry) cloneRows(ctx context.Context, repository string) ([]Clone, error) {
	if r.noSchema {
		return []Clone{}, nil
	}
	rows, e := r.db.QueryContext(ctx, `SELECT c.id,c.path,c.common_dir,c.repository,r.settings FROM clones c JOIN repositories r ON c.repository=r.identity WHERE ?1='' OR c.repository=?1 ORDER BY c.repository,c.path`, repository)
	if e != nil {
		return nil, e
	}
	result := []Clone{}
	for rows.Next() {
		var c Clone
		var raw string
		if e = rows.Scan(&c.ID, &c.Path, &c.CommonDir, &c.Repository, &raw); e != nil {
			_ = rows.Close()
			return nil, e
		}
		if e = json.Unmarshal([]byte(raw), &c.Settings); e != nil {
			_ = rows.Close()
			return nil, e
		}
		result = append(result, c)
	}
	e = rows.Err()
	_ = rows.Close()
	if e != nil {
		return nil, e
	}
	return result, nil
}

// probe sets Available on each clone from its current working tree.
func probe(ctx context.Context, clones []Clone) error {
	for i := range clones {
		if e := ctx.Err(); e != nil {
			return e
		}
		clones[i].Available = probed.available(ctx, clones[i])
	}
	return nil
}

// ProbeMaxAge bounds how long a clone's availability is reused while nothing
// it is read from changed: a change only global git configuration makes,
// such as a url.insteadOf rewrite, shows within this time.
const ProbeMaxAge = 5 * time.Minute

// probeCache remembers whether each registered clone was available, so the
// many registry reads of the daemon (the app, its tray, the schedulers and
// the background refresh) do not each run git in every clone. An answer is
// reused while what it is read from is as it was (see cloneStamp), for at
// most ProbeMaxAge. Concurrent misses for one clone share a single probe.
type probeCache struct {
	mu      sync.Mutex
	entries map[string]probeEntry
	flight  singleflight.Group
}

type probeEntry struct {
	stamp     string
	at        time.Time
	available bool
}

var probed = &probeCache{entries: map[string]probeEntry{}}

func (p *probeCache) available(ctx context.Context, clone Clone) bool {
	stamp, ok := cloneStamp(clone)
	if !ok {
		// The working tree or its git directory is gone.
		return false
	}
	key := clone.Path + "\x00" + clone.CommonDir + "\x00" + clone.Repository
	p.mu.Lock()
	entry, cached := p.entries[key]
	p.mu.Unlock()
	if cached && entry.stamp == stamp && time.Since(entry.at) < ProbeMaxAge {
		return entry.available
	}
	// The stamp is part of the flight key so a probe that began before a
	// change is never handed to a caller that saw the change. The probe runs
	// independently of the request that started it, so one cancelled request
	// cannot fail the others sharing it.
	ch := p.flight.DoChan(key+"\x00"+stamp, func() (any, error) {
		probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), probeTimeout)
		defer cancel()
		// Any remote may name the clone's repository, as AddFor allows.
		current, err := inspectFor(probeCtx, clone.Path, clone.Repository)
		available := err == nil && current.Path == clone.Path && current.CommonDir == clone.CommonDir && current.Repository == clone.Repository
		if probeCtx.Err() == nil {
			p.mu.Lock()
			p.entries[key] = probeEntry{stamp: stamp, at: time.Now(), available: available}
			p.mu.Unlock()
		}
		return available, nil
	})
	select {
	case res := <-ch:
		return res.Val.(bool)
	case <-ctx.Done():
		return false
	}
}

// probeTimeout bounds a shared probe.
const probeTimeout = time.Minute

// cloneStamp describes what a clone's availability is read from: that its
// directory and .git entry exist and are of the same kind, which git
// directory a linked worktree's .git file points at, and its common
// directory's config, where its remotes live. The directories' own
// modification times are left out: they move on every index write and every
// new top-level file without changing the answer. ok is false when one is
// missing.
func cloneStamp(clone Clone) (string, bool) {
	var b strings.Builder
	dotGit := filepath.Join(clone.Path, ".git")
	for _, path := range []string{clone.Path, dotGit, filepath.Join(clone.CommonDir, "config")} {
		info, err := os.Stat(path)
		if err != nil {
			return "", false
		}
		fmt.Fprintf(&b, "%s\x00%o\x00", path, info.Mode())
		switch {
		case info.IsDir():
		case path == dotGit:
			// A linked worktree's .git file names its git directory.
			data, err := os.ReadFile(path)
			if err != nil {
				return "", false
			}
			b.Write(data)
			b.WriteByte(0)
		default:
			fmt.Fprintf(&b, "%d\x00%d\x00", info.Size(), info.ModTime().UnixNano())
		}
	}
	return b.String(), true
}

// Update replaces shared repository settings without needing an available clone.
func (r *Registry) Update(ctx context.Context, id string, s Settings) error {
	data, e := json.Marshal(s)
	if e != nil {
		return e
	}
	res, e := r.db.ExecContext(ctx, `UPDATE repositories SET settings=? WHERE identity=(SELECT repository FROM clones WHERE id=?)`, string(data), id)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e == nil && n == 0 {
		return ErrNotFound
	}
	return e
}

// RepositorySettings reads one repository's shared settings without probing
// its clones; false means the repository is not picked.
func (r *Registry) RepositorySettings(ctx context.Context, repository string) (Settings, bool, error) {
	var s Settings
	if r.noSchema {
		return s, false, nil
	}
	var raw string
	e := r.db.QueryRowContext(ctx, `SELECT r.settings FROM repositories r WHERE r.identity=? AND (`+r.pickedSQL()+`)`, strings.ToLower(repository)).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return s, false, nil
	}
	if e == nil {
		e = json.Unmarshal([]byte(raw), &s)
	}
	return s, e == nil, e
}

const automaticPreferences = "automatic"

// Preferences reads the global preferences: DefaultPreferences until saved.
func (r *Registry) Preferences(ctx context.Context) (Preferences, error) {
	p := DefaultPreferences()
	if r.noSchema || r.noPreferences {
		return p, nil
	}
	var raw string
	e := r.db.QueryRowContext(ctx, "SELECT value FROM preferences WHERE key=?", automaticPreferences).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return p, nil
	}
	if e != nil {
		return p, e
	}
	return p, json.Unmarshal([]byte(raw), &p)
}

// UpdatePreferences atomically edits the global preferences. The edit runs
// under the writer transaction and must not call back into the registry.
func (r *Registry) UpdatePreferences(ctx context.Context, edit func(Preferences) Preferences) (Preferences, error) {
	return r.UpdatePreferencesIf(ctx, func(p Preferences) (Preferences, error) { return edit(p), nil })
}

// UpdatePreferencesIf is UpdatePreferences with an edit that may refuse: an
// error it returns rolls the transaction back, writes nothing and is
// returned as it is. The transaction holds the write lock from its start
// (appdb's immediate transactions), so a check the edit makes of the
// preferences it is given, such as the revision a proposal was read at, and
// the write are one step.
func (r *Registry) UpdatePreferencesIf(ctx context.Context, edit func(Preferences) (Preferences, error)) (Preferences, error) {
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return Preferences{}, e
	}
	defer func() { _ = tx.Rollback() }()
	// Take the write lock before reading.
	if _, e = tx.ExecContext(ctx, "INSERT INTO preferences VALUES (?,?) ON CONFLICT DO NOTHING", automaticPreferences, mustJSON(DefaultPreferences())); e != nil {
		return Preferences{}, e
	}
	var raw string
	if e = tx.QueryRowContext(ctx, "SELECT value FROM preferences WHERE key=?", automaticPreferences).Scan(&raw); e != nil {
		return Preferences{}, e
	}
	p := DefaultPreferences()
	if e = json.Unmarshal([]byte(raw), &p); e != nil {
		return Preferences{}, e
	}
	if p, e = edit(p); e != nil {
		return Preferences{}, e
	}
	if _, e = tx.ExecContext(ctx, "UPDATE preferences SET value=? WHERE key=?", mustJSON(p), automaticPreferences); e != nil {
		return Preferences{}, e
	}
	return p, tx.Commit()
}

func mustJSON(v any) string {
	data, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return string(data)
}

// pickedSQL is the condition, over repositories aliased r, for a picked
// repository: one with a picked row.
func (r *Registry) pickedSQL() string {
	if r.noPicked {
		return "0"
	}
	return "EXISTS (SELECT 1 FROM picked p WHERE p.identity=r.identity)"
}

// Repository is a picked repository with its settings and registered clones,
// of which there may be none.
type Repository struct {
	Repository string   `json:"repository"`
	Settings   Settings `json:"settings"`
	Clones     []Clone  `json:"clones"`
	// EarlierNames are recorded names GitHub forwards to the repository that
	// a clone's remote still uses, read from the clones' configuration on
	// each listing (AddForwarded records them).
	EarlierNames []string `json:"earlier_names,omitempty"`
}

// Pick registers a github.com repository by owner/name without a local
// clone. Nil settings keep the existing settings, or the default for a new
// repository; explicit settings replace them. Picking again is a no-op.
func (r *Registry) Pick(ctx context.Context, repository string, settings *Settings) (Repository, error) {
	gh, e := github.ParseRepository(repository)
	if e != nil || gh.Host != config.GitHubHost {
		return Repository{}, ErrInvalidRepository
	}
	identity := strings.ToLower(gh.String())
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return Repository{}, e
	}
	defer func() { _ = tx.Rollback() }()
	if _, e = tx.ExecContext(ctx, "INSERT INTO repositories VALUES (?,?) ON CONFLICT DO NOTHING", identity, `{"automatic":null}`); e != nil {
		return Repository{}, e
	}
	if settings != nil {
		if _, e = tx.ExecContext(ctx, "UPDATE repositories SET settings=? WHERE identity=?", mustJSON(*settings), identity); e != nil {
			return Repository{}, e
		}
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO picked VALUES (?) ON CONFLICT DO NOTHING", identity); e != nil {
		return Repository{}, e
	}
	if e = tx.Commit(); e != nil {
		return Repository{}, e
	}
	list, e := r.repositories(ctx, identity, r.clones)
	if e != nil {
		return Repository{}, e
	}
	if len(list) != 1 {
		return Repository{}, ErrNotFound
	}
	return list[0], nil
}

// Unpick removes a picked repository's clones and its picked row. The
// repositories row stays, as in Remove, so its settings survive a later pick.
func (r *Registry) Unpick(ctx context.Context, repository string) error {
	identity := strings.ToLower(repository)
	var removed []Clone
	if r.Removed != nil {
		removed, _ = r.cloneRows(ctx, identity)
	}
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback() }()
	if _, e = tx.ExecContext(ctx, "DELETE FROM clones WHERE repository=?", identity); e != nil {
		return e
	}
	unpicked, e := tx.ExecContext(ctx, "DELETE FROM picked WHERE identity=?", identity)
	if e != nil {
		return e
	}
	rows, e := unpicked.RowsAffected()
	if e != nil {
		return e
	}
	if rows == 0 {
		return ErrNotFound
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	r.removed(removed)
	return nil
}

// Repositories lists every picked repository by identity, with its clones
// probed for availability as List does.
func (r *Registry) Repositories(ctx context.Context) ([]Repository, error) {
	return r.repositories(ctx, "", r.clones)
}

// RepositoryRows is Repositories without probing the clones: Available is
// false throughout. It runs no git.
func (r *Registry) RepositoryRows(ctx context.Context) ([]Repository, error) {
	return r.repositories(ctx, "", r.cloneRows)
}

func (r *Registry) repositories(ctx context.Context, only string, listClones func(context.Context, string) ([]Clone, error)) ([]Repository, error) {
	result := []Repository{}
	if r.noSchema {
		return result, nil
	}
	query := "SELECT r.identity,r.settings FROM repositories r WHERE (" + r.pickedSQL() + ")"
	var args []any
	if only != "" {
		query += " AND r.identity=?"
		args = append(args, only)
	}
	rows, e := r.db.QueryContext(ctx, query+" ORDER BY r.identity", args...)
	if e != nil {
		return nil, e
	}
	byIdentity := map[string]int{}
	for rows.Next() {
		var v Repository
		var raw string
		if e = rows.Scan(&v.Repository, &raw); e == nil {
			e = json.Unmarshal([]byte(raw), &v.Settings)
		}
		if e != nil {
			_ = rows.Close()
			return nil, e
		}
		v.Clones = []Clone{}
		byIdentity[v.Repository] = len(result)
		result = append(result, v)
	}
	e = rows.Err()
	_ = rows.Close()
	if e != nil {
		return nil, e
	}
	clones, e := listClones(ctx, only)
	if e != nil {
		return nil, e
	}
	for _, c := range clones {
		if i, ok := byIdentity[c.Repository]; ok {
			result[i].Clones = append(result[i].Clones, c)
		}
	}
	// Only an earlier name a clone's remote still uses is worth a caution;
	// the remote is read again each time, so updating it clears the name.
	for i := range result {
		result[i].EarlierNames = usedEarlierNames(result[i].Repository, result[i].Clones)
	}
	return result, nil
}

// usedEarlierNames lists the recorded earlier names of repository that a remote
// of one of its clones still uses, sorted, read from the clones' git
// configuration now. A clone that cannot be read contributes nothing.
func usedEarlierNames(repository string, clones []Clone) []string {
	var names []string
	for _, c := range clones {
		for _, name := range gitlocal.RemoteEarlierNames(c.CommonDir) {
			if gitlocal.RenamedTo(name) == repository && !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	slices.Sort(names)
	return names
}

// UpdateRepositoryIf replaces a picked repository's shared settings with what
// edit makes of the stored ones, read and written in one immediate
// transaction: an error edit returns, such as a revision a proposal was read
// at that no longer holds, writes nothing and is returned as it is. An
// unpicked repository is ErrNotFound, as with UpdateRepository.
func (r *Registry) UpdateRepositoryIf(ctx context.Context, repository string, edit func(Settings) (Settings, error)) (Settings, error) {
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return Settings{}, e
	}
	defer func() { _ = tx.Rollback() }()
	identity := strings.ToLower(repository)
	var raw string
	e = tx.QueryRowContext(ctx, `SELECT r.settings FROM repositories r WHERE r.identity=? AND (`+r.pickedSQL()+`)`, identity).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return Settings{}, ErrNotFound
	}
	if e != nil {
		return Settings{}, e
	}
	var current Settings
	if e = json.Unmarshal([]byte(raw), &current); e != nil {
		return Settings{}, e
	}
	next, e := edit(current)
	if e != nil {
		return Settings{}, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE repositories SET settings=? WHERE identity=?`, mustJSON(next), identity); e != nil {
		return Settings{}, e
	}
	return next, tx.Commit()
}

// UpdateRepository replaces a picked repository's shared settings by identity.
func (r *Registry) UpdateRepository(ctx context.Context, repository string, s Settings) error {
	res, e := r.db.ExecContext(ctx, `UPDATE repositories AS r SET settings=? WHERE r.identity=? AND (`+r.pickedSQL()+`)`, mustJSON(s), strings.ToLower(repository))
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e == nil && n == 0 {
		return ErrNotFound
	}
	return e
}
