package decisionlog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
)

// timelineRevisionTable is the counter itself: per repository, a revision
// that moves whenever a row the Timeline's snapshot reads changes (README.md,
// "Timeline API"). Triggers (installTimelineRevision) bump it, so every
// writer is covered, in this process or another one, and a write to anything
// else in app.sqlite (jobs, UI state, discovery caches), to a column the
// snapshot does not read (a build's progress, a retry time) or to a feature
// version still building leaves it alone. The bump commits with the write
// that caused it.
const timelineRevisionTable = `CREATE TABLE IF NOT EXISTS timeline_revision(repository TEXT PRIMARY KEY, revision INTEGER NOT NULL);`

type revisionTrigger struct {
	table string
	// updates are the columns whose change counts; none for a table never
	// updated in place. An upsert that writes the same values is no change.
	updates []string
	// repositories are the SQL naming the repositories whose snapshots read
	// the row, with %s the row (NEW or OLD); the row's repository when empty.
	repositories []string
	// moves also bumps the old repositories when an update changes them.
	moves bool
	// live counts only rows of the repository's current feature version: a
	// building one is not read until it is switched in, and the switch moves
	// the revision (feature_version's state).
	live bool
}

const currentVersions = `(SELECT id FROM feature_version WHERE state='current')`

// timelineInputs are the tables the snapshot reads, with the columns it
// reads of those updated in place. A table or column the snapshot starts to
// read must be added here; the triggers are installed again when this
// changes (the names carry a hash of their definitions).
var timelineInputs = []revisionTrigger{
	{table: "feature_version", updates: []string{"repository", "state"}},
	{table: "feature", live: true, updates: []string{"version_id", "repository", "title", "description", "digest", "key_decisions_json",
		"merged_into", "membership_version", "summarized_version"}},
	{table: "feature_pull", live: true, updates: []string{"feature_id", "assigned_paths_json"}},
	{table: "feature_session", live: true, updates: []string{"feature_id"}},
	{table: "pull_request", updates: []string{"title", "state", "head_sha", "author", "draft", "merged_at", "bot", "created_at", "updated_at", "paths_json"}},
	{table: "pull_commit"},
	{table: "session_commit"},
	{table: "capture_session", updates: []string{"repository", "branch"}, moves: true},
	// A decision is read through its session's repository, which can differ
	// from the one it was stored under once the session moved.
	{table: "decision", moves: true, repositories: []string{"%s.repository",
		"COALESCE((SELECT repository FROM capture_session WHERE agent=%s.agent AND session_id=%s.session_id), '')"},
		updates: []string{"agent", "session_id", "repository", "ordinal", "title", "summary", "category", "who", "trigger", "rationale",
			"rejected_alternative", "evidence", "superseded", "turns_json", "decided_turn", "decided_at", "branch", "obsolete_at"}},
	{table: "decision_file", repositories: []string{
		"COALESCE((SELECT repository FROM decision WHERE decision_id=%s.decision_id), '')",
		"COALESCE((SELECT cs.repository FROM decision d JOIN capture_session cs ON cs.agent=d.agent AND cs.session_id=d.session_id WHERE d.decision_id=%s.decision_id), '')"}},
}

type triggerDefinition struct{ name, sql string }

// timelineRevisionTriggers are the triggers of timelineInputs, named by
// table and event after a hash of all their definitions.
func timelineRevisionTriggers() []triggerDefinition {
	var defs []triggerDefinition
	for _, in := range timelineInputs {
		repositories := in.repositories
		if len(repositories) == 0 {
			repositories = []string{"%s.repository"}
		}
		bump := func(row string) string {
			var b strings.Builder
			for _, r := range repositories {
				fmt.Fprintf(&b, "INSERT INTO timeline_revision(repository, revision) VALUES (%s, 1) ON CONFLICT(repository) DO UPDATE SET revision=revision+1; ",
					strings.ReplaceAll(r, "%s", row))
			}
			return b.String()
		}
		live := func(row string) string {
			if !in.live {
				return ""
			}
			return fmt.Sprintf("WHEN %s.version_id IN %s ", row, currentVersions)
		}
		define := func(event, condition, body string) {
			defs = append(defs, triggerDefinition{name: in.table + "_" + strings.ToLower(strings.Fields(event)[0]),
				sql: fmt.Sprintf("AFTER %s ON %s %sBEGIN %sEND", event, in.table, condition, body)})
		}
		define("INSERT", live("NEW"), bump("NEW"))
		define("DELETE", live("OLD"), bump("OLD"))
		if len(in.updates) == 0 {
			continue
		}
		changed := make([]string, len(in.updates))
		for i, column := range in.updates {
			changed[i] = "OLD." + column + " IS NOT NEW." + column
		}
		condition := "(" + strings.Join(changed, " OR ") + ")"
		if in.live {
			condition += " AND (NEW.version_id IN " + currentVersions + " OR OLD.version_id IN " + currentVersions + ")"
		}
		body := bump("NEW")
		if in.moves {
			body = bump("OLD") + body
		}
		define("UPDATE OF "+strings.Join(in.updates, ", "), "WHEN "+condition+" ", body)
	}
	sum := sha256.New()
	for _, d := range defs {
		sum.Write([]byte(d.name + d.sql))
	}
	hash := hex.EncodeToString(sum.Sum(nil))[:8]
	for i := range defs {
		defs[i].name = "timeline_revision_" + hash + "_" + defs[i].name
	}
	return defs
}

// installTimelineRevision makes the store's triggers those of this build:
// when the ones stored differ (an older build's, or none), all of them are
// replaced in one transaction, so no write goes unnoticed in between.
func installTimelineRevision(ctx context.Context, db *sql.DB) error {
	defs := timelineRevisionTriggers()
	want := make([]string, len(defs))
	for i, d := range defs {
		want[i] = d.name
	}
	slices.Sort(want)
	stored := func(q interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	}) ([]string, error) {
		return collect(scanString)(q.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='trigger' AND name LIKE 'timeline\_revision\_%' ESCAPE '\' ORDER BY name`))
	}
	if have, err := stored(db); err != nil || slices.Equal(have, want) {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Another process may have installed them while this one waited.
	have, err := stored(tx)
	if err == nil && !slices.Equal(have, want) {
		for _, name := range have {
			if _, err = tx.ExecContext(ctx, `DROP TRIGGER `+name); err != nil {
				break
			}
		}
		for _, d := range defs {
			if err != nil {
				break
			}
			_, err = tx.ExecContext(ctx, "CREATE TRIGGER "+d.name+" "+d.sql)
		}
	}
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
