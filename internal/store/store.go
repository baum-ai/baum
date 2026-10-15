// Package store keeps local review versions with immutable generation provenance.
// A SQLite transaction publishes the document and metadata together, comparing the previous version
// under the same write lock so concurrent generations cannot fork history.
package store

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
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("no stored review version; run baum generate first")
var ErrConflict = errors.New("the stored review changed during the operation; retry using the latest version")

// Key isolates pull requests of different GitHub repositories, including hosts.
type Key struct {
	Repository string
	Number     int
}

func (k Key) valid() bool        { return strings.TrimSpace(k.Repository) != "" && k.Number > 0 }
func (k Key) repository() string { return strings.ToLower(k.Repository) }

// Metadata records reproducible input identities and the generation that
// produced a version. Policy is the digest of the applied base REVIEW.md, null when absent.
type Metadata struct {
	Version        int           `json:"version"`
	Head           string        `json:"head"`
	Base           string        `json:"base"`
	MergeBase      string        `json:"merge_base"`
	BundleDigest   string        `json:"bundle_digest"`
	Backend        string        `json:"backend"`
	BackendVersion string        `json:"backend_version"`
	Model          string        `json:"model"`
	Schema         string        `json:"schema"`
	SchemaRevision string        `json:"schema_revision"`
	PromptVersion  string        `json:"prompt_version"`
	PromptDigest   string        `json:"prompt_digest"`
	Policy         *string       `json:"policy"`
	GeneratedAt    string        `json:"generated_at"`
	Elapsed        time.Duration `json:"elapsed_ns"`
	Calls          int           `json:"calls"`
	Tokens         int           `json:"tokens"`
}

type Version struct {
	Metadata Metadata        `json:"metadata"`
	Document json.RawMessage `json:"document"`
}
type Store struct{ root string }

func (s *Store) Root() string { return s.root }

// Open selects the store under the common git directory. An existing store
// always wins, even when read-only, rather than hiding its history. New stores
// fall back to XDG_DATA_HOME/baum/reviews/<clone digest> when git is read-only.
// Override is an explicit root: errors there never silently select another one.
// Read-only opens neither create directories nor change database contents.
func Open(commonDir, override string, write bool) (*Store, error) {
	if override != "" {
		return openRoot(override, write)
	}
	common, err := filepath.Abs(commonDir)
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(common); err == nil {
		common = resolved
	}
	local := filepath.Join(common, "baum", "reviews")
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" || !filepath.IsAbs(data) {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		data = filepath.Join(home, ".local", "share")
	}
	sum := sha256.Sum256([]byte(common))
	fallback := filepath.Join(data, "baum", "reviews", hex.EncodeToString(sum[:]))
	for _, root := range []string{local, fallback} {
		if _, err := os.Stat(filepath.Join(root, "versions.sqlite")); err == nil {
			return openRoot(root, write)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if !write {
		return &Store{root: local}, nil
	}
	s, err := openRoot(local, true)
	if err == nil {
		return s, nil
	}
	if !errors.Is(err, os.ErrPermission) && !errors.Is(err, syscall.EROFS) {
		return nil, err
	}
	return openRoot(fallback, true)
}
func openRoot(root string, write bool) (*Store, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	s := &Store{root: root}
	if !write {
		return s, nil
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	// SQLite must not create a world-readable file, even under a permissive umask.
	f, err := os.OpenFile(s.path(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	db, err := s.database(true)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS versions (repository TEXT NOT NULL, pr INTEGER NOT NULL, version INTEGER NOT NULL, metadata BLOB NOT NULL, document BLOB NOT NULL, PRIMARY KEY(repository,pr,version))`)
	return s, err
}
func (s *Store) path() string { return filepath.Join(s.root, "versions.sqlite") }
func (s *Store) database(write bool) (*sql.DB, error) {
	if _, err := os.Stat(s.path()); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	// URI filename escaping keeps a path containing '?' or '#' literal.
	mode := "ro"
	if write {
		mode = "rw"
	}
	uriPath := filepath.ToSlash(s.path())
	if filepath.VolumeName(s.path()) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: uriPath}).String()+"?mode="+mode+"&_pragma=busy_timeout(10000)")
	if err == nil {
		db.SetMaxOpenConns(1)
	}
	return db, err
}

func (s *Store) Latest(ctx context.Context, k Key) (Version, error) { return s.load(ctx, k, 0) }
func (s *Store) Get(ctx context.Context, k Key, version int) (Version, error) {
	if version <= 0 {
		return Version{}, fmt.Errorf("version must be positive")
	}
	return s.load(ctx, k, version)
}
func (s *Store) load(ctx context.Context, k Key, n int) (Version, error) {
	if !k.valid() {
		return Version{}, fmt.Errorf("invalid repository or pull request")
	}
	db, err := s.database(false)
	if err != nil {
		return Version{}, err
	}
	defer db.Close()
	query := `SELECT metadata,document FROM versions WHERE repository=? AND pr=?`
	args := []any{k.repository(), k.Number}
	if n > 0 {
		query += ` AND version=?`
		args = append(args, n)
	}
	query += ` ORDER BY version DESC LIMIT 1`
	var m, doc []byte
	err = db.QueryRowContext(ctx, query, args...).Scan(&m, &doc)
	if errors.Is(err, sql.ErrNoRows) {
		return Version{}, ErrNotFound
	}
	if err != nil {
		return Version{}, err
	}
	v := Version{Document: doc}
	err = json.Unmarshal(m, &v.Metadata)
	return v, err
}

// Versions returns metadata newest first without reading document bodies.
func (s *Store) Versions(ctx context.Context, k Key) ([]Metadata, error) {
	if !k.valid() {
		return nil, fmt.Errorf("invalid repository or pull request")
	}
	db, err := s.database(false)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT metadata FROM versions WHERE repository=? AND pr=? ORDER BY version DESC`, k.repository(), k.Number)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Metadata{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var m Metadata
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Put appends only when expected is still latest (zero means no history).
// A failed or cancelled transaction leaves every published version untouched.
func (s *Store) Put(ctx context.Context, k Key, expected int, v Version) error {
	if !k.valid() || expected < 0 || v.Metadata.Version != expected+1 || !json.Valid(v.Document) {
		return errors.New("invalid stored version")
	}
	var doc struct {
		Review struct {
			Repository  string `json:"repository"`
			PullRequest int    `json:"pull_request"`
			Version     int    `json:"version"`
		} `json:"review"`
	}
	if err := json.Unmarshal(v.Document, &doc); err != nil {
		return err
	}
	if !strings.EqualFold(doc.Review.Repository, k.Repository) || doc.Review.PullRequest != k.Number || doc.Review.Version != v.Metadata.Version {
		return errors.New("document identity does not match stored version")
	}
	metadata, err := json.Marshal(v.Metadata)
	if err != nil {
		return err
	}
	db, err := s.database(true)
	if err != nil {
		return err
	}
	defer db.Close()
	// The first statement acquires SQLite's writer lock and checks history in
	// that same statement; there is no deferred read-to-write upgrade race.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `INSERT INTO versions(repository,pr,version,metadata,document) SELECT ?,?,?,?,? WHERE COALESCE((SELECT MAX(version) FROM versions WHERE repository=? AND pr=?),0)=?`, k.repository(), k.Number, v.Metadata.Version, metadata, []byte(v.Document), k.repository(), k.Number, expected)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return tx.Commit()
}

// RefreshCurrent replaces only the latest document, preserving its metadata.
// Both the version and exact previous bytes fence concurrent generation and refresh.
func (s *Store) RefreshCurrent(ctx context.Context, k Key, previous Version, document []byte) error {
	if !k.valid() || previous.Metadata.Version <= 0 || !json.Valid(document) {
		return errors.New("invalid refreshed version")
	}
	var before, after struct {
		Review struct {
			Repository  string `json:"repository"`
			PullRequest int    `json:"pull_request"`
			Version     int    `json:"version"`
			Head        string `json:"head"`
			Base        string `json:"base"`
			MergeBase   string `json:"merge_base"`
			GeneratedAt string `json:"generated_at"`
		} `json:"review"`
	}
	if err := json.Unmarshal(previous.Document, &before); err != nil {
		return err
	}
	if err := json.Unmarshal(document, &after); err != nil {
		return err
	}
	if before != after || !strings.EqualFold(after.Review.Repository, k.Repository) || after.Review.PullRequest != k.Number || after.Review.Version != previous.Metadata.Version {
		return errors.New("refresh changed generation identity")
	}
	db, err := s.database(true)
	if err != nil {
		return err
	}
	defer db.Close()
	result, err := db.ExecContext(ctx, `UPDATE versions SET document=? WHERE repository=? AND pr=? AND version=? AND document=? AND version=(SELECT MAX(version) FROM versions WHERE repository=? AND pr=?)`, document, k.repository(), k.Number, previous.Metadata.Version, []byte(previous.Document), k.repository(), k.Number)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%w; refresh again using the latest document", ErrConflict)
	}
	return nil
}
