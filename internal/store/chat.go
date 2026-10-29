package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// A chat is a conversation with the agent that wrote a review version: the
// reviewer asks questions about the pull request, and each turn continues one
// agent session (internal/chat). Conversations and their turns live in the
// same database as the versions they are about, under the same lowercased
// repository key.

// chatSchema creates the chat tables. openRoot runs it on every writable
// open, so a store made before chats existed gains them on its next write.
const chatSchema = `CREATE TABLE IF NOT EXISTS conversations (id TEXT PRIMARY KEY, repository TEXT NOT NULL, pr INTEGER NOT NULL, version INTEGER NOT NULL, backend TEXT NOT NULL, model TEXT NOT NULL, session_id TEXT NOT NULL DEFAULT '', directory TEXT NOT NULL DEFAULT '', seeded INTEGER NOT NULL DEFAULT 0, created INTEGER NOT NULL, updated INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS conversations_by_pr ON conversations(repository,pr,created);
CREATE TABLE IF NOT EXISTS turns (conversation_id TEXT NOT NULL, seq INTEGER NOT NULL, question TEXT NOT NULL, focus BLOB, answer BLOB, job_id TEXT NOT NULL, state TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '', cost_usd REAL, cost_reason TEXT NOT NULL DEFAULT '', created INTEGER NOT NULL, finished INTEGER, note TEXT NOT NULL DEFAULT '', PRIMARY KEY(conversation_id, seq));`

// ErrConversationNotFound is a conversation the store does not hold for the
// pull request.
var ErrConversationNotFound = errors.New("no such conversation for this pull request")

// ErrConversationBusy refuses a question while another of the same
// conversation is queued or running.
var ErrConversationBusy = errors.New("the conversation is still answering a question")

// Turn states. Queued and running are pending; the others are settled.
const (
	TurnQueued      = "queued"
	TurnRunning     = "running"
	TurnCompleted   = "completed"
	TurnFailed      = "failed"
	TurnCancelled   = "cancelled"
	TurnInterrupted = "interrupted"
)

// Pending reports whether a turn in state is still waiting for its answer.
func Pending(state string) bool { return state == TurnQueued || state == TurnRunning }

// Conversation is one chat about a review version. SessionID and Directory
// are the agent session the next turn resumes and the working directory it
// was recorded under; they never leave the daemon. Seeded says the session
// started from the saved review and earlier turns instead of the review
// agent's own session.
type Conversation struct {
	ID        string    `json:"id"`
	Version   int       `json:"version"`
	Backend   string    `json:"backend"`
	Model     string    `json:"model"`
	SessionID string    `json:"-"`
	Directory string    `json:"-"`
	Seeded    bool      `json:"seeded"`
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
	Turns     []Turn    `json:"turns"`
}

// Turn is one question and its answer. Focus is the part of the review the
// question is about, as the app sent it; Answer is the redacted answer
// document, absent until the turn completes.
type Turn struct {
	Seq        int             `json:"seq"`
	Question   string          `json:"question"`
	Focus      json.RawMessage `json:"focus"`
	Answer     json.RawMessage `json:"answer,omitempty"`
	JobID      string          `json:"job_id"`
	State      string          `json:"state"`
	Reason     string          `json:"reason,omitempty"`
	Note       string          `json:"note,omitempty"`
	CostUSD    *float64        `json:"cost_usd,omitempty"`
	CostReason string          `json:"cost_reason,omitempty"`
	Created    time.Time       `json:"created"`
	Finished   *time.Time      `json:"finished,omitempty"`
}

// Settlement is how a turn ends.
type Settlement struct {
	State, Reason string
	// Answer is the redacted answer document of a completed turn.
	Answer     json.RawMessage
	CostUSD    *float64
	CostReason string
	Finished   time.Time
}

func millis(t time.Time) int64 { return t.UTC().UnixMilli() }
func fromMillis(n int64) time.Time {
	return time.UnixMilli(n).UTC()
}

// nullable stores an empty JSON value as NULL.
func nullable(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}

// CreateConversation stores a new conversation of the pull request with its
// first turn.
func (s *Store) CreateConversation(ctx context.Context, k Key, c Conversation, first Turn) error {
	if !k.valid() || c.ID == "" || c.Version <= 0 || first.Seq != 1 {
		return errors.New("invalid conversation")
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO conversations(id,repository,pr,version,backend,model,session_id,directory,seeded,created,updated) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, k.repository(), k.Number, c.Version, c.Backend, c.Model, c.SessionID, c.Directory, c.Seeded, millis(c.Created), millis(c.Updated)); err != nil {
		return err
	}
	if err := insertTurn(ctx, tx, c.ID, first); err != nil {
		return err
	}
	return tx.Commit()
}

func insertTurn(ctx context.Context, tx *sql.Tx, conversation string, t Turn) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO turns(conversation_id,seq,question,focus,job_id,state,created) VALUES(?,?,?,?,?,?,?)`,
		conversation, t.Seq, t.Question, nullable(t.Focus), t.JobID, t.State, millis(t.Created))
	return err
}

// Ended reports how the job of a pending turn ended: the state and reason to
// settle the turn with, and false while the job is still queued or running.
type Ended func(jobID string) (state, reason string, ended bool)

// AppendTurn adds t to the conversation as its next turn and returns the
// conversation as it was before, with its turns, and the new turn's number.
// A pending turn whose job ended reports ended (a job that just failed, or a
// daemon that stopped while it ran) is settled first; any turn still pending
// after that refuses the new one with ErrConversationBusy. The check and the
// insert are one transaction.
func (s *Store) AppendTurn(ctx context.Context, k Key, id string, t Turn, ended Ended) (Conversation, int, error) {
	if !k.valid() || id == "" {
		return Conversation{}, 0, errors.New("invalid conversation")
	}
	db, err := s.database(true)
	if err != nil {
		return Conversation{}, 0, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Conversation{}, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	// The first statement writes, so the transaction holds SQLite's writer
	// lock from here on: two appends cannot both pass the busy check.
	result, err := tx.ExecContext(ctx, `UPDATE conversations SET updated=? WHERE id=? AND repository=? AND pr=?`, millis(t.Created), id, k.repository(), k.Number)
	if err != nil {
		return Conversation{}, 0, err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return Conversation{}, 0, errors.Join(err, ErrConversationNotFound)
	}
	c, err := conversation(ctx, tx, k, id)
	if err != nil {
		return Conversation{}, 0, err
	}
	for _, turn := range c.Turns {
		if !Pending(turn.State) {
			continue
		}
		if ended == nil {
			return Conversation{}, 0, ErrConversationBusy
		}
		state, reason, done := ended(turn.JobID)
		if !done || Pending(state) || state == "" {
			return Conversation{}, 0, ErrConversationBusy
		}
		if _, err := tx.ExecContext(ctx, `UPDATE turns SET state=?, reason=?, finished=? WHERE conversation_id=? AND seq=?`, state, reason, millis(t.Created), id, turn.Seq); err != nil {
			return Conversation{}, 0, err
		}
	}
	t.Seq = len(c.Turns) + 1
	if err := insertTurn(ctx, tx, id, t); err != nil {
		return Conversation{}, 0, err
	}
	return c, t.Seq, tx.Commit()
}

type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// Conversation returns one conversation of the pull request with its turns.
func (s *Store) Conversation(ctx context.Context, k Key, id string) (Conversation, error) {
	if !k.valid() {
		return Conversation{}, errors.New("invalid repository or pull request")
	}
	db, err := s.database(false)
	if errors.Is(err, ErrNotFound) {
		return Conversation{}, ErrConversationNotFound
	}
	if err != nil {
		return Conversation{}, err
	}
	defer db.Close()
	c, err := conversation(ctx, db, k, id)
	if missingTable(err) {
		return Conversation{}, ErrConversationNotFound
	}
	return c, err
}

func conversation(ctx context.Context, db querier, k Key, id string) (Conversation, error) {
	list, err := conversations(ctx, db, k, id)
	if err != nil {
		return Conversation{}, err
	}
	if len(list) == 0 {
		return Conversation{}, ErrConversationNotFound
	}
	return list[0], nil
}

// Conversations returns the pull request's conversations, newest first, each
// with its turns in order. A store without any, or made before chats existed,
// has none.
func (s *Store) Conversations(ctx context.Context, k Key) ([]Conversation, error) {
	if !k.valid() {
		return nil, errors.New("invalid repository or pull request")
	}
	db, err := s.database(false)
	if errors.Is(err, ErrNotFound) {
		return []Conversation{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer db.Close()
	list, err := conversations(ctx, db, k, "")
	if missingTable(err) {
		return []Conversation{}, nil
	}
	return list, err
}

// missingTable reports a read of a store whose chat tables were never
// created: a read-only open does not create them.
func missingTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}

func conversations(ctx context.Context, db querier, k Key, id string) ([]Conversation, error) {
	query := `SELECT id,version,backend,model,session_id,directory,seeded,created,updated FROM conversations WHERE repository=? AND pr=?`
	args := []any{k.repository(), k.Number}
	if id != "" {
		query += ` AND id=?`
		args = append(args, id)
	}
	rows, err := db.QueryContext(ctx, query+` ORDER BY created DESC, id DESC`, args...)
	if err != nil {
		return nil, err
	}
	list := []Conversation{}
	index := map[string]int{}
	for rows.Next() {
		var c Conversation
		var created, updated int64
		if err := rows.Scan(&c.ID, &c.Version, &c.Backend, &c.Model, &c.SessionID, &c.Directory, &c.Seeded, &created, &updated); err != nil {
			_ = rows.Close()
			return nil, err
		}
		c.Created, c.Updated, c.Turns = fromMillis(created), fromMillis(updated), []Turn{}
		index[c.ID] = len(list)
		list = append(list, c)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return list, nil
	}
	query = `SELECT t.conversation_id,t.seq,t.question,t.focus,t.answer,t.job_id,t.state,t.reason,t.cost_usd,t.cost_reason,t.created,t.finished,t.note FROM turns t JOIN conversations c ON c.id=t.conversation_id WHERE c.repository=? AND c.pr=?`
	if id != "" {
		query += ` AND c.id=?`
	}
	rows, err = db.QueryContext(ctx, query+` ORDER BY t.conversation_id, t.seq`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t Turn
		var conversation string
		var focus, answer []byte
		var cost sql.NullFloat64
		var created int64
		var finished sql.NullInt64
		if err := rows.Scan(&conversation, &t.Seq, &t.Question, &focus, &answer, &t.JobID, &t.State, &t.Reason, &cost, &t.CostReason, &created, &finished, &t.Note); err != nil {
			return nil, err
		}
		if len(focus) > 0 {
			t.Focus = focus
		}
		if len(answer) > 0 {
			t.Answer = answer
		}
		if cost.Valid {
			t.CostUSD = &cost.Float64
		}
		t.Created = fromMillis(created)
		if finished.Valid {
			at := fromMillis(finished.Int64)
			t.Finished = &at
		}
		if i, ok := index[conversation]; ok {
			list[i].Turns = append(list[i].Turns, t)
		}
	}
	return list, rows.Err()
}

// StartTurn marks a queued turn running. It reports false when the turn is
// not queued (cancelled before it started, or already settled).
func (s *Store) StartTurn(ctx context.Context, k Key, id string, seq int) (bool, error) {
	return s.updateTurn(ctx, k, id, seq, `UPDATE turns SET state='running' WHERE conversation_id=? AND seq=? AND state='queued'`)
}

// NoteTurn records a note on a turn, such as the conversation falling back
// to a session started without the review's own context.
func (s *Store) NoteTurn(ctx context.Context, k Key, id string, seq int, note string) error {
	_, err := s.updateTurn(ctx, k, id, seq, `UPDATE turns SET note=? WHERE conversation_id=? AND seq=?`, note)
	return err
}

// SettleTurn ends a pending turn and reports whether it did: a turn already
// settled keeps how it ended.
func (s *Store) SettleTurn(ctx context.Context, k Key, id string, seq int, settled Settlement) (bool, error) {
	if Pending(settled.State) || settled.State == "" {
		return false, errors.New("a turn settles as completed, failed, cancelled or interrupted")
	}
	var cost any
	if settled.CostUSD != nil {
		cost = *settled.CostUSD
	}
	return s.updateTurn(ctx, k, id, seq, `UPDATE turns SET state=?, reason=?, answer=?, cost_usd=?, cost_reason=?, finished=? WHERE conversation_id=? AND seq=? AND state IN ('queued','running')`,
		settled.State, settled.Reason, nullable(settled.Answer), cost, settled.CostReason, millis(settled.Finished))
}

// updateTurn runs statement, whose last two arguments are the conversation
// and turn, on a turn of a conversation of the pull request.
func (s *Store) updateTurn(ctx context.Context, k Key, id string, seq int, statement string, args ...any) (bool, error) {
	if !k.valid() || id == "" || seq < 1 {
		return false, errors.New("invalid turn")
	}
	db, err := s.database(true)
	if err != nil {
		return false, err
	}
	defer db.Close()
	if err := owned(ctx, db, k, id); err != nil {
		return false, err
	}
	result, err := db.ExecContext(ctx, statement, append(args, id, seq)...)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// owned checks the conversation belongs to the pull request.
func owned(ctx context.Context, db *sql.DB, k Key, id string) error {
	var found int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM conversations WHERE id=? AND repository=? AND pr=?`, id, k.repository(), k.Number).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrConversationNotFound
	}
	return err
}

// SetConversationSession records the agent session the conversation's next
// turn resumes, the directory it runs in and whether it was seeded.
func (s *Store) SetConversationSession(ctx context.Context, k Key, id, session, directory string, seeded bool, at time.Time) error {
	if !k.valid() || id == "" {
		return errors.New("invalid conversation")
	}
	db, err := s.database(true)
	if err != nil {
		return err
	}
	defer db.Close()
	result, err := db.ExecContext(ctx, `UPDATE conversations SET session_id=?, directory=?, seeded=?, updated=? WHERE id=? AND repository=? AND pr=?`, session, directory, seeded, millis(at), id, k.repository(), k.Number)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return errors.Join(err, ErrConversationNotFound)
	}
	return nil
}
