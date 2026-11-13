package console

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/appdb"
)

// schema is the conversations table in the application database. New columns
// are added by a check of pragma_table_info and an ALTER TABLE, as the other
// relational stores there add theirs, so a database written by an older baum
// opens with its rows intact.
const schema = `CREATE TABLE IF NOT EXISTS console_conversations(id TEXT PRIMARY KEY, agent TEXT NOT NULL, model TEXT NOT NULL, mode TEXT NOT NULL,
directory TEXT NOT NULL, repository TEXT NOT NULL DEFAULT '', pull_request INTEGER NOT NULL DEFAULT 0, branch TEXT NOT NULL DEFAULT '',
created_worktree INTEGER NOT NULL DEFAULT 0, session_id TEXT NOT NULL DEFAULT '', pages TEXT NOT NULL DEFAULT '[]', title TEXT NOT NULL DEFAULT '',
created INTEGER NOT NULL, updated INTEGER NOT NULL, state TEXT NOT NULL, error TEXT NOT NULL DEFAULT '', cost_usd REAL, cost_partial INTEGER NOT NULL DEFAULT 0)`

// store keeps conversations in the application database.
type store struct{ db *sql.DB }

// openStore opens the conversations table and settles the conversations a
// stopped daemon left running: their turn is over, whatever its agent did,
// so they come back failed with reason.
func openStore(path, reason string, now time.Time) (*store, error) {
	db, err := appdb.Open(path, false)
	if err != nil {
		return nil, err
	}
	if _, err = db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err = addColumns(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err = db.Exec(`UPDATE console_conversations SET state=?, error=?, updated=? WHERE state=?`, StateFailed, reason, now.UnixMilli(), StateRunning); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &store{db: db}, nil
}

// addedColumns are the columns added after the table was first created,
// with their definitions.
var addedColumns = [][2]string{
	{"created_branch", "INTEGER NOT NULL DEFAULT 0"},
	{"directory_note", "TEXT NOT NULL DEFAULT ''"},
}

// addColumns adds the columns a table created by an earlier baum lacks.
func addColumns(db *sql.DB) error {
	for _, column := range addedColumns {
		var found int
		if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('console_conversations') WHERE name=?`, column[0]).Scan(&found); err != nil {
			return err
		}
		if found > 0 {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE console_conversations ADD COLUMN ` + column[0] + ` ` + column[1]); err != nil {
			return err
		}
	}
	return nil
}

func (s *store) close() error { return s.db.Close() }

const columns = `id, agent, model, mode, directory, repository, pull_request, branch, created_worktree, session_id, pages, title, created, updated, state, error, cost_usd, cost_partial, created_branch, directory_note`

// save writes the whole conversation, inserting it or replacing its row.
func (s *store) save(c Conversation) error {
	pages, err := json.Marshal(c.Pages)
	if err != nil {
		return err
	}
	var cost sql.NullFloat64
	if c.CostUSD != nil {
		cost = sql.NullFloat64{Float64: *c.CostUSD, Valid: true}
	}
	_, err = s.db.Exec(`INSERT INTO console_conversations(`+columns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET agent=excluded.agent, model=excluded.model, mode=excluded.mode, directory=excluded.directory, repository=excluded.repository,
pull_request=excluded.pull_request, branch=excluded.branch, created_worktree=excluded.created_worktree, session_id=excluded.session_id, pages=excluded.pages,
title=excluded.title, created=excluded.created, updated=excluded.updated, state=excluded.state, error=excluded.error, cost_usd=excluded.cost_usd, cost_partial=excluded.cost_partial,
created_branch=excluded.created_branch, directory_note=excluded.directory_note`,
		c.ID, c.Agent, c.Model, c.Mode, c.Directory, c.Repository, c.PullRequest, c.Branch, c.CreatedWorktree, c.Session, string(pages), c.Title,
		c.CreatedAt.UnixMilli(), c.UpdatedAt.UnixMilli(), c.State, c.Error, cost, c.CostPartial, c.CreatedBranch, c.DirectoryNote)
	return err
}

type scanner interface{ Scan(dest ...any) error }

func scan(row scanner) (Conversation, error) {
	var c Conversation
	var pages string
	var created, updated int64
	var cost sql.NullFloat64
	if err := row.Scan(&c.ID, &c.Agent, &c.Model, &c.Mode, &c.Directory, &c.Repository, &c.PullRequest, &c.Branch, &c.CreatedWorktree, &c.Session,
		&pages, &c.Title, &created, &updated, &c.State, &c.Error, &cost, &c.CostPartial, &c.CreatedBranch, &c.DirectoryNote); err != nil {
		return Conversation{}, err
	}
	// A page list that does not parse is shown as none rather than hiding
	// the conversation.
	if json.Unmarshal([]byte(pages), &c.Pages) != nil || c.Pages == nil {
		c.Pages = []agentcontext.PageContext{}
	}
	c.CreatedAt, c.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
	if cost.Valid {
		c.CostUSD = &cost.Float64
	}
	return c, nil
}

// get returns the conversation with this id, or ErrNotFound.
func (s *store) get(id string) (Conversation, error) {
	c, err := scan(s.db.QueryRow(`SELECT `+columns+` FROM console_conversations WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	return c, err
}

// list returns every conversation, the most recently updated first.
func (s *store) list() ([]Conversation, error) {
	rows, err := s.db.Query(`SELECT ` + columns + ` FROM console_conversations ORDER BY updated DESC, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Conversation{}
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
