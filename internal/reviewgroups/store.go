// Package reviewgroups keeps review groups: named sets of open pull requests
// of one repository that a person reviews together, and the versions of
// their combined review. See README.md.
package reviewgroups

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/baum-ai/baum/internal/appdb"
	"github.com/baum-ai/baum/internal/store"
)

// MaxMembers bounds a group's pull requests; MinMembers is the fewest a group
// keeps, since one pull request is reviewed on its own.
const (
	MinMembers = 2
	MaxMembers = 8
	// MaxNameLength is the schema's bound on a stack title, which the
	// group's name becomes.
	MaxNameLength = 80
)

// ErrNotFound is a group that does not exist or was deleted.
var ErrNotFound = errors.New("review group not found")

// ErrConflict is a version that another generation published first.
var ErrConflict = errors.New("the group review changed during the operation; generate again")

// InvalidError is a group definition the rules refuse; Code is the
// machine-readable reason the API answers with.
type InvalidError struct {
	Code    string
	Message string
}

func (e *InvalidError) Error() string { return e.Message }

// The codes of InvalidError.
const (
	CodeInvalid         = "invalid_group"
	CodeCrossRepository = "cross_repository"
	CodeNotPicked       = "not_picked"
)

func invalid(format string, args ...any) error {
	return &InvalidError{Code: CodeInvalid, Message: fmt.Sprintf(format, args...)}
}

// Group is one review group's definition.
type Group struct {
	ID string `json:"id"`
	// Repository is the registry's identity of the members' repository:
	// lowercase owner/name, or host/owner/name off github.com.
	Repository string `json:"repository"`
	Name       string `json:"name"`
	// Members are the pull request numbers, ascending.
	Members []int `json:"members"`
	// Stack marks a group baum made for a stack of pull requests and keeps
	// in step with it (SyncStacks); a person's rename or member change makes
	// it theirs.
	Stack     bool      `json:"stack"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ReviewedMember is a member as a group review version covered it: the
// document's stack member.
type ReviewedMember struct {
	Number        int    `json:"number"`
	HeadSHA       string `json:"head_sha"`
	ReviewVersion int    `json:"review_version"`
}

// Summary is what the group list shows of a group's versions.
type Summary struct {
	VersionCount int             `json:"version_count"`
	Latest       *store.Metadata `json:"latest"`
	// RiskScore is the latest version's summary.risk_score, or null.
	RiskScore json.RawMessage  `json:"risk_score"`
	Reviewed  []ReviewedMember `json:"reviewed"`
}

// Store keeps group definitions and versions in the application database.
type Store struct{ db *sql.DB }

// Open creates or opens a private store at path.
func Open(path string) (*Store, error) {
	db, err := appdb.Open(path, false)
	if err != nil {
		return nil, err
	}
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS groups(id TEXT PRIMARY KEY, repository TEXT NOT NULL, name TEXT NOT NULL, members TEXT NOT NULL, created INTEGER NOT NULL, updated INTEGER NOT NULL, deleted INTEGER NOT NULL DEFAULT 0, stack INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS versions(group_id TEXT NOT NULL, version INTEGER NOT NULL, metadata BLOB NOT NULL, summary BLOB NOT NULL, document BLOB NOT NULL, PRIMARY KEY(group_id, version));`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// cleanName validates a group name: trimmed, 1 to MaxNameLength characters,
// no control characters.
func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxNameLength {
		return "", invalid("A group name has 1 to %d characters.", MaxNameLength)
	}
	if strings.ContainsFunc(name, unicode.IsControl) {
		return "", invalid("A group name is one line of text.")
	}
	return name, nil
}

// cleanMembers validates members and returns them ascending.
func cleanMembers(members []int) ([]int, error) {
	sorted := slices.Clone(members)
	slices.Sort(sorted)
	if len(slices.Compact(slices.Clone(sorted))) != len(sorted) {
		return nil, invalid("A pull request is in the group once.")
	}
	if len(sorted) < MinMembers || len(sorted) > MaxMembers {
		return nil, invalid("A review group has %d to %d pull requests.", MinMembers, MaxMembers)
	}
	if sorted[0] <= 0 {
		return nil, invalid("Pull request numbers are positive.")
	}
	return sorted, nil
}

// Create stores a new group of members of repository.
func (s *Store) Create(ctx context.Context, repository, name string, members []int) (Group, error) {
	repository = strings.ToLower(strings.TrimSpace(repository))
	if repository == "" {
		return Group{}, invalid("A review group needs a repository.")
	}
	name, err := cleanName(name)
	if err != nil {
		return Group{}, err
	}
	if members, err = cleanMembers(members); err != nil {
		return Group{}, err
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	g := Group{ID: newID(), Repository: repository, Name: name, Members: members, CreatedAt: now, UpdatedAt: now}
	encoded, _ := json.Marshal(members)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO groups(id,repository,name,members,created,updated) VALUES(?,?,?,?,?,?)`, g.ID, g.Repository, g.Name, string(encoded), now.UnixMilli(), now.UnixMilli()); err != nil {
		return Group{}, err
	}
	return g, nil
}

const groupColumns = `id,repository,name,members,stack,created,updated`

func scanGroup(row interface{ Scan(...any) error }) (Group, error) {
	var g Group
	var members string
	var created, updated int64
	if err := row.Scan(&g.ID, &g.Repository, &g.Name, &members, &g.Stack, &created, &updated); err != nil {
		return Group{}, err
	}
	if err := json.Unmarshal([]byte(members), &g.Members); err != nil {
		return Group{}, err
	}
	g.CreatedAt, g.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
	return g, nil
}

// List returns the groups that are not deleted, newest first.
func (s *Store) List(ctx context.Context) ([]Group, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+groupColumns+` FROM groups WHERE deleted=0 ORDER BY created DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Group{}
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Get returns one group, ErrNotFound when it does not exist or was deleted.
func (s *Store) Get(ctx context.Context, id string) (Group, error) {
	g, err := scanGroup(s.db.QueryRowContext(ctx, `SELECT `+groupColumns+` FROM groups WHERE id=? AND deleted=0`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Group{}, ErrNotFound
	}
	return g, err
}

// Update renames the group when name is not nil and replaces its members
// when members is not nil. Either makes a stack group the person's own.
func (s *Store) Update(ctx context.Context, id string, name *string, members []int) (Group, error) {
	// One transaction, so a concurrent SyncStacks cannot be written over.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Group{}, err
	}
	defer func() { _ = tx.Rollback() }()
	g, err := scanGroup(tx.QueryRowContext(ctx, `SELECT `+groupColumns+` FROM groups WHERE id=? AND deleted=0`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Group{}, ErrNotFound
	}
	if err != nil {
		return Group{}, err
	}
	if name != nil {
		if g.Name, err = cleanName(*name); err != nil {
			return Group{}, err
		}
	}
	if members != nil {
		if g.Members, err = cleanMembers(members); err != nil {
			return Group{}, err
		}
	}
	if name == nil && members == nil {
		return g, nil
	}
	g.Stack = false
	g.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
	encoded, _ := json.Marshal(g.Members)
	if _, err := tx.ExecContext(ctx, `UPDATE groups SET name=?, members=?, stack=0, updated=? WHERE id=?`, g.Name, string(encoded), g.UpdatedAt.UnixMilli(), id); err != nil {
		return Group{}, err
	}
	return g, tx.Commit()
}

// Delete removes the group from every list. Its versions stay in the store,
// and the members' own reviews are never touched. A deleted stack group
// keeps its flag, so SyncStacks makes no new group for its pull requests.
func (s *Store) Delete(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE groups SET deleted=? WHERE id=? AND deleted=0`, time.Now().UnixMilli(), id)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return ErrNotFound
	}
	return nil
}

// Latest returns the group's latest version, store.ErrNotFound when it has
// none.
func (s *Store) Latest(ctx context.Context, id string) (store.Version, error) {
	return s.load(ctx, id, 0)
}

// Version returns one version, store.ErrNotFound when it does not exist.
func (s *Store) Version(ctx context.Context, id string, version int) (store.Version, error) {
	if version <= 0 {
		return store.Version{}, errors.New("version must be positive")
	}
	return s.load(ctx, id, version)
}

func (s *Store) load(ctx context.Context, id string, version int) (store.Version, error) {
	query, args := `SELECT metadata,document FROM versions WHERE group_id=?`, []any{id}
	if version > 0 {
		query += ` AND version=?`
		args = append(args, version)
	}
	var metadata, document []byte
	err := s.db.QueryRowContext(ctx, query+` ORDER BY version DESC LIMIT 1`, args...).Scan(&metadata, &document)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Version{}, store.ErrNotFound
	}
	if err != nil {
		return store.Version{}, err
	}
	v := store.Version{Document: document}
	return v, json.Unmarshal(metadata, &v.Metadata)
}

// Versions returns the metadata of every version, newest first.
func (s *Store) Versions(ctx context.Context, id string) ([]store.Metadata, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT metadata FROM versions WHERE group_id=? ORDER BY version DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.Metadata{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var m store.Metadata
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Summary returns what the list shows of the group's versions.
func (s *Store) Summary(ctx context.Context, id string) (Summary, error) {
	out := Summary{Reviewed: []ReviewedMember{}}
	var metadata, summary []byte
	err := s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM versions WHERE group_id=?), metadata, summary FROM versions WHERE group_id=? ORDER BY version DESC LIMIT 1`, id, id).Scan(&out.VersionCount, &metadata, &summary)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Latest = &store.Metadata{}
	if err := json.Unmarshal(metadata, out.Latest); err != nil {
		return out, err
	}
	var stored versionSummary
	if err := json.Unmarshal(summary, &stored); err != nil {
		return out, err
	}
	out.RiskScore = stored.RiskScore
	if stored.Members != nil {
		out.Reviewed = stored.Members
	}
	return out, nil
}

// versionSummary is copied out of a version's document when it is put, so
// the list reads no document.
type versionSummary struct {
	Members   []ReviewedMember `json:"members"`
	RiskScore json.RawMessage  `json:"risk_score,omitempty"`
}

// Put appends v only while previous is still the group's latest version (0
// for none). The document must be the group's: a review of its repository
// with a stack, at version previous+1.
func (s *Store) Put(ctx context.Context, id string, previous int, v store.Version) error {
	g, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	var doc struct {
		Review struct {
			Repository string `json:"repository"`
			Version    int    `json:"version"`
		} `json:"review"`
		Summary struct {
			RiskScore json.RawMessage `json:"risk_score"`
		} `json:"summary"`
		Stack *struct {
			Members []ReviewedMember `json:"members"`
		} `json:"stack"`
	}
	if previous < 0 || v.Metadata.Version != previous+1 || !json.Valid(v.Document) {
		return errors.New("invalid group review version")
	}
	if err := json.Unmarshal(v.Document, &doc); err != nil {
		return err
	}
	if doc.Stack == nil || len(doc.Stack.Members) == 0 || doc.Review.Version != v.Metadata.Version || !strings.EqualFold(doc.Review.Repository, documentRepository(g.Repository)) {
		return errors.New("document identity does not match the group")
	}
	metadata, err := json.Marshal(v.Metadata)
	if err != nil {
		return err
	}
	summary, err := json.Marshal(versionSummary{Members: doc.Stack.Members, RiskScore: doc.Summary.RiskScore})
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO versions(group_id,version,metadata,summary,document) SELECT ?,?,?,?,? WHERE COALESCE((SELECT MAX(version) FROM versions WHERE group_id=?),0)=?`, id, v.Metadata.Version, metadata, summary, []byte(v.Document), id, previous)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return ErrConflict
	}
	return nil
}

// documentRepository is how a document names the repository: owner/name,
// without the host the registry's identity carries off github.com.
func documentRepository(identity string) string {
	if parts := strings.Split(identity, "/"); len(parts) == 3 {
		return parts[1] + "/" + parts[2]
	}
	return identity
}

// Publisher is the group as generate.Publish's version store: it ignores the
// pull request key and records no run leases (the job queue shows a running
// group job).
type Publisher struct {
	Store *Store
	Group string
}

// Latest implements generate.Versions.
func (p Publisher) Latest(ctx context.Context, _ store.Key) (store.Version, error) {
	return p.Store.Latest(ctx, p.Group)
}

// BeginRun implements generate.Versions.
func (Publisher) BeginRun(context.Context, store.Key, time.Time) (int64, error) { return 0, nil }

// HeartbeatRun implements generate.Versions.
func (Publisher) HeartbeatRun(context.Context, int64, time.Time) error { return nil }

// FinishRun implements generate.Versions.
func (Publisher) FinishRun(context.Context, int64, string) error { return nil }

// Put implements generate.Versions.
func (p Publisher) Put(ctx context.Context, _ store.Key, previous, next store.Version) error {
	return p.Store.Put(ctx, p.Group, previous.Metadata.Version, next)
}

// Outdated reports whether a group review that covered reviewed no longer
// matches the group: its members changed, or a member's current head (from
// discovery; an unknown head is not counted) differs from the head reviewed.
// A group without a review is not outdated.
func Outdated(members []int, reviewed []ReviewedMember, heads map[int]string) bool {
	if len(reviewed) == 0 {
		return false
	}
	covered := make([]int, 0, len(reviewed))
	for _, member := range reviewed {
		covered = append(covered, member.Number)
		if head := heads[member.Number]; head != "" && !strings.EqualFold(head, member.HeadSHA) {
			return true
		}
	}
	slices.Sort(covered)
	current := slices.Clone(members)
	slices.Sort(current)
	return !slices.Equal(covered, current)
}
