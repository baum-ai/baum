package decisionlog

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/baum-ai/baum/decisions"
)

// A write the Timeline's snapshot does not read leaves the repository's
// shared snapshot in place; one it reads builds it again. The whole of
// app.sqlite is shared, so the writes of jobs, UI state and builds' progress
// must not count.
func TestTimelineRevisionOnlyMovesWithTimelineInputs(t *testing.T) {
	f := newTimelineFixture(t)
	list := func() {
		t.Helper()
		if code := f.get("/api/timeline/features", url.Values{"repository": {repo}}, nil); code != 200 {
			t.Fatalf("features = %d", code)
		}
	}
	list()
	version, err := f.store.LiveVersion(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	check := func(name string, rebuilds bool, query string, args ...any) {
		t.Helper()
		before := f.clones
		f.exec(query, args...)
		list()
		if built := f.clones != before; built != rebuilds {
			t.Errorf("%s: rebuilt = %v, want %v", name, built, rebuilds)
		}
	}
	// Not read by the snapshot.
	check("app document", false, `INSERT INTO app_documents(bucket, key, value) VALUES ('ui', 'k', x'00') ON CONFLICT DO UPDATE SET value=x'01'`)
	check("capture attempt", false, `INSERT INTO capture_attempt(agent, session_id, repository, backend, model, mode, started_at, state, reserved_usd)
VALUES ('claude-code','s1',?,'b','m','full',1,'pending',0)`, repo)
	check("build progress", false, `UPDATE feature_version SET total=9, placed=3, spent_usd=1.5 WHERE id=?`, version)
	check("pull sync", false, `UPDATE pull_sync SET etag='e' WHERE repository=?`, repo)
	check("settle", false, `INSERT INTO feature_settle(version_id, repository, kind, item) VALUES (?,?,'pull','1')`, version, repo)
	check("paths retry", false, `UPDATE pull_request SET paths_retry_at=5, paths_failures=1 WHERE number=1`)
	check("checked head", false, `UPDATE feature_pull SET checked_head_sha='x' WHERE number=1`)
	check("feature bookkeeping", false, `UPDATE feature SET dirty_since=7, changed_at=8, split_checked_version=1 WHERE id='F1'`)
	check("extraction cost", false, `UPDATE capture_session SET input_tokens=5, decisions_json='[]' WHERE session_id='s1'`)
	check("same pull values", false, `UPDATE pull_request SET title=title WHERE number=1`)
	check("same session upsert", false, `INSERT INTO capture_session(agent, session_id, repository, branch, coverage, updated_at)
SELECT agent, session_id, repository, branch, coverage, 1 FROM capture_session WHERE session_id='s1' ON CONFLICT(agent, session_id) DO UPDATE SET
repository=excluded.repository, branch=excluded.branch, updated_at=excluded.updated_at`)
	// Another repository's rows are not this one's.
	check("other repository", false, `UPDATE feature SET title='Counts' WHERE id='G1'`)
	// Read by it.
	check("feature text", true, `UPDATE feature SET title='Renamed' WHERE id='F1'`)
	check("feature merged", true, `UPDATE feature SET merged_into='F1' WHERE id='F2'`)
	check("feature added", true, `INSERT INTO feature(id, version_id, repository, changed_at, created_at, updated_at) VALUES ('F9',?,?,0,1,1)`, version, repo)
	check("feature removed", true, `DELETE FROM feature WHERE id='F9'`)
	check("pull moved", true, `UPDATE feature_pull SET feature_id='F2' WHERE number=2`)
	check("pull assigned", true, `INSERT INTO feature_pull(version_id, repository, number, feature_id, assigned_by, assigned_at) VALUES (?,?,3,'F1','a',1)`, version, repo)
	check("session assigned", true, `UPDATE feature_session SET feature_id='F1' WHERE session_id='s2'`)
	check("session unassigned", true, `DELETE FROM feature_session WHERE session_id='s2'`)
	check("pull request", true, `UPDATE pull_request SET title='New title' WHERE number=1`)
	check("pull paths", true, `UPDATE pull_request SET paths_json='["z.go"]' WHERE number=1`)
	check("pull commit", true, `INSERT INTO pull_commit(repository, number, sha, committed_at) VALUES (?,2,'c2',1)`, repo)
	check("session commit", true, `INSERT INTO session_commit(agent, session_id, repository, sha, method, confidence) VALUES ('claude-code','s2',?,'c2','x',1)`, repo)
	check("session commit removed", true, `DELETE FROM session_commit WHERE sha='c2'`)
	check("session branch", true, `UPDATE capture_session SET branch='other' WHERE session_id='s1'`)
	check("decision text", true, `UPDATE decision SET summary='Changed.' WHERE decision_id='d-s1'`)
	check("decision obsolete", true, `UPDATE decision SET obsolete_at=1 WHERE decision_id='d-s1'`)
	check("decision file", true, `INSERT INTO decision_file(decision_id, path) VALUES ('d-s2', 'new.go')`)
	check("decision file removed", true, `DELETE FROM decision_file WHERE decision_id='d-s2'`)
	// A session moved to this repository moves both revisions.
	check("session moved in", true, `UPDATE capture_session SET repository=? WHERE session_id='g1'`, repo)
	before := f.clones
	f.get("/api/timeline/features", url.Values{"repository": {gadgets}}, nil)
	if f.clones == before {
		t.Error("session moved out: the old repository's snapshot was kept")
	}
	// A retired version leaves the repository without features.
	revision, err := f.store.revision(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	f.exec(`UPDATE feature_version SET state='retired' WHERE id=?`, version)
	if after, err := f.store.revision(context.Background(), repo); err != nil || after <= revision {
		t.Errorf("version retired: revision %d after %d, %v", after, revision, err)
	}
}

// A capture saves its decisions through the store: that moves the revision.
func TestTimelineRevisionMovesWithCapture(t *testing.T) {
	f := newTimelineFixture(t)
	ctx := context.Background()
	revision := func() int64 {
		t.Helper()
		n, err := f.store.revision(ctx, repo)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	save := func(summary string) {
		t.Helper()
		if err := f.store.Save(ctx, Extraction{Meta: Meta{Agent: "claude-code", ID: "s3", Repository: repo, Branch: "b"}, ContentHash: summary,
			Decisions: []decisions.Decision{{ID: "d-s3", Summary: summary, Category: decisions.CategoryScope, Who: decisions.WhoHuman, Turns: []int{1}, Files: []string{"a.go"}}}}); err != nil {
			t.Fatal(err)
		}
	}
	save("One.")
	first := revision()
	save("Two.")
	if second := revision(); second <= first {
		t.Fatalf("revision after a changed decision = %d, was %d", second, first)
	}
	f.exec(`INSERT INTO app_documents(bucket, key, value) VALUES ('ui', 'a', x'00')`)
	if n := revision(); n <= first {
		t.Fatalf("revision = %d, was %d", n, first)
	}
}

// Every table the snapshot reads has its triggers: a table dropped from
// timelineInputs leaves the Timeline stale.
func TestTimelineRevisionTriggersExist(t *testing.T) {
	f := newTimelineFixture(t)
	for _, in := range timelineInputs {
		for _, event := range []string{"insert", "delete"} {
			var n int
			name := fmt.Sprintf("timeline_revision_%s_%s", in.table, event)
			if err := f.store.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&n); err != nil || n != 1 {
				t.Errorf("%s: %d, %v", name, n, err)
			}
		}
	}
}

// The queries the decisions page, its coverage, the people count and the
// feature assigner make use the repository indexes, not a scan of every
// session or decision.
func TestDecisionQueriesUseIndexes(t *testing.T) {
	f := newTimelineFixture(t)
	for i := range 40 {
		f.exec(`INSERT INTO capture_session(agent, session_id, repository, coverage, extracted_at, updated_at) VALUES ('claude-code', ?, ?, 'extracted', ?, 1)`,
			fmt.Sprintf("x%d", i), fmt.Sprintf("o/r%d", i%8), i)
		f.exec(`INSERT INTO decision(decision_id, agent, session_id, repository, ordinal, title, summary, category, who, trigger, rationale, rejected_alternative,
evidence, superseded, turns_json, first_turn, decided_turn, decided_at, created_at) VALUES (?, 'claude-code', ?, ?, 0, '', '', 'scope', 'human', '', '', '', '', 0, '[]', 1, 1, ?, 1)`,
			fmt.Sprintf("dx%d", i), fmt.Sprintf("x%d", i), fmt.Sprintf("o/r%d", i%8), i)
	}
	if _, err := f.store.db.Exec(`ANALYZE`); err != nil {
		t.Fatal(err)
	}
	queries := map[string]struct{ query, index string }{
		"decisions page": {`SELECT d.decision_id FROM decision d JOIN capture_session cs ON cs.agent=d.agent AND cs.session_id=d.session_id
WHERE d.obsolete_at IS NULL AND cs.repository IN ('o/r1') AND d.superseded=0 ORDER BY d.decided_at DESC, d.decision_id LIMIT 51`, "capture_session_repository"},
		"coverage": {`SELECT coverage, skip_reason, count(*) FROM capture_session WHERE repository IN ('o/r1') GROUP BY 1, 2`, "capture_session_repository"},
		"people": {`SELECT count(*) FROM decision d JOIN capture_session cs ON cs.agent=d.agent AND cs.session_id=d.session_id
WHERE d.obsolete_at IS NULL AND cs.repository IN ('o/r1')`, "capture_session_repository"},
		"assigner": {`SELECT cs.agent FROM capture_session cs WHERE cs.repository='o/r1' AND cs.extracted_at >= 5 AND cs.extracted_at <= 30`, "capture_session_repository"},
	}
	for name, q := range queries {
		rows, err := f.store.db.Query(`EXPLAIN QUERY PLAN ` + q.query)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		_ = rows.Close()
		text := strings.Join(plan, "\n")
		if testing.Verbose() {
			t.Logf("%s:\n%s", name, text)
		}
		if !strings.Contains(text, q.index) {
			t.Errorf("%s does not use %s:\n%s", name, q.index, text)
		}
	}
}
