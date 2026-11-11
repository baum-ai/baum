package repos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/baum-ai/baum/agent/prompt"
)

// ErrInvalidPrompt wraps the reason prompt.Validate (or an unknown id)
// rejected a custom prompt, fit to show the author; a host maps it to 400.
var ErrInvalidPrompt = errors.New("invalid prompt")

// PromptOverride is a custom prompt text stored locally for one scope.
type PromptOverride struct {
	ID   prompt.ID
	Text string
	// BasedOn is the built-in version (Entry.DefaultVersion) current when the
	// text was saved, so a later change of the built-in can be flagged.
	BasedOn   string
	UpdatedAt time.Time
}

// PromptSource says where a prompt's effective text comes from.
type PromptSource string

const (
	PromptSourceDefault    PromptSource = "default"
	PromptSourceGlobal     PromptSource = "global"
	PromptSourceRepository PromptSource = "repository"
)

// GlobalPrompts is the repository argument naming the global scope.
const GlobalPrompts = ""

// promptScope is the stored scope: "" is global, else the lowercase identity.
func promptScope(repository string) string { return strings.ToLower(repository) }

// promptQuerier is what reads rows: the database or one transaction.
type promptQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// PromptOverrides reads the overrides stored for one scope: repository ""
// is the global scope. A database without the table has none.
func (r *Registry) PromptOverrides(ctx context.Context, repository string) (map[prompt.ID]PromptOverride, error) {
	return r.promptRows(ctx, r.db, repository)
}

func (r *Registry) promptRows(ctx context.Context, q promptQuerier, repository string) (map[prompt.ID]PromptOverride, error) {
	out := map[prompt.ID]PromptOverride{}
	if r.noSchema {
		return out, nil
	}
	rows, e := q.QueryContext(ctx, `SELECT prompt_id, text, based_on, updated_at FROM prompt_overrides WHERE repository=?`, promptScope(repository))
	if e != nil {
		// A read-only registry of an older database may lack the table; it
		// is looked for on every read, so a writer creating it is seen.
		if strings.Contains(e.Error(), "no such table: prompt_overrides") {
			return out, nil
		}
		return nil, e
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var o PromptOverride
		var id, at string
		if e = rows.Scan(&id, &o.Text, &o.BasedOn, &at); e != nil {
			return nil, e
		}
		o.ID = prompt.ID(id)
		if o.UpdatedAt, e = time.Parse(time.RFC3339Nano, at); e != nil {
			return nil, e
		}
		if _, known := prompt.Lookup(o.ID); known {
			out[o.ID] = o
		}
	}
	return out, rows.Err()
}

// promptSnapshot reads the global rows, and for a repository its picked
// state and own rows, in one transaction so a concurrent change is not
// mixed in. An unpicked repository has no own rows here: they are kept
// after an unpick and apply again once it is picked.
func (r *Registry) promptSnapshot(ctx context.Context, repository string) (global, own map[prompt.ID]PromptOverride, picked bool, e error) {
	if r.noSchema {
		return map[prompt.ID]PromptOverride{}, map[prompt.ID]PromptOverride{}, false, nil
	}
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, nil, false, e
	}
	defer func() { _ = tx.Rollback() }()
	if global, e = r.promptRows(ctx, tx, GlobalPrompts); e != nil {
		return nil, nil, false, e
	}
	own = map[prompt.ID]PromptOverride{}
	if repository == GlobalPrompts {
		return global, own, true, nil
	}
	var one int
	e = tx.QueryRowContext(ctx, `SELECT 1 FROM repositories r WHERE r.identity=? AND (`+r.pickedSQL()+`)`, strings.ToLower(repository)).Scan(&one)
	if errors.Is(e, sql.ErrNoRows) {
		return global, own, false, nil
	}
	if e != nil {
		return nil, nil, false, e
	}
	if own, e = r.promptRows(ctx, tx, repository); e != nil {
		return nil, nil, false, e
	}
	return global, own, true, nil
}

// resolvedPrompt is what a repository's run uses for one prompt.
type resolvedPrompt struct {
	Source PromptSource
	// Text is the effective text.
	Text string
	// Inherited is the text without the repository's own row: the global
	// override if it validates, else the built-in text.
	Inherited string
	// OwnInvalid is why the repository's own row is skipped, else nil.
	OwnInvalid error
}

// resolvePrompt picks the repository's valid override, else the valid global
// one, else the built-in text. A stored text that no longer passes Validate
// (a rule tightened by a later release) is skipped, not used. It is the one
// place jobs (ResolvePrompts) and the app's entries agree on.
func resolvePrompt(c prompt.Entry, global, own map[prompt.ID]PromptOverride) resolvedPrompt {
	out := resolvedPrompt{Source: PromptSourceDefault, Text: c.Default, Inherited: c.Default}
	if o, ok := global[c.ID]; ok && prompt.Validate(o.ID, o.Text) == nil {
		out.Source, out.Text, out.Inherited = PromptSourceGlobal, o.Text, o.Text
	}
	if o, ok := own[c.ID]; ok {
		if out.OwnInvalid = prompt.Validate(o.ID, o.Text); out.OwnInvalid == nil {
			out.Source, out.Text = PromptSourceRepository, o.Text
		}
	}
	return out
}

// requirePicked fails with ErrNotPicked unless a non-empty repository is
// picked, read in tx so an unpick in between is not undone.
func requirePicked(ctx context.Context, tx *sql.Tx, repository string) error {
	if repository == "" {
		return nil
	}
	var one int
	e := tx.QueryRowContext(ctx, `SELECT 1 FROM picked WHERE identity=?`, repository).Scan(&one)
	if errors.Is(e, sql.ErrNoRows) {
		return ErrNotPicked
	}
	return e
}

// SetPromptOverride validates and stores text as the override of id for a
// scope ("" is global), recording the current built-in version as BasedOn. A
// non-empty repository must be picked (ErrNotPicked). A rejected text fails
// with an error wrapping ErrInvalidPrompt.
func (r *Registry) SetPromptOverride(ctx context.Context, repository string, id prompt.ID, text string) (PromptOverride, error) {
	if e := prompt.Validate(id, text); e != nil {
		return PromptOverride{}, fmt.Errorf("%w: %s", ErrInvalidPrompt, e.Error())
	}
	entry, _ := prompt.Lookup(id)
	scope := promptScope(repository)
	o := PromptOverride{ID: id, Text: text, BasedOn: entry.DefaultVersion, UpdatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return PromptOverride{}, e
	}
	defer func() { _ = tx.Rollback() }()
	if e = requirePicked(ctx, tx, scope); e != nil {
		return PromptOverride{}, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO prompt_overrides VALUES (?,?,?,?,?) ON CONFLICT(repository,prompt_id) DO UPDATE SET text=excluded.text, based_on=excluded.based_on, updated_at=excluded.updated_at`,
		scope, string(id), text, o.BasedOn, o.UpdatedAt.Format(time.RFC3339Nano)); e != nil {
		return PromptOverride{}, e
	}
	return o, tx.Commit()
}

// DeletePromptOverride removes the override of id for a scope; a missing one
// is not an error. An unknown id fails with ErrInvalidPrompt and a
// non-empty repository must be picked (ErrNotPicked).
func (r *Registry) DeletePromptOverride(ctx context.Context, repository string, id prompt.ID) error {
	if _, ok := prompt.Lookup(id); !ok {
		return fmt.Errorf("%w: unknown prompt %q", ErrInvalidPrompt, id)
	}
	scope := promptScope(repository)
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback() }()
	if e = requirePicked(ctx, tx, scope); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM prompt_overrides WHERE repository=? AND prompt_id=?`, scope, string(id)); e != nil {
		return e
	}
	return tx.Commit()
}

// ResolvePrompts picks, for every catalogue prompt, the repository's
// override, else the global one, else the built-in text. Overrides holds only
// the overridden prompts; sources has every catalogue id. A repository that
// is not picked (or "") gets only the global overrides: its rows, kept after
// an unpick, apply again once it is picked. Stored texts that fail
// prompt.Validate are skipped. Pass the repository's current lower-case name;
// earlier alias names are not followed.
func (r *Registry) ResolvePrompts(ctx context.Context, repository string) (prompt.Overrides, map[prompt.ID]PromptSource, error) {
	global, own, _, e := r.promptSnapshot(ctx, repository)
	if e != nil {
		return nil, nil, e
	}
	overrides := prompt.Overrides{}
	sources := map[prompt.ID]PromptSource{}
	for _, entry := range prompt.Catalogue() {
		res := resolvePrompt(entry, global, own)
		sources[entry.ID] = res.Source
		if res.Source != PromptSourceDefault {
			overrides[entry.ID] = res.Text
		}
	}
	return overrides, sources, nil
}
