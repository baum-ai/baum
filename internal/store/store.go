// Package store keeps local review versions with immutable generation provenance.
// A SQLite transaction publishes the document and metadata together, comparing
// the previous version under the same write lock so concurrent generations
// cannot fork history. A refresh replaces the latest document only while its
// exact bytes are unchanged, so concurrent refreshes cannot lose each other,
// and only when it changes more than the refresh time.
// Triggers keep the index projection of each latest document in the summaries
// table current in the writer's own transaction; see Snapshot. Others renew
// each version's document revision with every write; see Revision.
//
// A stored document holds no hunk evidence: Put removes every indexed hunk's
// patch text and GitHub fragment (review.StripHunkEvidence), which the clone
// and the hunk's path and range give back when the document is read.
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

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/workstats"
	"github.com/baum-ai/baum/review"
)

var ErrNotFound = errors.New("no stored review version; generate a review in the baum desktop app first")
var ErrConflict = errors.New("the stored review changed during the operation; retry using the latest version")

// Key isolates pull requests of different GitHub repositories, including hosts.
type Key struct {
	Repository string
	Number     int
}

func (k Key) valid() bool        { return strings.TrimSpace(k.Repository) != "" && k.Number > 0 }
func (k Key) repository() string { return strings.ToLower(k.Repository) }

// Documents name owner/repository; the storage key also carries an Enterprise host.
func (k Key) documentRepository() string {
	parts := strings.Split(k.Repository, "/")
	if len(parts) == 3 {
		return parts[1] + "/" + parts[2]
	}
	return k.Repository
}

// Metadata records reproducible input identities and the generation that
// produced a version. Policy is the digest of the applied base REVIEW.md, null when absent.
type Metadata struct {
	PriceVersion    string            `json:"price_version,omitempty"`
	Accounting      *agent.Accounting `json:"accounting,omitempty"`
	CostReason      string            `json:"cost_reason,omitempty"`
	Version         int               `json:"version"`
	Head            string            `json:"head"`
	Base            string            `json:"base"`
	MergeBase       string            `json:"merge_base"`
	BundleDigest    string            `json:"bundle_digest"`
	Backend         string            `json:"backend"`
	BackendVersion  string            `json:"backend_version"`
	Model           string            `json:"model"`
	Schema          string            `json:"schema"`
	SchemaRevision  string            `json:"schema_revision"`
	PromptVersion   string            `json:"prompt_version"`
	PromptDigest    string            `json:"prompt_digest"`
	Policy          *string           `json:"policy"`
	GeneratedAt     string            `json:"generated_at"`
	Elapsed         time.Duration     `json:"elapsed_ns"`
	Calls           int               `json:"calls"`
	Tokens          int               `json:"tokens"`
	RecordedCostUSD *float64          `json:"recorded_cost_usd"`
	// Session is the local agent session that produced the version, kept with
	// its run directory so a chat can continue from it. Absent for hosted and
	// other runs, and in versions stored before it existed.
	Session *ReviewSession `json:"session,omitempty"`
}

// ReviewSession names a kept Claude Code session or Codex thread: resuming it
// has to run in Directory, the cwd the session was recorded under.
type ReviewSession struct {
	Backend   string `json:"backend"`
	ID        string `json:"id"`
	Directory string `json:"directory"`
}

type Version struct {
	Attachments []Attachment    `json:"-"`
	Metadata    Metadata        `json:"metadata"`
	Document    json.RawMessage `json:"document"`
	// Revision names Document's stored bytes (see Store.Revision), read with
	// them: a nonzero revision equal to one read before means the same bytes.
	// 0 when the store cannot vouch for one, and on versions not yet stored.
	Revision int64 `json:"-"`
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
	roots, err := Roots(commonDir)
	if err != nil {
		return nil, err
	}
	local, fallback := roots[0], roots[1]
	for _, root := range roots {
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

// Roots are the roots Open chooses between for a clone's common directory,
// in its order: the clone-local one, then the per-user data fallback.
func Roots(commonDir string) ([]string, error) {
	common, err := filepath.Abs(commonDir)
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(common); err == nil {
		common = resolved
	}
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" || !filepath.IsAbs(data) {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		data = filepath.Join(home, ".local", "share")
	}
	sum := sha256.Sum256([]byte(common))
	return []string{filepath.Join(common, "baum", "reviews"), filepath.Join(data, "baum", "reviews", hex.EncodeToString(sum[:]))}, nil
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
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS versions (repository TEXT NOT NULL, pr INTEGER NOT NULL, version INTEGER NOT NULL, metadata BLOB NOT NULL, document BLOB NOT NULL, PRIMARY KEY(repository,pr,version)); CREATE TABLE IF NOT EXISTS runs (id INTEGER PRIMARY KEY AUTOINCREMENT, repository TEXT NOT NULL, pr INTEGER NOT NULL, state TEXT NOT NULL, reason TEXT NOT NULL, started INTEGER NOT NULL, expires INTEGER NOT NULL, finished INTEGER NOT NULL); CREATE INDEX IF NOT EXISTS runs_by_pr ON runs(repository,pr,id);`)
	if err == nil {
		err = setupSummaries(context.Background(), db)
	}
	if err == nil {
		err = setupRevisions(context.Background(), db)
	}
	if err == nil {
		_, err = db.Exec(chatSchema)
	}
	if err == nil {
		// A move that cannot finish now (a full disk, a writer holding the lock
		// past busy_timeout) keeps its committed batches and resumes on the next
		// writable open; reads fall back to the rows not moved yet, so it does
		// not stop this open.
		_ = migrateAttachments(context.Background(), db)
	}
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
	// One read transaction, so the revision is the one of the bytes read.
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Version{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := currentRevisions(ctx, tx)
	if err != nil {
		return Version{}, err
	}
	column, join := revisionSelect(current)
	query := `SELECT v.metadata,v.document,` + column + ` FROM versions v` + join + ` WHERE v.repository=? AND v.pr=?`
	args := []any{k.repository(), k.Number}
	if n > 0 {
		query += ` AND v.version=?`
		args = append(args, n)
	}
	query += ` ORDER BY v.version DESC LIMIT 1`
	var m, doc []byte
	var revision int64
	err = tx.QueryRowContext(ctx, query, args...).Scan(&m, &doc, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return Version{}, ErrNotFound
	}
	if err != nil {
		return Version{}, err
	}
	v := Version{Document: doc, Revision: revision}
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

// Commits are the commits a stored version recorded.
type Commits struct {
	Key       Key
	Version   int
	Head      string
	Base      string
	MergeBase string
}

// Commits lists the recorded commits of every stored version of every pull
// request, without reading documents. A root with no database is ErrNotFound.
func (s *Store) Commits(ctx context.Context) ([]Commits, error) {
	db, err := s.database(false)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT repository,pr,version,metadata FROM versions ORDER BY repository,pr,version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Commits
	for rows.Next() {
		var c Commits
		var raw []byte
		if err := rows.Scan(&c.Key.Repository, &c.Key.Number, &c.Version, &raw); err != nil {
			return nil, err
		}
		var m struct {
			Head      string `json:"head"`
			Base      string `json:"base"`
			MergeBase string `json:"merge_base"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		c.Head, c.Base, c.MergeBase = m.Head, m.Base, m.MergeBase
		out = append(out, c)
	}
	return out, rows.Err()
}

// Put appends v only while previous is still the latest version; the zero
// Version means no history. The document is stored without hunk evidence
// (review.StripHunkEvidence), so the stored bytes differ from v.Document when
// it carries any. A refresh of previous during generation replaces
// only its live facts in place, so it does not reject the generated version:
// the next refresh brings the new version's live facts up to date.
// A failed or cancelled transaction leaves every published version untouched.
func (s *Store) Put(ctx context.Context, k Key, previous Version, v Version) error {
	expected := previous.Metadata.Version
	if !k.valid() || expected < 0 || v.Metadata.Version != expected+1 || !json.Valid(v.Document) || (expected > 0) != (len(previous.Document) > 0) {
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
	if !strings.EqualFold(doc.Review.Repository, k.documentRepository()) || doc.Review.PullRequest != k.Number || doc.Review.Version != v.Metadata.Version {
		return errors.New("document identity does not match stored version")
	}
	if err := validateAttachments(v); err != nil {
		return err
	}
	document, _, err := review.StripHunkEvidence(v.Document)
	if err != nil {
		return err
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
	result, err := tx.ExecContext(ctx, `INSERT INTO versions(repository,pr,version,metadata,document) SELECT ?,?,?,?,? WHERE COALESCE((SELECT MAX(version) FROM versions WHERE repository=? AND pr=?),0)=?`, k.repository(), k.Number, v.Metadata.Version, metadata, document, k.repository(), k.Number, expected)
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
	if err := saveAttachments(ctx, tx, k, v); err != nil {
		return err
	}
	return tx.Commit()
}

// RefreshCurrent replaces only the latest document, preserving its metadata.
// A document stored without hunk evidence is replaced without it; one stored
// with it, before Put removed it, is replaced as given, so no write takes the
// evidence away from a version saved with it.
// A document that differs from the previous one only in
// review.evidence_refreshed_at (review.RefreshChanged) is not written, so
// the stored time is that of the last refresh that changed something.
// Both the version and exact previous bytes fence concurrent generation and
// refresh, a refresh that writes nothing included. A running generation does
// not block it: live facts refresh independently, and the generated version
// still publishes over it.
func (s *Store) RefreshCurrent(ctx context.Context, k Key, previous Version, document []byte) error {
	return s.replaceCurrent(ctx, k, previous, document, "refresh", "refresh")
}

// CompleteCurrent replaces the latest document with the same version whose
// failed section details were written again, preserving its metadata. It is
// fenced as RefreshCurrent is: the generation identity (repository, pull
// request, version, head, base, merge base and generation time) must not
// change, and the version and exact previous bytes must still be the
// latest's, so a refresh or a new version in the meantime makes it a
// conflict. Like a refresh, a completion that changes nothing is not written.
func (s *Store) CompleteCurrent(ctx context.Context, k Key, previous Version, document []byte) error {
	return s.replaceCurrent(ctx, k, previous, document, "completion", "complete it")
}

func (s *Store) replaceCurrent(ctx context.Context, k Key, previous Version, document []byte, what, again string) error {
	if !k.valid() || previous.Metadata.Version <= 0 || !json.Valid(document) {
		return fmt.Errorf("invalid %s of the current version", what)
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
	if before != after || !strings.EqualFold(after.Review.Repository, k.documentRepository()) || after.Review.PullRequest != k.Number || after.Review.Version != previous.Metadata.Version {
		return fmt.Errorf("%s changed generation identity", what)
	}
	if _, carried, err := review.StripHunkEvidence(previous.Document); err != nil {
		return err
	} else if !carried {
		if document, _, err = review.StripHunkEvidence(document); err != nil {
			return err
		}
	}
	if !review.RefreshChanged(previous.Document, document) {
		return s.unchanged(ctx, k, previous, again)
	}
	db, err := s.database(true)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE versions SET document=? WHERE repository=? AND pr=? AND version=? AND document=? AND version=(SELECT MAX(version) FROM versions WHERE repository=? AND pr=?)`, document, k.repository(), k.Number, previous.Metadata.Version, []byte(previous.Document), k.repository(), k.Number)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%w; %s again using the latest document", ErrConflict, again)
	}
	return tx.Commit()
}

// unchanged fences a replacement that would change nothing but the refresh
// time as replaceCurrent fences a write, reading only: the previous version
// and its exact bytes must still be the latest.
func (s *Store) unchanged(ctx context.Context, k Key, previous Version, again string) error {
	workstats.Inc("store.unchanged_replacements")
	db, err := s.database(false)
	if err != nil {
		return err
	}
	defer db.Close()
	var latest int
	err = db.QueryRowContext(ctx, `SELECT 1 FROM versions WHERE repository=? AND pr=? AND version=? AND document=? AND version=(SELECT MAX(version) FROM versions WHERE repository=? AND pr=?)`, k.repository(), k.Number, previous.Metadata.Version, []byte(previous.Document), k.repository(), k.Number).Scan(&latest)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w; %s again using the latest document", ErrConflict, again)
	}
	return err
}
