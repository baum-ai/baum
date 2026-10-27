package decisionlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
  merge_commit_sha TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, merged_at INTEGER, closed_at INTEGER,
  paths_head_sha TEXT NOT NULL DEFAULT '', paths_json TEXT,
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

// migrate brings a store an earlier release made up to the schema: the
// ledger's stage and item columns, added when missing.
func migrate(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info('capture_attempt')`)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return err
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, column := range []struct{ name, ddl string }{
		{"stage", `ALTER TABLE capture_attempt ADD COLUMN stage TEXT NOT NULL DEFAULT 'capture'`},
		{"item", `ALTER TABLE capture_attempt ADD COLUMN item TEXT`},
	} {
		if !have[column.name] {
			if _, err := db.ExecContext(ctx, column.ddl); err != nil {
				return err
			}
		}
	}
	return nil
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
	err := s.db.QueryRowContext(ctx, `INSERT INTO pull_sync(repository, activated_at) VALUES (?,?)
ON CONFLICT(repository) DO UPDATE SET repository=excluded.repository RETURNING etag, cursor_updated_at, activated_at, relinked_at`,
		repository, s.now().UnixMilli()).Scan(&st.ETag, &cursor, &activated, &relinked)
	st.ActivatedAt = time.UnixMilli(activated)
	if cursor.Valid {
		st.Cursor = time.UnixMilli(cursor.Int64)
	}
	if relinked.Valid {
		st.RelinkedAt = time.UnixMilli(relinked.Int64)
	}
	return st, err
}

// SavePulls upserts the listed pull requests and advances the sync state to
// etag and the latest updated_at among them.
func (s *Store) SavePulls(ctx context.Context, repository string, pulls []github.ListedPull, etag string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var cursor int64
		for _, p := range pulls {
			var merged, closed any
			if p.MergedAt != nil {
				merged = p.MergedAt.UnixMilli()
			}
			if p.ClosedAt != nil {
				closed = p.ClosedAt.UnixMilli()
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO pull_request(repository, number, title, body, state, draft, author, bot,
head_ref, head_sha, base_ref, base_sha, merge_commit_sha, created_at, updated_at, merged_at, closed_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(repository, number) DO UPDATE SET title=excluded.title, body=excluded.body,
state=excluded.state, draft=excluded.draft, author=excluded.author, bot=excluded.bot, head_ref=excluded.head_ref, head_sha=excluded.head_sha,
base_ref=excluded.base_ref, base_sha=excluded.base_sha, merge_commit_sha=excluded.merge_commit_sha, created_at=excluded.created_at,
updated_at=excluded.updated_at, merged_at=excluded.merged_at, closed_at=excluded.closed_at`,
				repository, p.Number, p.Title, p.Body, p.State, p.Draft, p.Author, p.Bot, p.HeadRef, p.HeadSHA, p.BaseRef, p.BaseSHA,
				p.MergeCommitSHA, p.CreatedAt.UnixMilli(), p.UpdatedAt.UnixMilli(), merged, closed); err != nil {
				return err
			}
			cursor = max(cursor, p.UpdatedAt.UnixMilli())
		}
		_, err := tx.ExecContext(ctx, `UPDATE pull_sync SET etag=?, cursor_updated_at=MAX(COALESCE(cursor_updated_at, 0), ?) WHERE repository=?`,
			etag, cursor, repository)
		return err
	})
}

// StoredPull is a pull request whose paths are to be read.
type StoredPull struct {
	Number                    int
	HeadSHA, BaseSHA, BaseRef string
}

// PullsWithoutPaths lists the repository's pull requests whose paths were
// not read at their head, newest first.
func (s *Store) PullsWithoutPaths(ctx context.Context, repository string) ([]StoredPull, error) {
	return s.pulls(ctx, `SELECT number, head_sha, base_sha, base_ref FROM pull_request WHERE repository=? AND paths_head_sha != head_sha
ORDER BY updated_at DESC`, repository)
}

// PullsWithCommitsSince lists the pull requests holding a commit made at or
// after since.
func (s *Store) PullsWithCommitsSince(ctx context.Context, repository string, since time.Time) ([]StoredPull, error) {
	return s.pulls(ctx, `SELECT number, head_sha, base_sha, base_ref FROM pull_request p WHERE repository=?1 AND EXISTS
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
	var encoded any
	if paths != nil {
		raw, err := json.Marshal(paths)
		if err != nil {
			return err
		}
		encoded = string(raw)
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE pull_request SET paths_head_sha=?, paths_json=? WHERE repository=? AND number=?`,
			head, encoded, repository, number); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM pull_commit WHERE repository=? AND number=?`, repository, number); err != nil {
			return err
		}
		for _, c := range commits {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO pull_commit(repository, number, sha, committed_at) VALUES (?,?,?,?)`,
				repository, number, c.SHA, c.CommittedAt.UnixMilli()); err != nil {
				return err
			}
		}
		return nil
	})
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
func (s *Store) ReplaceSessionCommits(ctx context.Context, repository string, shas []string, links []SessionCommit, relinked time.Time) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, sha := range shas {
			if _, err := tx.ExecContext(ctx, `DELETE FROM session_commit WHERE repository=? AND sha=?`, repository, sha); err != nil {
				return err
			}
		}
		for _, l := range links {
			if _, err := tx.ExecContext(ctx, `INSERT INTO session_commit(agent, session_id, repository, sha, method, confidence) VALUES (?,?,?,?,?,?)
ON CONFLICT(repository, sha, agent, session_id) DO UPDATE SET method=excluded.method, confidence=MAX(confidence, excluded.confidence)`,
				l.Agent, l.SessionID, repository, l.SHA, l.Method, l.Confidence); err != nil {
				return err
			}
		}
		if relinked.IsZero() {
			return nil
		}
		_, err := tx.ExecContext(ctx, `UPDATE pull_sync SET relinked_at=? WHERE repository=?`, relinked.UnixMilli(), repository)
		return err
	})
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
	rows, err := s.db.QueryContext(ctx, `SELECT fp.feature_id, pr.number, pr.created_at, COALESCE(pr.paths_json, '[]'), sc.sha
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
		var paths, sha string
		if err := rows.Scan(&pull.FeatureID, &pull.Number, &opened, &paths, &sha); err != nil {
			_ = rows.Close()
			return links, err
		}
		i, seen := byNumber[pull.Number]
		if !seen {
			if err := json.Unmarshal([]byte(paths), &pull.Paths); err != nil {
				_ = rows.Close()
				return links, err
			}
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
