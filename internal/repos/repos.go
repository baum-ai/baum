// Package repos stores explicitly registered local clones and repository settings.
package repos

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/gitlocal"
)

var ErrNotFound = errors.New("registered clone not found")

// Settings are shared by all clones of a repository. Nil overrides inherit
// global settings; a nil cap is unset and zero permits no automatic spending.
// A future scheduler enforces the cap; registration starts no generation.
type Settings struct {
	Automatic bool            `json:"automatic"`
	DailyCap  *float64        `json:"daily_cap,omitempty"`
	Backend   *config.Backend `json:"backend,omitempty"`
	Model     *string         `json:"model,omitempty"`
}

func (s Settings) Validate() error {
	if s.DailyCap != nil && (math.IsNaN(*s.DailyCap) || math.IsInf(*s.DailyCap, 0) || *s.DailyCap < 0) {
		return errors.New("daily cap must be finite and nonnegative")
	}
	if s.Backend != nil && !s.Backend.Valid() {
		return errors.New("invalid backend")
	}
	if s.Model != nil && (strings.TrimSpace(*s.Model) == "" || len(*s.Model) > 256) {
		return errors.New("model must be nonempty and at most 256 bytes")
	}
	return nil
}

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
}

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
	_, e = db.Exec(`CREATE TABLE IF NOT EXISTS repositories(identity TEXT PRIMARY KEY,settings TEXT NOT NULL); CREATE TABLE IF NOT EXISTS clones(id TEXT PRIMARY KEY,path TEXT NOT NULL UNIQUE,common_dir TEXT NOT NULL,repository TEXT NOT NULL REFERENCES repositories(identity));`)
	if e != nil {
		_ = db.Close()
		return nil, e
	}
	return &Registry{db: db}, nil
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
	var tables int
	if e = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('clones','repositories')`).Scan(&tables); e != nil {
		_ = db.Close()
		return nil, e
	}
	return &Registry{db: db, noSchema: tables < 2}, nil
}

func (r *Registry) Close() error { return r.db.Close() }
func inspect(ctx context.Context, path string) (Clone, error) {
	repo, e := gitlocal.Open(ctx, path, gitlocal.Options{})
	if e != nil {
		return Clone{}, errors.New("path is unavailable or is not a git working tree")
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
	for _, name := range ordered {
		remote, e := repo.RemoteFetchURL(ctx, name)
		if e != nil {
			return Clone{}, errors.New("cannot read git remote")
		}
		gh, e := github.RepositoryFromRemote(remote)
		if e != nil {
			continue
		}
		sum := sha256.Sum256([]byte(p))
		return Clone{ID: hex.EncodeToString(sum[:]), Path: p, CommonDir: common, Repository: strings.ToLower(gh.String()), Available: true}, nil
	}
	return Clone{}, errors.New("working tree has no GitHub remote")
}

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
	c, e := inspect(ctx, path)
	if e != nil {
		return Clone{}, e
	}
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return Clone{}, e
	}
	defer func() { _ = tx.Rollback() }()
	// Acquire the write lock before reading shared settings.
	_, e = tx.ExecContext(ctx, "INSERT INTO repositories VALUES (?,?) ON CONFLICT DO NOTHING", c.Repository, `{"automatic":false}`)
	if e != nil {
		return Clone{}, e
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
func (r *Registry) List(ctx context.Context) ([]Clone, error) {
	if r.noSchema {
		return []Clone{}, nil
	}
	rows, e := r.db.QueryContext(ctx, `SELECT c.id,c.path,c.common_dir,c.repository,r.settings FROM clones c JOIN repositories r ON c.repository=r.identity ORDER BY c.repository,c.path`)
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
	for i := range result {
		if e = ctx.Err(); e != nil {
			return nil, e
		}
		current, err := inspect(ctx, result[i].Path)
		result[i].Available = err == nil && current.Path == result[i].Path && current.CommonDir == result[i].CommonDir && current.Repository == result[i].Repository
	}
	return result, nil
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
