package decisionlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/baum-ai/baum/features"
	"github.com/baum-ai/baum/internal/github"
)

// featureSchema is the feature log's part of the store (README.md, "Feature
// log").
const featureSchema = `
CREATE TABLE IF NOT EXISTS pull_request(
  repository TEXT NOT NULL, number INTEGER NOT NULL,
  title TEXT NOT NULL, body TEXT NOT NULL DEFAULT '', state TEXT NOT NULL, draft INTEGER NOT NULL DEFAULT 0,
  author TEXT NOT NULL DEFAULT '', bot INTEGER NOT NULL DEFAULT 0,
  head_ref TEXT NOT NULL DEFAULT '', head_sha TEXT NOT NULL, base_ref TEXT NOT NULL DEFAULT '', base_sha TEXT NOT NULL,
  cross_repository INTEGER NOT NULL DEFAULT 0, default_branch TEXT NOT NULL DEFAULT '',
  merge_commit_sha TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, merged_at INTEGER, closed_at INTEGER,
  paths_head_sha TEXT NOT NULL DEFAULT '', paths_json TEXT,
  paths_failures INTEGER NOT NULL DEFAULT 0, paths_retry_at INTEGER,
  PRIMARY KEY(repository, number));
CREATE TABLE IF NOT EXISTS pull_commit(
  repository TEXT NOT NULL, number INTEGER NOT NULL, sha TEXT NOT NULL, committed_at INTEGER NOT NULL,
  PRIMARY KEY(repository, number, sha));
CREATE INDEX IF NOT EXISTS pull_commit_sha ON pull_commit(repository, sha);
CREATE INDEX IF NOT EXISTS pull_commit_committed ON pull_commit(repository, committed_at);
CREATE TABLE IF NOT EXISTS session_commit(
  agent TEXT NOT NULL, session_id TEXT NOT NULL, repository TEXT NOT NULL, sha TEXT NOT NULL,
  method TEXT NOT NULL, confidence REAL NOT NULL,
  PRIMARY KEY(repository, sha, agent, session_id));
CREATE INDEX IF NOT EXISTS session_commit_session ON session_commit(agent, session_id);
CREATE TABLE IF NOT EXISTS pull_sync(
  repository TEXT PRIMARY KEY, etag TEXT NOT NULL DEFAULT '', cursor_updated_at INTEGER,
  activated_at INTEGER NOT NULL, relinked_at INTEGER);
CREATE TABLE IF NOT EXISTS feature_version(
  id INTEGER PRIMARY KEY AUTOINCREMENT, repository TEXT NOT NULL, state TEXT NOT NULL,
  created_at INTEGER NOT NULL, switched_at INTEGER, stopped TEXT NOT NULL DEFAULT '',
  spent_usd REAL NOT NULL DEFAULT 0, total INTEGER NOT NULL DEFAULT 0, placed INTEGER NOT NULL DEFAULT 0);
CREATE UNIQUE INDEX IF NOT EXISTS feature_version_current ON feature_version(repository) WHERE state='current';
CREATE UNIQUE INDEX IF NOT EXISTS feature_version_building ON feature_version(repository) WHERE state='building';
CREATE TABLE IF NOT EXISTS feature(
  id TEXT PRIMARY KEY, version_id INTEGER NOT NULL, repository TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '', description TEXT NOT NULL DEFAULT '', digest TEXT NOT NULL DEFAULT '',
  key_decisions_json TEXT NOT NULL DEFAULT '[]',
  membership_version INTEGER NOT NULL DEFAULT 0, summarized_version INTEGER NOT NULL DEFAULT 0,
  dirty_since INTEGER, changed_at INTEGER NOT NULL, summary_input_hash TEXT NOT NULL DEFAULT '',
  merged_into TEXT, split_from TEXT, split_checked_version INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS feature_version_repository ON feature(version_id, repository);
CREATE TABLE IF NOT EXISTS feature_pull(
  version_id INTEGER NOT NULL, repository TEXT NOT NULL, number INTEGER NOT NULL, feature_id TEXT NOT NULL,
  assigned_by TEXT NOT NULL, assigned_at INTEGER NOT NULL, assigned_head_sha TEXT NOT NULL DEFAULT '',
  assigned_paths_json TEXT NOT NULL DEFAULT '[]', assigned_merged INTEGER NOT NULL DEFAULT 0, checked_head_sha TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(version_id, repository, number));
CREATE INDEX IF NOT EXISTS feature_pull_feature ON feature_pull(feature_id);
CREATE TABLE IF NOT EXISTS feature_session(
  version_id INTEGER NOT NULL, repository TEXT NOT NULL, agent TEXT NOT NULL, session_id TEXT NOT NULL,
  feature_id TEXT NOT NULL, assigned_at INTEGER NOT NULL,
  PRIMARY KEY(version_id, repository, agent, session_id));
CREATE INDEX IF NOT EXISTS feature_session_feature ON feature_session(feature_id);
CREATE TABLE IF NOT EXISTS feature_settle(
  version_id INTEGER NOT NULL, repository TEXT NOT NULL, kind TEXT NOT NULL, item TEXT NOT NULL,
  trigger_key TEXT NOT NULL DEFAULT '', outcome TEXT NOT NULL DEFAULT '', failures INTEGER NOT NULL DEFAULT 0, next_at INTEGER,
  PRIMARY KEY(version_id, repository, kind, item));
CREATE TABLE IF NOT EXISTS pr_summary(
  repository TEXT NOT NULL, number INTEGER NOT NULL, content_hash TEXT NOT NULL, summary TEXT NOT NULL, created_at INTEGER NOT NULL,
  PRIMARY KEY(repository, number));
CREATE TABLE IF NOT EXISTS feature_consolidation(
  repository TEXT PRIMARY KEY, last_pass_at INTEGER, fingerprint TEXT NOT NULL DEFAULT '');`

// Feature version states.
const (
	VersionBuilding = "building"
	VersionCurrent  = "current"
	VersionRetired  = "retired"
)

// migrate brings a store an earlier release made up to the schema, in one
// transaction: the ledger's stage, item and rejections columns, and the pull
// requests' fork and default branch columns, added when missing. Pull
// requests listed before the latter are listed again: the sync state's ETag
// and cursor are reset. The columns are read without a lock; only when one is
// missing is the write lock taken and they are read again, so a store two
// processes open at once is migrated by one of them.
func migrate(ctx context.Context, db *sql.DB) error {
	// A migrated store is read without the write lock.
	if missing, err := missingColumns(ctx, db); err != nil || len(missing) == 0 {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), `ROLLBACK`)
		}
	}()
	// Read again under the lock: another process may have migrated it.
	missing, err := missingColumns(ctx, conn)
	if err != nil {
		return err
	}
	relist := false
	for _, column := range missing {
		if _, err := conn.ExecContext(ctx, column.ddl); err != nil {
			return err
		}
		relist = relist || column.relist
	}
	if relist {
		if _, err := conn.ExecContext(ctx, `UPDATE pull_sync SET etag='', cursor_updated_at=NULL`); err != nil {
			return err
		}
	}
	_, err = conn.ExecContext(ctx, `COMMIT`)
	committed = err == nil
	return err
}

// migration is a column migrate adds, and whether adding it lists the pull
// requests again.
type migration struct {
	table, name, ddl string
	relist           bool
}

var migrations = []migration{
	{"capture_attempt", "stage", `ALTER TABLE capture_attempt ADD COLUMN stage TEXT NOT NULL DEFAULT 'capture'`, false},
	{"capture_attempt", "item", `ALTER TABLE capture_attempt ADD COLUMN item TEXT`, false},
	{"capture_attempt", "rejections", `ALTER TABLE capture_attempt ADD COLUMN rejections INTEGER NOT NULL DEFAULT 0`, false},
	{"pull_request", "cross_repository", `ALTER TABLE pull_request ADD COLUMN cross_repository INTEGER NOT NULL DEFAULT 0`, true},
	{"pull_request", "default_branch", `ALTER TABLE pull_request ADD COLUMN default_branch TEXT NOT NULL DEFAULT ''`, true},
}

// missingColumns are the migrations whose column the store lacks.
func missingColumns(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) ([]migration, error) {
	have := map[string]bool{}
	for _, table := range []string{"capture_attempt", "pull_request"} {
		names, err := collect(scanString)(q.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table))
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			have[table+"."+name] = true
		}
	}
	var missing []migration
	for _, m := range migrations {
		if !have[m.table+"."+m.name] {
			missing = append(missing, m)
		}
	}
	return missing, nil
}

// querier is a *sql.DB or *sql.Tx.
type querier interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// liveVersion is the one place that decides which feature version of a
// repository the feature log reads and writes: its current one. With create
// a repository without one gets it; otherwise it is 0.
func liveVersion(ctx context.Context, q querier, repository string, create bool, now time.Time) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, `SELECT id FROM feature_version WHERE repository=? AND state=?`, repository, VersionCurrent).Scan(&id)
	if !errors.Is(err, sql.ErrNoRows) || !create {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return id, err
	}
	err = q.QueryRowContext(ctx, `INSERT INTO feature_version(repository, state, created_at, switched_at) VALUES (?,?,?,?) RETURNING id`,
		repository, VersionCurrent, now.UnixMilli(), now.UnixMilli()).Scan(&id)
	return id, err
}

// LiveVersion is the repository's feature version in use, created when it
// has none.
func (s *Store) LiveVersion(ctx context.Context, repository string) (int64, error) {
	var id int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = liveVersion(ctx, tx, repository, true, s.now())
		return err
	})
	return id, err
}

// PullSync is a repository's pull request sync state.
type PullSync struct {
	ETag string
	// Cursor is the latest updated_at listed, zero before the first sync.
	Cursor time.Time
	// ActivatedAt is when the feature log first ran for the repository;
	// earlier work is not assigned.
	ActivatedAt time.Time
	// RelinkedAt is the last full re-link, zero for none.
	RelinkedAt time.Time
}

// Activate reads the repository's sync state, activating the repository the
// first time.
func (s *Store) Activate(ctx context.Context, repository string) (PullSync, error) {
	var st PullSync
	var cursor, relinked sql.NullInt64
	var activated int64
	read := func() error {
		return s.db.QueryRowContext(ctx, `SELECT etag, cursor_updated_at, activated_at, relinked_at FROM pull_sync WHERE repository=?`, repository).
			Scan(&st.ETag, &cursor, &activated, &relinked)
	}
	err := read()
	if errors.Is(err, sql.ErrNoRows) {
		if _, err = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO pull_sync(repository, activated_at) VALUES (?,?)`, repository, s.now().UnixMilli()); err == nil {
			err = read()
		}
	}
	if err != nil {
		return st, err
	}
	st.ActivatedAt = time.UnixMilli(activated)
	if cursor.Valid {
		st.Cursor = time.UnixMilli(cursor.Int64)
	}
	if relinked.Valid {
		st.RelinkedAt = time.UnixMilli(relinked.Int64)
	}
	return st, nil
}

// SavePulls upserts the listed pull requests, marking the feature of one
// whose title, description or state changed, and, for a non-zero cursor,
// moves the sync state to etag and the cursor; the cursor never goes back. A
// new head or base starts the count of failed reads again, and a new base
// has the pull request's commits read again.
func (s *Store) SavePulls(ctx context.Context, repository string, pulls []github.ListedPull, etag string, cursor time.Time) error {
	now := s.now()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		version, err := liveVersion(ctx, tx, repository, false, now)
		if err != nil {
			return err
		}
		for _, p := range pulls {
			// A feature's text reads its pull requests' title, description
			// and state.
			var changed string
			err := tx.QueryRowContext(ctx, `SELECT fp.feature_id FROM pull_request p
JOIN feature_pull fp ON fp.version_id=?1 AND fp.repository=p.repository AND fp.number=p.number
WHERE p.repository=?2 AND p.number=?3 AND (p.title != ?4 OR p.body != ?5 OR p.state != ?6 OR (p.merged_at IS NOT NULL) != ?7)`,
				version, repository, p.Number, p.Title, p.Body, p.State, p.MergedAt != nil).Scan(&changed)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err := markFeatures(ctx, tx, []string{changed}, now); err != nil {
				return err
			}
			var merged, closed any
			if p.MergedAt != nil {
				merged = p.MergedAt.UnixMilli()
			}
			if p.ClosedAt != nil {
				closed = p.ClosedAt.UnixMilli()
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO pull_request(repository, number, title, body, state, draft, author, bot,
head_ref, head_sha, base_ref, base_sha, merge_commit_sha, created_at, updated_at, merged_at, closed_at, cross_repository, default_branch)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(repository, number) DO UPDATE SET
cross_repository=excluded.cross_repository, default_branch=excluded.default_branch,
paths_head_sha=CASE WHEN base_sha=excluded.base_sha OR head_sha!=excluded.head_sha THEN paths_head_sha ELSE '' END,
paths_failures=CASE WHEN head_sha=excluded.head_sha AND base_sha=excluded.base_sha THEN paths_failures ELSE 0 END,
paths_retry_at=CASE WHEN head_sha=excluded.head_sha AND base_sha=excluded.base_sha THEN paths_retry_at END, title=excluded.title, body=excluded.body,
state=excluded.state, draft=excluded.draft, author=excluded.author, bot=excluded.bot, head_ref=excluded.head_ref, head_sha=excluded.head_sha,
base_ref=excluded.base_ref, base_sha=excluded.base_sha, merge_commit_sha=excluded.merge_commit_sha, created_at=excluded.created_at,
updated_at=excluded.updated_at, merged_at=excluded.merged_at, closed_at=excluded.closed_at`,
				repository, p.Number, p.Title, p.Body, p.State, p.Draft, p.Author, p.Bot, p.HeadRef, p.HeadSHA, p.BaseRef, p.BaseSHA,
				p.MergeCommitSHA, p.CreatedAt.UnixMilli(), p.UpdatedAt.UnixMilli(), merged, closed, p.CrossRepository, p.DefaultBranch); err != nil {
				return err
			}
		}
		if cursor.IsZero() {
			return nil
		}
		_, err = tx.ExecContext(ctx, `UPDATE pull_sync SET etag=?, cursor_updated_at=MAX(COALESCE(cursor_updated_at, 0), ?) WHERE repository=?`,
			etag, cursor.UnixMilli(), repository)
		return err
	})
}

// StoredPull is a pull request whose paths are to be read.
type StoredPull struct {
	Number                    int
	HeadSHA, BaseSHA, BaseRef string
}

// PullsWithoutPaths lists the repository's pull requests that may be placed
// in a feature, open or merged and not opened by a bot, whose paths were not
// read at their head and are due to be, newest first.
func (s *Store) PullsWithoutPaths(ctx context.Context, repository string) ([]StoredPull, error) {
	return s.pulls(ctx, `SELECT number, head_sha, base_sha, base_ref FROM pull_request WHERE repository=? AND paths_head_sha != head_sha
AND bot=0 AND (state='open' OR merged_at IS NOT NULL) AND (paths_retry_at IS NULL OR paths_retry_at <= ?) ORDER BY updated_at DESC`,
		repository, s.now().UnixMilli())
}

// PathsFailed counts a failed read of the pull request's commits at head
// (a new head or base starts the count again, SavePulls). It is tried again after
// FeatureQuiet; after FeaturePathTries failures its paths are unknown at
// that head, and true is returned.
func (s *Store) PathsFailed(ctx context.Context, repository string, number int, head string) (bool, error) {
	unknown := false
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var failures int
		if err := tx.QueryRowContext(ctx, `UPDATE pull_request SET paths_failures=paths_failures+1, paths_retry_at=?
WHERE repository=? AND number=? RETURNING paths_failures`, s.now().Add(FeatureQuiet).UnixMilli(), repository, number).Scan(&failures); err != nil {
			return err
		}
		if unknown = failures >= FeaturePathTries; unknown {
			return s.setPaths(ctx, tx, repository, number, head, nil, nil)
		}
		return nil
	})
	return unknown, err
}

// RetryUnknownPaths has every pull request whose commits failed to be read
// read again now, those with unknown paths FeaturePathTries more times.
func (s *Store) RetryUnknownPaths(ctx context.Context, repository string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE pull_request SET paths_head_sha='', paths_failures=0
WHERE repository=? AND paths_json IS NULL AND paths_head_sha != ''`, repository); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE pull_request SET paths_retry_at=NULL WHERE repository=? AND paths_retry_at IS NOT NULL`, repository)
		return err
	})
}

// PullsWithCommitsSince lists the pull requests holding a recorded commit
// made at or after since, at the head their commits were recorded at, so
// they are linked again while a newer head cannot be read.
// The base is the current one. A new base with the same head has the pull
// request read again (SavePulls); with a new head too, the recorded head is
// kept until the new one is read.
func (s *Store) PullsWithCommitsSince(ctx context.Context, repository string, since time.Time) ([]StoredPull, error) {
	return s.pulls(ctx, `SELECT number, CASE WHEN paths_head_sha != '' THEN paths_head_sha ELSE head_sha END, base_sha, base_ref
FROM pull_request p WHERE repository=?1 AND EXISTS
(SELECT 1 FROM pull_commit c WHERE c.repository=p.repository AND c.number=p.number AND c.committed_at >= ?2) ORDER BY number`,
		repository, since.UnixMilli())
}

func (s *Store) pulls(ctx context.Context, query string, args ...any) ([]StoredPull, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []StoredPull
	for rows.Next() {
		var p StoredPull
		if err := rows.Scan(&p.Number, &p.HeadSHA, &p.BaseSHA, &p.BaseRef); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PullCommit is one commit of a pull request.
type PullCommit struct {
	SHA         string
	CommittedAt time.Time
}

// SetPullPaths records what the pull request touches at head: its paths, nil
// when they cannot be read, and its commits, which replace those it had.
func (s *Store) SetPullPaths(ctx context.Context, repository string, number int, head string, paths []string, commits []PullCommit) error {
	return s.inTx(ctx, func(tx *sql.Tx) error { return s.setPaths(ctx, tx, repository, number, head, paths, commits) })
}

// setPaths records the pull request's paths and commits at head. For a
// feature pull request, a changed commit list or paths mark its feature and
// the features of the sessions linked to its old and new commits; placed at
// this head without paths, it gets them as the ones it was placed with.
func (s *Store) setPaths(ctx context.Context, tx *sql.Tx, repository string, number int, head string, paths []string, commits []PullCommit) error {
	var encoded any
	if paths != nil {
		raw, err := json.Marshal(paths)
		if err != nil {
			return err
		}
		encoded = string(raw)
	}
	var before sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT paths_json FROM pull_request WHERE repository=? AND number=?`, repository, number).Scan(&before); err != nil {
		return err
	}
	shas, err := collect(scanString)(tx.QueryContext(ctx, `SELECT sha FROM pull_commit WHERE repository=? AND number=?`, repository, number))
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE pull_request SET paths_head_sha=?, paths_json=?, paths_failures=0, paths_retry_at=NULL
WHERE repository=? AND number=?`, head, encoded, repository, number); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM pull_commit WHERE repository=? AND number=?`, repository, number); err != nil {
		return err
	}
	text, _ := encoded.(string)
	changed := before.Valid != (paths != nil) || before.String != text || len(shas) != len(commits)
	for _, c := range commits {
		changed = changed || !slices.Contains(shas, c.SHA)
		shas = append(shas, c.SHA)
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO pull_commit(repository, number, sha, committed_at) VALUES (?,?,?,?)`,
			repository, number, c.SHA, c.CommittedAt.UnixMilli()); err != nil {
			return err
		}
	}
	now := s.now()
	version, err := liveVersion(ctx, tx, repository, false, now)
	if err != nil || version == 0 {
		return err
	}
	var feature, placedHead, placedPaths string
	err = tx.QueryRowContext(ctx, `SELECT feature_id, assigned_head_sha, assigned_paths_json FROM feature_pull
WHERE version_id=? AND repository=? AND number=?`, version, repository, number).Scan(&feature, &placedHead, &placedPaths)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var marked []string
	if placedHead == head && placedPaths == "[]" && len(paths) > 0 {
		// Paths recorded late: no call, the placement keeps them.
		if _, err := tx.ExecContext(ctx, `UPDATE feature_pull SET assigned_paths_json=? WHERE version_id=? AND repository=? AND number=?`,
			encoded, version, repository, number); err != nil {
			return err
		}
		marked = append(marked, feature)
	}
	if changed {
		sessions, err := commitSessions(ctx, tx, repository, shas)
		if err != nil {
			return err
		}
		linked, err := sessionFeatures(ctx, tx, version, repository, sessions)
		if err != nil {
			return err
		}
		marked = append(append(marked, feature), linked...)
	}
	return markFeatures(ctx, tx, marked, now)
}

// SessionCommit is one session linked to a commit.
type SessionCommit struct {
	Agent, SessionID, SHA string
	Method                string
	Confidence            float64
}

// ReplaceSessionCommits makes links the sessions linked to the commits shas:
// every earlier link of those commits goes. relinked, when not zero, records
// a full re-link.
// A commit whose sessions changed marks the features holding a pull request
// with it and the features of its old and new sessions.
func (s *Store) ReplaceSessionCommits(ctx context.Context, repository string, shas []string, links []SessionCommit, relinked time.Time) error {
	now := s.now()
	shas = slices.Compact(slices.Sorted(slices.Values(shas)))
	return s.inTx(ctx, func(tx *sql.Tx) error {
		before := map[string][]SessionKey{}
		for chunk := range slices.Chunk(shas, maxParameters) {
			args := []any{repository}
			for _, sha := range chunk {
				args = append(args, sha)
			}
			type link struct {
				sha string
				key SessionKey
			}
			links, err := collect(func(rows *sql.Rows) (link, error) {
				var l link
				return l, rows.Scan(&l.sha, &l.key.Agent, &l.key.ID)
			})(tx.QueryContext(ctx, `SELECT sha, agent, session_id FROM session_commit WHERE repository=?1 AND sha IN (`+
				placeholders(2, len(chunk))+`)`, args...))
			if err != nil {
				return err
			}
			for _, l := range links {
				before[l.sha] = append(before[l.sha], l.key)
			}
		}
		after := map[string][]SessionKey{}
		for _, l := range links {
			if key := (SessionKey{Agent: l.Agent, ID: l.SessionID}); !slices.Contains(after[l.SHA], key) {
				after[l.SHA] = append(after[l.SHA], key)
			}
		}
		var changed []string
		var sessions []SessionKey
		for _, sha := range shas {
			if len(before[sha]) != len(after[sha]) || slices.ContainsFunc(after[sha], func(k SessionKey) bool { return !slices.Contains(before[sha], k) }) {
				changed = append(changed, sha)
				sessions = append(append(sessions, before[sha]...), after[sha]...)
			}
		}
		for _, sha := range shas {
			if _, err := tx.ExecContext(ctx, `DELETE FROM session_commit WHERE repository=? AND sha=?`, repository, sha); err != nil {
				return err
			}
		}
		for _, l := range links {
			if _, err := tx.ExecContext(ctx, `INSERT INTO session_commit(agent, session_id, repository, sha, method, confidence) VALUES (?,?,?,?,?,?)
ON CONFLICT(repository, sha, agent, session_id) DO UPDATE SET method=excluded.method, confidence=excluded.confidence
WHERE excluded.confidence > session_commit.confidence`,
				l.Agent, l.SessionID, repository, l.SHA, l.Method, l.Confidence); err != nil {
				return err
			}
		}
		if err := markLinkChanges(ctx, tx, repository, changed, sessions, now); err != nil {
			return err
		}
		if relinked.IsZero() {
			return nil
		}
		_, err := tx.ExecContext(ctx, `UPDATE pull_sync SET relinked_at=? WHERE repository=?`, relinked.UnixMilli(), repository)
		return err
	})
}

// markLinkChanges marks, for commits whose sessions changed, the features
// holding a pull request with one of them and the features of sessions;
// nothing when no feature holds such a pull request.
func markLinkChanges(ctx context.Context, tx *sql.Tx, repository string, shas []string, sessions []SessionKey, now time.Time) error {
	if len(shas) == 0 {
		return nil
	}
	version, err := liveVersion(ctx, tx, repository, false, now)
	if err != nil || version == 0 {
		return err
	}
	var held []string
	for chunk := range slices.Chunk(shas, maxParameters) {
		args := []any{version, repository}
		for _, sha := range chunk {
			args = append(args, sha)
		}
		features, err := collect(scanString)(tx.QueryContext(ctx, `SELECT DISTINCT fp.feature_id FROM pull_commit pc
JOIN feature_pull fp ON fp.version_id=?1 AND fp.repository=pc.repository AND fp.number=pc.number
WHERE pc.repository=?2 AND pc.sha IN (`+placeholders(3, len(chunk))+`)`, args...))
		if err != nil {
			return err
		}
		held = append(held, features...)
	}
	if len(held) == 0 {
		return nil
	}
	linked, err := sessionFeatures(ctx, tx, version, repository, sessions)
	if err != nil {
		return err
	}
	return markFeatures(ctx, tx, append(held, linked...), now)
}

// SessionLinks reads what attributes a captured session's decisions in the
// repository's live version: the feature pull requests its linked commits
// are in, and the feature it is in on its own. A session without a capture
// row has none.
func (s *Store) SessionLinks(ctx context.Context, repository, agent, sessionID string) (features.SessionLinks, error) {
	var links features.SessionLinks
	version, err := liveVersion(ctx, s.db, repository, false, s.now())
	if err != nil || version == 0 {
		return links, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT fp.feature_id, pr.number, pr.created_at, COALESCE(pr.paths_json, '[]'), fp.assigned_paths_json, sc.sha
FROM session_commit sc
JOIN capture_session cs ON cs.agent=sc.agent AND cs.session_id=sc.session_id
JOIN pull_commit pc ON pc.repository=sc.repository AND pc.sha=sc.sha
JOIN feature_pull fp ON fp.version_id=?1 AND fp.repository=pc.repository AND fp.number=pc.number
JOIN pull_request pr ON pr.repository=pc.repository AND pr.number=pc.number
WHERE sc.repository=?2 AND sc.agent=?3 AND sc.session_id=?4 ORDER BY pr.number, sc.sha`, version, repository, agent, sessionID)
	if err != nil {
		return links, err
	}
	byNumber := map[int32]int{}
	for rows.Next() {
		var pull features.LinkedPullRequest
		var opened int64
		var paths, assigned, sha string
		if err := rows.Scan(&pull.FeatureID, &pull.Number, &opened, &paths, &assigned, &sha); err != nil {
			_ = rows.Close()
			return links, err
		}
		i, seen := byNumber[pull.Number]
		if !seen {
			// The paths it touches now and those it was assigned with.
			var now, then []string
			if err := errors.Join(json.Unmarshal([]byte(paths), &now), json.Unmarshal([]byte(assigned), &then)); err != nil {
				_ = rows.Close()
				return links, err
			}
			pull.Paths = slices.Compact(slices.Sorted(slices.Values(append(now, then...))))
			pull.PullRequestID, pull.OpenedAt = strconv.Itoa(int(pull.Number)), time.UnixMilli(opened)
			i = len(links.PullRequests)
			byNumber[pull.Number] = i
			links.PullRequests = append(links.PullRequests, pull)
		}
		links.PullRequests[i].Commits = append(links.PullRequests[i].Commits, sha)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return links, err
	}
	if err := rows.Close(); err != nil {
		return links, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT fs.feature_id FROM feature_session fs
JOIN capture_session cs ON cs.agent=fs.agent AND cs.session_id=fs.session_id
WHERE fs.version_id=? AND fs.repository=? AND fs.agent=? AND fs.session_id=?`, version, repository, agent, sessionID).Scan(&links.OwnFeature)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return links, err
}
