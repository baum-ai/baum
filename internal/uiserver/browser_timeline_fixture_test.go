package uiserver_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/baum-ai/baum/decisions"
	"github.com/baum-ai/baum/internal/decisionlog"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/repos"
)

// browserTimeline seeds a decision store for acme/demo, as capture and the
// feature log would leave it, and serves the Timeline over it: three
// sessions' decisions and four features, one shipped, one in progress with
// a session on its own, one abandoned and one whose text is not written
// yet. acme/other has decisions on, one session's decision and no feature
// history.
func browserTimeline(t *testing.T, home string, registry *repos.Registry) *decisionlog.Timeline {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(home, "decisions", "decisions.sqlite")
	store, err := decisionlog.Open(path, decisionlog.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	const repo = "acme/demo"
	day := func(days int, hour int) time.Time {
		now := time.Now()
		return time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, time.Local).AddDate(0, 0, -days)
	}
	sessions := []struct {
		id, branch string
		at         time.Time
		decisions  []decisions.Decision
	}{
		{"timeline-1", "feature", day(1, 10), []decisions.Decision{
			{ID: "dec-local", Summary: "Serve the Timeline from the daemon's own decision store", Category: decisions.CategoryArchitecture,
				Who: decisions.WhoHuman, Turns: []int{1}, RejectedAlternative: "Sync the hosted log down", Files: []string{"internal/decisionlog/timeline.go"}},
			{ID: "dec-viewer", Summary: "Draw it with the review viewer's decision log components", Category: decisions.CategoryInterface,
				Who: decisions.WhoApproved, Turns: []int{2}, Files: []string{"app/src/timeline.tsx"}},
		}},
		{"timeline-2", "search", day(3, 15), []decisions.Decision{
			{ID: "dec-fts", Summary: "Match keywords with SQLite FTS5", Category: decisions.CategoryImplementation, Who: decisions.WhoAgent,
				Turns: []int{1}, Files: []string{"internal/decisionlog/featurestore.go"}},
		}},
		{"timeline-3", "hosted", day(9, 11), []decisions.Decision{
			{ID: "dec-hosted", Summary: "Try a hosted-only Timeline first", Category: decisions.CategoryScope, Who: decisions.WhoHuman,
				Turns: []int{1}, Files: []string{}},
		}},
	}
	// timeline-1's transcript is on this computer: its first prompt names
	// an address the Timeline masks, and its third is only a wrapper. The
	// others' are gone.
	transcriptPath := filepath.Join(home, ".claude", "projects", "-fixture-demo", "timeline-1.jsonl")
	writeFixtureTranscript(transcriptPath, "timeline-1", day(1, 10), []string{
		"Serve the Timeline from the daemon's own store; ask ada.lovelace@example.com if the hosted log should sync down.",
		"Draw it with the review viewer's decision log components.",
		"<local-command-stdout>Set model to opus</local-command-stdout>",
	})
	for _, s := range sessions {
		times := map[int]time.Time{}
		for i := range 3 {
			times[i+1] = s.at.Add(time.Duration(i) * 20 * time.Minute)
		}
		path := ""
		if s.id == "timeline-1" {
			path = transcriptPath
		}
		if err := store.Save(ctx, decisionlog.Extraction{Meta: decisionlog.Meta{Agent: "claude-code", ID: s.id, Repository: repo, Branch: s.branch, Path: path},
			ContentHash: "c-" + s.id, Decisions: s.decisions, TurnTimes: times}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Save(ctx, decisionlog.Extraction{Meta: decisionlog.Meta{Agent: "codex", ID: "other-1", Repository: "acme/other", Branch: "main"},
		ContentHash: "c-other-1", TurnTimes: map[int]time.Time{1: day(2, 14)}, Decisions: []decisions.Decision{
			{ID: "dec-other", Summary: "Keep the other repository's checks in one workflow", Category: decisions.CategoryImplementation,
				Who: decisions.WhoAgent, Turns: []int{1}, Files: []string{".github/workflows/ci.yml"}},
		}}); err != nil {
		t.Fatal(err)
	}
	version, err := store.LiveVersion(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	ms := func(at time.Time) int64 { return at.UnixMilli() }
	pull := func(number int, title, state string, merged *time.Time, opened, updated time.Time) {
		var mergedAt any
		if merged != nil {
			mergedAt = ms(*merged)
		}
		exec(`INSERT INTO pull_request(repository, number, title, state, author, head_sha, base_sha, created_at, updated_at, merged_at)
VALUES (?,?,?,?,'author',?,'base',?,?,?)`, repo, number, title, state, "head-"+title, ms(opened), ms(updated), mergedAt)
	}
	shipped := day(1, 16)
	pull(12, "Serve the decision log locally", "closed", &shipped, day(2, 9), shipped)
	pull(7, "Reuse the existing PR page", "open", nil, day(1, 9), day(1, 12))
	pull(13, "A hosted-only Timeline", "closed", nil, day(10, 9), day(8, 9))
	// The user opened pull request 12: their login folds it into them.
	exec(`UPDATE pull_request SET author='ada-l' WHERE number=12`)
	feature := func(id, title, description, keys string, behind bool, updated time.Time) {
		summarized := 1
		if behind {
			summarized = 0
		}
		exec(`INSERT INTO feature(id, version_id, repository, title, description, digest, key_decisions_json, membership_version, summarized_version,
changed_at, created_at, updated_at) VALUES (?,?,?,?,?,?,?,1,?,?,?,?)`, id, version, repo, title, description,
			"Decisions and features captured on this computer, shown without the hosted service.", keys, summarized, ms(updated), ms(updated), ms(updated))
	}
	feature("feature-local", "Local decision log", "Shows the features and decisions captured on this computer in the app.",
		`[{"id":"dec-local","agent":"claude-code","session":"timeline-1","turn":1},{"id":"dec-viewer","agent":"claude-code","session":"timeline-1","turn":2}]`, false, day(1, 9))
	feature("feature-search", "Keyword search over decisions", "Finds decisions and features by the words in them.", `[]`, false, day(3, 9))
	feature("feature-hosted", "Hosted-only Timeline", "An earlier attempt that kept the Timeline on the hosted service.", `[]`, false, day(10, 9))
	feature("feature-pending", "", "", `[]`, true, day(4, 9))
	member := func(number int, id string) {
		exec(`INSERT INTO feature_pull(version_id, repository, number, feature_id, assigned_by, assigned_at) VALUES (?,?,?,?,'assigner',?)`,
			version, repo, number, id, ms(day(1, 9)))
	}
	member(12, "feature-local")
	member(7, "feature-local")
	member(13, "feature-hosted")
	own := func(session, id string) {
		exec(`INSERT INTO feature_session(version_id, repository, agent, session_id, feature_id, assigned_at) VALUES (?,?,'claude-code',?,?,?)`,
			version, repo, session, id, ms(day(3, 9)))
	}
	own("timeline-2", "feature-search")
	own("timeline-3", "feature-hosted")
	exec(`INSERT INTO pull_commit(repository, number, sha, committed_at) VALUES (?,12,'commit-1',?)`, repo, ms(day(1, 11)))
	exec(`INSERT INTO session_commit(agent, session_id, repository, sha, method, confidence) VALUES ('claude-code','timeline-1',?,'commit-1','checkpoint',1)`, repo)
	exec(`INSERT INTO pr_summary(repository, number, content_hash, summary, created_at) VALUES (?,12,'h','Adds the local Timeline API and page.',?)`, repo, ms(day(1, 16)))
	return &decisionlog.Timeline{Store: store, Registry: registry, Home: home,
		Clone: func(context.Context, string) decisionlog.TimelineClone { return fixtureClone{} },
		Login: func(context.Context) string { return "ada-l" }}
}

// fixtureClone names the sessions' person and has no saved reviews.
type fixtureClone struct{}

func (fixtureClone) Person(context.Context) *decisionlog.Person {
	name := "Ada Lovelace"
	return &decisionlog.Person{Email: "ada@example.com", Name: &name}
}

func (fixtureClone) Review(context.Context, int, string) (string, string) { return "", "none" }

// fixtureBuilder estimates every feature build at 42 pull requests of a
// truncated history, with discovery first, and
// stops each one at once, as a build whose backend went away.
type fixtureBuilder struct{}

func (fixtureBuilder) EstimateFeatureBuild(context.Context, string, string) (jobs.FeatureBuildEstimate, error) {
	estimate := 0.84
	return jobs.FeatureBuildEstimate{Backend: "claude", Model: "claude-haiku-5-5", PullRequests: 42, EstimateUSD: &estimate, DiscoveryUSD: 5,
		HistoryTruncated: true}, nil
}

func (fixtureBuilder) BuildFeatures(context.Context, string, string, func(jobs.Progress)) (float64, error) {
	return 0, &jobs.StoppedError{Reason: "backend_unavailable"}
}
