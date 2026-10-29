// Package repos stores explicitly registered local clones and repository settings.
package repos

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/gitlocal"
)

// ErrNotFound reports a clone or picked repository the registry does not hold.
var ErrNotFound = errors.New("registered clone or picked repository not found")

// ErrInvalidRepository reports a name that is not a github.com owner/name.
var ErrInvalidRepository = errors.New("repository must be a github.com owner/name")

// Settings are shared by all clones of a repository. Nil overrides inherit
// global settings. Automatic is the repository's choice under the global
// master switch (see AutomaticEnabled): nil follows it, false turns the
// repository off, true is an explicit on that still stops while the global
// switch is off. Registration itself starts no generation. A daily_cap an
// older baum stored is ignored on read and dropped on the next write: baum
// sets no spending limits, since it cannot price what a subscription bills.
type Settings struct {
	Automatic *bool           `json:"automatic"`
	Backend   *config.Backend `json:"backend,omitempty"`
	Model     *string         `json:"model,omitempty"`
	// Decisions is the repository's choice for local decision capture under
	// the global switches (see DecisionsEnabled): nil and true follow them,
	// false turns the repository off.
	Decisions *bool `json:"decisions,omitempty"`
}

func (s Settings) Validate() error {
	if s.Backend != nil && !s.Backend.Valid() {
		return errors.New("invalid backend")
	}
	if s.Model != nil && (strings.TrimSpace(*s.Model) == "" || len(*s.Model) > 256) {
		return errors.New("model must be nonempty and at most 256 bytes")
	}
	return nil
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
// the feature log. A nil Enabled is on. Model, when set, replaces the
// backend's default decision model.
type DecisionPreferences struct {
	Enabled *bool   `json:"enabled,omitempty"`
	Model   *string `json:"model,omitempty"`
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
	// FixBackend and FixModel choose the agent that fixes pull requests
	// (ResolveFix). A nil FixBackend means the same as reviews; an empty
	// FixModel is the fix backend's default model.
	FixBackend *config.Backend `json:"fix_backend,omitempty"`
	FixModel   *string         `json:"fix_model,omitempty"`
	// PullRequests narrows which pull requests get automatic reviews:
	// PullRequestsAuthored, PullRequestsRequested, PullRequestsAll, or empty
	// for both authored and requested.
	PullRequests string `json:"pull_requests,omitempty"`
	// AgentEnv, when it sets any variable, replaces the config file's
	// agent_env (config.Config.AgentEnv).
	AgentEnv map[string]string `json:"agent_env,omitempty"`
	// Decisions configures local decision capture.
	Decisions DecisionPreferences `json:"decisions,omitzero"`
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
	if p.FixBackend != nil && *p.FixBackend != config.BackendClaude && *p.FixBackend != config.BackendCodex {
		return errors.New("fixes run with Claude Code or Codex only")
	}
	if p.FixModel != nil && len(*p.FixModel) > 256 {
		return errors.New("fix model must be at most 256 bytes")
	}
	if p.FixBackend == nil && p.FixModel != nil && strings.TrimSpace(*p.FixModel) != "" {
		return errors.New("choose the agent for fixes before its model")
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

// Resolve overlays the app's backend, model and agent environment on the
// config file's.
func (p Preferences) Resolve(global config.Config) config.Config {
	if len(p.AgentEnv) > 0 {
		global.AgentEnv = p.AgentEnv
	}
	if p.Backend != nil {
		global.Backend = *p.Backend
	}
	if p.Model != nil {
		global.Model = strings.TrimSpace(*p.Model)
	}
	return global
}

// ResolveFix gives the config a fix runs with, from review, the config
// already resolved for reviews (the global preferences and the repository's
// overrides). A set FixBackend wins over the repository's own review backend
// and model, and takes FixModel (empty for the backend's default); without
// one the fix follows the reviews, repository overrides included.
func (p Preferences) ResolveFix(review config.Config) config.Config {
	if p.FixBackend == nil {
		return review
	}
	review.Backend = *p.FixBackend
	review.Model = ""
	if p.FixModel != nil {
		review.Model = strings.TrimSpace(*p.FixModel)
	}
	return review
}

// DefaultPreferences apply until the user changes them: automatic generation
// on.
func DefaultPreferences() Preferences { return Preferences{Automatic: true} }

// Resolve overlays generation choices without changing credentials or budgets.
func (s Settings) Resolve(global config.Config) config.Config {
	if s.Backend != nil {
		global.Backend = *s.Backend
	}
	if s.Model != nil {
		global.Model = *s.Model
	}
	return global
}

type Clone struct {
	ID         string   `json:"id"`
	Path       string   `json:"path"`
	CommonDir  string   `json:"common_dir"`
	Repository string   `json:"repository"`
	Settings   Settings `json:"settings"`
	Available  bool     `json:"available"`
}
type Registry struct {
	db *sql.DB
	// noSchema marks a read-only registry whose file has no tables yet.
	noSchema bool
	// legacy marks a read-only registry not yet migrated to schemaVersion,
	// whose stored automatic=false only ever meant the old default.
	legacy bool
	// noPicked marks a read-only registry without the picked table.
	noPicked bool
	// noPreferences marks a read-only registry without saved preferences.
	noPreferences bool
}

// schemaVersion is the registry's PRAGMA user_version. Version 1 makes a null
// automatic setting follow the global preference; before it, registration
// stored automatic=false for every repository.
const schemaVersion = 1

func Path() (string, error) {
	p, e := config.Path()
	return filepath.Join(filepath.Dir(p), "repos.sqlite"), e
}

// Open creates a private registry beside config.toml. SQLite serializes writers
// across processes. The rollback journal inherits the private database mode.
func Open(path string) (*Registry, error) {
	if path == "" {
		var e error
		path, e = Path()
		if e != nil {
			return nil, e
		}
	}
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	e = f.Chmod(0600)
	ce := f.Close()
	if e != nil {
		return nil, e
	}
	if ce != nil {
		return nil, ce
	}
	uri, e := databaseURI(path)
	if e != nil {
		return nil, e
	}
	db, e := sql.Open("sqlite", uri+"?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)")
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	_, e = db.Exec(`CREATE TABLE IF NOT EXISTS repositories(identity TEXT PRIMARY KEY,settings TEXT NOT NULL); CREATE TABLE IF NOT EXISTS clones(id TEXT PRIMARY KEY,path TEXT NOT NULL UNIQUE,common_dir TEXT NOT NULL,repository TEXT NOT NULL REFERENCES repositories(identity)); CREATE TABLE IF NOT EXISTS preferences(key TEXT PRIMARY KEY,value TEXT NOT NULL); CREATE TABLE IF NOT EXISTS picked(identity TEXT PRIMARY KEY REFERENCES repositories(identity) ON DELETE CASCADE); CREATE TABLE IF NOT EXISTS aliases(alias TEXT PRIMARY KEY,repository TEXT NOT NULL);`)
	if e == nil {
		e = migrate(context.Background(), db)
	}
	if e == nil {
		e = loadAliases(db)
	}
	if e != nil {
		_ = db.Close()
		return nil, e
	}
	return &Registry{db: db}, nil
}

// migrate brings the registry to schemaVersion in one immediate write
// transaction, so concurrent processes migrate it exactly once. Before
// version 1 the app had no automatic control and registration stored
// automatic=false for every repository, so a stored false becomes "follow the
// global preference"; a stored true was an explicit choice and stays.
func migrate(ctx context.Context, db *sql.DB) (err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()
	var version int
	if err = conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version < schemaVersion {
		if err = migrateAutomatic(ctx, conn); err != nil {
			return err
		}
		if _, err = conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", schemaVersion)); err != nil {
			return err
		}
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

func migrateAutomatic(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, "SELECT identity,settings FROM repositories")
	if err != nil {
		return err
	}
	updates := map[string]string{}
	for rows.Next() {
		var identity, raw string
		var s Settings
		if err = rows.Scan(&identity, &raw); err == nil {
			err = json.Unmarshal([]byte(raw), &s)
		}
		if err != nil {
			_ = rows.Close()
			return err
		}
		if s.Automatic != nil && !*s.Automatic {
			s.Automatic = nil
			updates[identity] = mustJSON(s)
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for identity, settings := range updates {
		if _, err = conn.ExecContext(ctx, "UPDATE repositories SET settings=? WHERE identity=?", settings, identity); err != nil {
			return err
		}
	}
	return nil
}

func databaseURI(path string) (string, error) {
	absolute, e := filepath.Abs(path)
	if e != nil {
		return "", e
	}
	uriPath := filepath.ToSlash(absolute)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	return (&url.URL{Scheme: "file", Path: uriPath}).String(), nil
}

// OpenReadOnly opens an existing registry only to list it. Unlike Open it
// creates no directory, file or table and changes no file mode, and SQLite
// opens the file read-only, so it writes no journal. A missing file is an error
// wrapping fs.ErrNotExist; a file without tables lists no clones.
func OpenReadOnly(path string) (*Registry, error) {
	if path == "" {
		var e error
		path, e = Path()
		if e != nil {
			return nil, e
		}
	}
	if _, e := os.Stat(path); e != nil {
		return nil, e
	}
	uri, e := databaseURI(path)
	if e != nil {
		return nil, e
	}
	db, e := sql.Open("sqlite", uri+"?mode=ro&_pragma=busy_timeout(10000)")
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	var tables, preferences, picked, version int
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
	if e = db.QueryRow(`PRAGMA user_version`).Scan(&version); e != nil {
		_ = db.Close()
		return nil, e
	}
	// A registry from before aliases has none to load.
	_ = loadAliases(db)
	return &Registry{db: db, noSchema: tables < 2, noPreferences: preferences == 0, legacy: version < schemaVersion, noPicked: picked == 0}, nil
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
// could not check or does not show the token is kept.
func (r *Registry) RecheckAliases(ctx context.Context, repository string, current func(context.Context, github.Repository) (string, error)) error {
	rows, e := r.db.QueryContext(ctx, "SELECT alias,repository FROM aliases WHERE ?1='' OR repository=lower(?1)", repository)
	if e != nil {
		return e
	}
	var aliases [][2]string
	for rows.Next() {
		var alias [2]string
		if e = rows.Scan(&alias[0], &alias[1]); e != nil {
			_ = rows.Close()
			return e
		}
		aliases = append(aliases, alias)
	}
	if e = errors.Join(rows.Err(), rows.Close()); e != nil {
		return e
	}
	for _, alias := range aliases {
		earlier, e := github.ParseRepository(alias[0])
		if e != nil {
			continue
		}
		now, e := current(ctx, earlier)
		if e != nil || strings.EqualFold(now, alias[1]) {
			continue
		}
		// Renamed again since: both names still lead to the same repository.
		if recorded, e := github.ParseRepository(alias[1]); e != nil {
			continue
		} else if then, e := current(ctx, recorded); e != nil || strings.EqualFold(now, then) {
			continue
		}
		// Unless AddForwarded recorded it anew meanwhile.
		result, e := r.db.ExecContext(ctx, "DELETE FROM aliases WHERE alias=? AND repository=?", alias[0], alias[1])
		if e != nil {
			return e
		}
		if n, _ := result.RowsAffected(); n > 0 {
			gitlocal.RemoveRenamed(alias[0])
		}
	}
	return nil
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
			return *settings, settings.Validate()
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
			return *settings, settings.Validate()
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
	if e = c.Settings.Validate(); e != nil {
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
	return c, tx.Commit()
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
	// The repository stays picked: its picked row is not touched.
	result, e := r.db.ExecContext(ctx, "DELETE FROM clones WHERE id=? OR path=?", idOrPath, p)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e == nil && n == 0 {
		return ErrNotFound
	}
	return e
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
		if r.legacy && c.Settings.Automatic != nil && !*c.Settings.Automatic {
			c.Settings.Automatic = nil
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
		// Any remote may name the clone's repository, as AddFor allows.
		current, err := inspectFor(ctx, clones[i].Path, clones[i].Repository)
		clones[i].Available = err == nil && current.Path == clones[i].Path && current.CommonDir == clones[i].CommonDir && current.Repository == clones[i].Repository
	}
	return nil
}

// Update replaces shared repository settings without needing an available clone.
func (r *Registry) Update(ctx context.Context, id string, s Settings) error {
	if e := s.Validate(); e != nil {
		return e
	}
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
	if e == nil && r.legacy && s.Automatic != nil && !*s.Automatic {
		s.Automatic = nil
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
	p = edit(p)
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
	// EarlierNames are names GitHub forwards to the repository that clones'
	// remotes were registered with (AddForwarded).
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
	if settings != nil {
		if e = settings.Validate(); e != nil {
			return Repository{}, e
		}
	}
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
	list, e := r.repositories(ctx, identity)
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
	return tx.Commit()
}

// Repositories lists every picked repository by identity, with its clones
// probed for availability as List does.
func (r *Registry) Repositories(ctx context.Context) ([]Repository, error) {
	return r.repositories(ctx, "")
}

func (r *Registry) repositories(ctx context.Context, only string) ([]Repository, error) {
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
		if r.legacy && v.Settings.Automatic != nil && !*v.Settings.Automatic {
			v.Settings.Automatic = nil
		}
		v.Clones = []Clone{}
		v.EarlierNames = gitlocal.EarlierNames(v.Repository)
		byIdentity[v.Repository] = len(result)
		result = append(result, v)
	}
	e = rows.Err()
	_ = rows.Close()
	if e != nil {
		return nil, e
	}
	clones, e := r.clones(ctx, only)
	if e != nil {
		return nil, e
	}
	for _, c := range clones {
		if i, ok := byIdentity[c.Repository]; ok {
			result[i].Clones = append(result[i].Clones, c)
		}
	}
	return result, nil
}

// UpdateRepository replaces a picked repository's shared settings by identity.
func (r *Registry) UpdateRepository(ctx context.Context, repository string, s Settings) error {
	if e := s.Validate(); e != nil {
		return e
	}
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
