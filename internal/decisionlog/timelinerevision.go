package decisionlog

import (
	"fmt"
	"strings"
)

// timelineRevisionSchema keeps, for each repository, a counter that moves
// whenever a row the Timeline's snapshot reads changes (README.md,
// "Timeline API"). Triggers bump it, so every writer is covered, in this
// process or another one, and a write to anything else in app.sqlite (jobs,
// UI state, discovery caches) or to a column the snapshot does not read (a
// build's progress, a retry time) leaves it alone. The bump commits with the
// write that caused it.
var timelineRevisionSchema = buildTimelineRevisionSchema()

type revisionTrigger struct {
	table string
	// updates are the columns whose change counts; none for a table never
	// updated in place. An upsert that writes the same values is no change.
	updates []string
	// repository is the SQL naming the repository, with %s the row (NEW or
	// OLD); "%s.repository" when empty.
	repository string
	// moves also bumps the old repository when an update changes it.
	moves bool
}

// timelineInputs are the tables the snapshot reads, with the columns it
// reads of those updated in place. A new read of the snapshot needs its
// table or column here: TestTimelineRevisionTriggersExist fails for
// a table missing.
var timelineInputs = []revisionTrigger{
	{table: "feature_version", updates: []string{"repository", "state"}},
	{table: "feature", updates: []string{"version_id", "repository", "title", "description", "digest", "key_decisions_json",
		"merged_into", "membership_version", "summarized_version"}},
	{table: "feature_pull", updates: []string{"feature_id", "assigned_paths_json"}},
	{table: "feature_session", updates: []string{"feature_id"}},
	{table: "pull_request", updates: []string{"title", "state", "head_sha", "author", "draft", "merged_at", "bot", "created_at", "updated_at", "paths_json"}},
	{table: "pull_commit"},
	{table: "session_commit"},
	{table: "capture_session", updates: []string{"repository", "branch"}, moves: true},
	{table: "decision", updates: []string{"agent", "session_id", "repository", "title", "summary", "category", "who", "trigger", "rationale",
		"rejected_alternative", "evidence", "superseded", "turns_json", "decided_turn", "decided_at", "branch", "obsolete_at"}, moves: true},
	{table: "decision_file", repository: "COALESCE((SELECT repository FROM decision WHERE decision_id=%s.decision_id), '')"},
}

func buildTimelineRevisionSchema() string {
	var b strings.Builder
	b.WriteString("CREATE TABLE IF NOT EXISTS timeline_revision(repository TEXT PRIMARY KEY, revision INTEGER NOT NULL);\n")
	for _, in := range timelineInputs {
		repository := in.repository
		if repository == "" {
			repository = "%s.repository"
		}
		bump := func(row string) string {
			return fmt.Sprintf("INSERT INTO timeline_revision(repository, revision) VALUES (%s, 1) ON CONFLICT(repository) DO UPDATE SET revision=revision+1;",
				strings.ReplaceAll(repository, "%s", row))
		}
		trigger := func(name, event, body string) {
			fmt.Fprintf(&b, "CREATE TRIGGER IF NOT EXISTS timeline_revision_%s_%s AFTER %s ON %s %s END;\n", in.table, name, event, in.table, body)
		}
		trigger("insert", "INSERT", "BEGIN "+bump("NEW"))
		trigger("delete", "DELETE", "BEGIN "+bump("OLD"))
		if len(in.updates) == 0 {
			continue
		}
		changed := make([]string, len(in.updates))
		for i, column := range in.updates {
			changed[i] = "OLD." + column + " IS NOT NEW." + column
		}
		body := bump("NEW")
		if in.moves {
			body = bump("OLD") + " " + body
		}
		trigger("update", "UPDATE OF "+strings.Join(in.updates, ", ")+" ", "WHEN "+strings.Join(changed, " OR ")+" BEGIN "+body)
	}
	return b.String()
}
