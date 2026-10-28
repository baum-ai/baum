package decisionlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/decisions"
	"github.com/baum-ai/baum/internal/repos"
)

type timelineRegistry struct {
	prefs repos.Preferences
	list  []repos.Repository
}

func (r timelineRegistry) Preferences(context.Context) (repos.Preferences, error) {
	return r.prefs, nil
}
func (r timelineRegistry) Repositories(context.Context) ([]repos.Repository, error) {
	return r.list, nil
}

type timelineFixture struct {
	*featureFixture
	handler  http.Handler
	registry *timelineRegistry
	// clones counts the clones opened, one per request.
	clones int
}

// fakeClone names the sessions' person and has a current review of pull
// request 1 at h1.
type fakeClone struct{}

func (fakeClone) Person(context.Context) *Person { return &Person{Email: "me@example.com"} }
func (fakeClone) Review(_ context.Context, number int, head string) (string, string) {
	if number == 1 && head == "h1" {
		return "Reviewed export.", "current"
	}
	return "", "none"
}

// newTimelineFixture stores two features of acme/widgets' current version
// and one merged into the first: "Widget export" with pull requests 1
// (open, linked to session s1's commit) and 2 (merged, with a generated
// summary), and an untitled feature behind its members holding session s2
// on its own. acme/off has its decisions turned off.
func newTimelineFixture(t *testing.T) *timelineFixture {
	f := &timelineFixture{featureFixture: newFeatureFixture(t)}
	ctx := context.Background()
	version, err := f.store.LiveVersion(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	f.pull(1, "Export widgets as CSV", t0, "h1", []string{"export.go"})
	f.pull(2, "Add the export button", t0.Add(time.Hour), "h2", []string{"button.go"})
	f.exec(`UPDATE pull_request SET state='closed', merged_at=?, author='octo' WHERE number=2`, t0.Add(2*time.Hour).UnixMilli())
	f.exec(`UPDATE pull_request SET author='octo' WHERE number=1`)
	f.saveSession("s1", "export.go")
	if err := f.store.Save(ctx, Extraction{Meta: Meta{Agent: "claude-code", ID: "s2", Repository: repo, Branch: "draft"}, ContentHash: "c-s2",
		Decisions: []decisions.Decision{
			{ID: "d-s2", Summary: "Keep drafts in memory.", Category: decisions.CategoryArchitecture, Who: decisions.WhoAgent, Turns: []int{2}, Files: []string{}},
			{ID: "d-s2-old", Summary: "Store drafts on disk.", Category: decisions.CategoryArchitecture, Who: decisions.WhoAgent, Turns: []int{1}, Superseded: true, Files: []string{}},
		}}); err != nil {
		t.Fatal(err)
	}
	feature := func(id, title, keys string, membership, summarized int, merged any) {
		f.exec(`INSERT INTO feature(id, version_id, repository, title, description, digest, key_decisions_json, membership_version, summarized_version,
changed_at, merged_into, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, version, repo, title, "About "+title, "Digest of "+title,
			keys, membership, summarized, t0.UnixMilli(), merged, t0.UnixMilli(), t0.UnixMilli())
	}
	feature("F1", "Widget export", `[{"id":"d-s1","agent":"claude-code","session":"s1","turn":1}]`, 2, 2, nil)
	feature("F2", "", `[]`, 1, 0, nil)
	feature("F3", "Old export", `[]`, 1, 1, "F1")
	for _, number := range []int{1, 2} {
		f.exec(`INSERT INTO feature_pull(version_id, repository, number, feature_id, assigned_by, assigned_at) VALUES (?,?,?,?,?,?)`,
			version, repo, number, "F1", "assigner", t0.UnixMilli())
	}
	f.exec(`INSERT INTO feature_session(version_id, repository, agent, session_id, feature_id, assigned_at) VALUES (?,?,?,?,?,?)`,
		version, repo, "claude-code", "s2", "F2", t0.UnixMilli())
	f.exec(`INSERT INTO pull_commit(repository, number, sha, committed_at) VALUES (?,1,'c1',?)`, repo, t0.UnixMilli())
	f.exec(`INSERT INTO session_commit(agent, session_id, repository, sha, method, confidence) VALUES ('claude-code','s1',?,'c1','checkpoint',1)`, repo)
	f.exec(`INSERT INTO pr_summary(repository, number, content_hash, summary, created_at) VALUES (?,2,'h','Adds the button.',?)`, repo, t0.UnixMilli())
	off := false
	f.registry = &timelineRegistry{prefs: repos.DefaultPreferences(),
		list: []repos.Repository{{Repository: repo}, {Repository: "acme/off", Settings: repos.Settings{Decisions: &off}}}}
	timeline := &Timeline{Store: f.store, Registry: f.registry,
		Clone: func(context.Context, string) TimelineClone {
			f.clones++
			return fakeClone{}
		}}
	f.handler = timeline.Handler()
	return f
}

func (f *timelineFixture) get(path string, query url.Values, out any) int {
	f.t.Helper()
	target := path
	if query != nil {
		target += "?" + query.Encode()
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
	if w.Code == http.StatusOK && out != nil {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			f.t.Fatal(err)
		}
	}
	return w.Code
}

type featurePage struct {
	Features []struct {
		ID               string  `json:"id"`
		Title            *string `json:"title"`
		Status           string  `json:"status"`
		SummaryPending   bool    `json:"summary_pending"`
		PullRequestCount int     `json:"pull_request_count"`
		SessionCount     int     `json:"session_count"`
		DecisionCount    int     `json:"decision_count"`
		People           []Person
		KeyDecisions     []timelineKeyDecision `json:"key_decisions"`
	} `json:"features"`
	NextCursor *string `json:"next_cursor"`
}

func (p featurePage) ids() string {
	var ids []string
	for _, f := range p.Features {
		ids = append(ids, f.ID)
	}
	return strings.Join(ids, ",")
}

func TestTimelineRepositoriesAndScope(t *testing.T) {
	f := newTimelineFixture(t)
	var list struct {
		Repositories []struct{ Repository string } `json:"repositories"`
	}
	f.get("/api/timeline/repositories", nil, &list)
	if len(list.Repositories) != 1 || list.Repositories[0].Repository != repo {
		t.Fatalf("repositories = %+v", list)
	}
	for _, name := range []string{"acme/off", "acme/unknown", ""} {
		if code := f.get("/api/timeline/features", url.Values{"repository": {name}}, nil); code != http.StatusBadRequest {
			t.Fatalf("features of %q = %d", name, code)
		}
	}
	for _, bad := range []url.Values{{"cursor": {"x"}}, {"cursor": {"1|"}}, {"limit": {"0"}}, {"limit": {"501"}}} {
		bad.Set("repository", repo)
		if code := f.get("/api/timeline/decisions", bad, nil); code != http.StatusBadRequest {
			t.Fatalf("%v = %d", bad, code)
		}
	}
	var listed struct {
		Enabled   bool     `json:"decisions_enabled"`
		TurnedOff []string `json:"turned_off"`
	}
	f.get("/api/timeline/repositories", nil, &listed)
	if !listed.Enabled || len(listed.TurnedOff) != 1 || listed.TurnedOff[0] != "acme/off" {
		t.Fatalf("repositories = %+v", listed)
	}
	// A detail of a repository turned off is not found.
	off := false
	f.registry.list[0].Settings.Decisions = &off
	for _, path := range []string{"/api/timeline/features/F1", "/api/timeline/decisions/d-s1"} {
		if code := f.get(path, nil, nil); code != http.StatusNotFound {
			t.Fatalf("%s of a repository turned off = %d", path, code)
		}
	}
	f.registry.list[0].Settings.Decisions = nil
	f.registry.prefs.Decisions.Enabled = &off
	f.get("/api/timeline/repositories", nil, &listed)
	if listed.Enabled {
		t.Fatal("the decisions switch reads as on")
	}
	if code := f.get("/api/timeline/decisions/d-s1", nil, nil); code != http.StatusNotFound {
		t.Fatalf("decision with the switch off = %d", code)
	}
}

func TestTimelineFeatures(t *testing.T) {
	f := newTimelineFixture(t)
	var page featurePage
	f.get("/api/timeline/features", url.Values{"repository": {"ACME/Widgets"}}, &page)
	// F1's last activity is pull request 2's update, after F2's decisions.
	if page.ids() != "F1,F2" || page.NextCursor != nil {
		t.Fatalf("features = %s", page.ids())
	}
	one, two := page.Features[0], page.Features[1]
	if *one.Title != "Widget export" || one.Status != "in_progress" || one.PullRequestCount != 2 || one.SessionCount != 1 || one.DecisionCount != 1 {
		t.Fatalf("F1 = %+v", one)
	}
	if len(one.KeyDecisions) != 1 || one.KeyDecisions[0].ID != "d-s1" || one.KeyDecisions[0].SessionID != "claude-code:s1" {
		t.Fatalf("F1 key decisions = %+v", one.KeyDecisions)
	}
	if len(one.People) != 2 || one.People[0].Email != "me@example.com" || *one.People[1].Login != "octo" {
		t.Fatalf("F1 people = %+v", one.People)
	}
	if two.Title != nil || !two.SummaryPending || two.SessionCount != 1 || two.DecisionCount != 2 {
		t.Fatalf("F2 = %+v", two)
	}
	for query, want := range map[string]string{"widget": "F1", "button": "F1", "#2": "F1", "nothing": "", "export%": "F1"} {
		page = featurePage{}
		f.get("/api/timeline/features", url.Values{"repository": {repo}, "q": {query}}, &page)
		if page.ids() != want {
			t.Errorf("q=%q = %q, want %q", query, page.ids(), want)
		}
	}
	page = featurePage{}
	f.get("/api/timeline/features", url.Values{"repository": {repo}, "status": {"shipped,abandoned"}}, &page)
	if page.ids() != "" {
		t.Fatalf("shipped = %s", page.ids())
	}
	// Text written later is found.
	f.exec(`UPDATE feature SET title='Draft keeping' WHERE id='F2'`)
	page = featurePage{}
	f.get("/api/timeline/features", url.Values{"repository": {repo}, "q": {"keep"}}, &page)
	if page.ids() != "F2" {
		t.Fatalf("q=keep = %s", page.ids())
	}
}

func TestTimelineFeatureDetail(t *testing.T) {
	f := newTimelineFixture(t)
	var detail struct {
		Feature struct {
			ID     string  `json:"id"`
			Digest *string `json:"digest"`
		} `json:"feature"`
		PullRequests []struct {
			Number      int     `json:"number"`
			State       string  `json:"state"`
			Merged      bool    `json:"merged"`
			ReviewState string  `json:"review_state"`
			Summary     *string `json:"summary"`
			Decisions   []timelineDecision
		} `json:"pull_requests"`
		Sessions []timelineSession `json:"sessions"`
	}
	// A merged-away feature answers with its survivor.
	f.get("/api/timeline/features/F3", nil, &detail)
	if detail.Feature.ID != "F1" || *detail.Feature.Digest != "Digest of Widget export" || len(detail.PullRequests) != 2 {
		t.Fatalf("detail = %+v", detail)
	}
	newest, oldest := detail.PullRequests[0], detail.PullRequests[1]
	if newest.Number != 2 || !newest.Merged || newest.State != "closed" || *newest.Summary != "Adds the button." || len(newest.Decisions) != 0 {
		t.Fatalf("PR 2 = %+v", newest)
	}
	if oldest.ReviewState != "current" || *oldest.Summary != "Reviewed export." || len(oldest.Decisions) != 1 || oldest.Decisions[0].ID != "d-s1" ||
		oldest.Decisions[0].Vendor != "claude_code" || oldest.Decisions[0].Person.Email != "me@example.com" {
		t.Fatalf("PR 1 = %+v", oldest)
	}
	if len(detail.Sessions) != 0 {
		t.Fatalf("sessions = %+v", detail.Sessions)
	}
	detail.Sessions = nil
	f.get("/api/timeline/features/F2", nil, &detail)
	if len(detail.Sessions) != 1 || detail.Sessions[0].ID != "claude-code:s2" || detail.Sessions[0].Branch != "draft" || len(detail.Sessions[0].Decisions) != 2 {
		t.Fatalf("F2 sessions = %+v", detail.Sessions)
	}
	// Every list is an array, empty ones too: the viewer reads them.
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/timeline/features/F1", nil))
	if !strings.Contains(w.Body.String(), `"summary":"Adds the button.","decisions":[]`) || !strings.Contains(w.Body.String(), `"sessions":[]`) {
		t.Fatalf("F1 = %s", w.Body)
	}
	if code := f.get("/api/timeline/features/nope", nil, nil); code != http.StatusNotFound {
		t.Fatalf("unknown = %d", code)
	}
	// A feature of a version no longer in use is archived.
	f.exec(`UPDATE feature_version SET state='retired'`)
	f.exec(`INSERT INTO feature_version(repository, state, created_at) VALUES (?, 'current', 0)`, repo)
	var archived map[string]bool
	f.get("/api/timeline/features/F1", nil, &archived)
	if !archived["archived"] {
		t.Fatalf("archived = %v", archived)
	}
}

func TestTimelineDecisions(t *testing.T) {
	f := newTimelineFixture(t)
	f.exec(`INSERT INTO capture_attempt(agent, session_id, repository, backend, model, mode, started_at, state, reserved_usd, stage)
VALUES ('claude-code','s3',?,'claude','m','full',0,'pending',0,'capture')`, repo)
	type page struct {
		Decisions []timelineDecision `json:"decisions"`
		Coverage  timelineCoverage   `json:"coverage"`
	}
	ids := func(p page) string {
		var out []string
		for _, d := range p.Decisions {
			out = append(out, d.ID)
		}
		return strings.Join(out, ",")
	}
	var all page
	f.get("/api/timeline/decisions", url.Values{"repository": {repo}}, &all)
	if len(all.Decisions) != 2 || all.Coverage.Extracted != 2 || all.Coverage.Pending != 1 {
		t.Fatalf("decisions = %s, coverage %+v", ids(all), all.Coverage)
	}
	for query, want := range map[string]string{
		"q=drafts":                    "d-s2",
		"category=architecture":       "d-s2",
		"category=architecture,scope": "d-s2",
		"who=human":                   "d-s1",
		"file=export.go":              "d-s1",
		"file=expo":                   "",
		"superseded=1&q=drafts":       "d-s2,d-s2-old",
	} {
		q, _ := url.ParseQuery(query + "&repository=" + repo)
		var got page
		f.get("/api/timeline/decisions", q, &got)
		// Both drafts decisions date from the extraction; order by id.
		if ids(got) != want && (want != "d-s2,d-s2-old" || ids(got) != "d-s2-old,d-s2") {
			t.Errorf("%s = %q, want %q", query, ids(got), want)
		}
	}
	var detail struct {
		Session         timelineSession `json:"session"`
		Decisions       []struct{ ID string }
		PromptsWithheld bool           `json:"prompts_withheld"`
		PullRequests    []timelinePull `json:"pull_requests"`
	}
	f.get("/api/timeline/decisions/d-s1", nil, &detail)
	if !detail.PromptsWithheld || detail.Session.ID != "claude-code:s1" || len(detail.Decisions) != 1 ||
		len(detail.PullRequests) != 1 || detail.PullRequests[0].Number != 1 || detail.PullRequests[0].ReviewState != "current" {
		t.Fatalf("detail = %+v", detail)
	}
	if code := f.get("/api/timeline/decisions/nope", nil, nil); code != http.StatusNotFound {
		t.Fatalf("unknown = %d", code)
	}
}

// Pages follow on from the last row they named, so a row added at the top
// meanwhile repeats none; a request opens its clone once.
func TestTimelinePagesByKeyset(t *testing.T) {
	f := newTimelineFixture(t)
	var seen []string
	cursor := ""
	for range 5 {
		q := url.Values{"repository": {repo}, "limit": {"1"}, "superseded": {"1"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page struct {
			Decisions  []timelineDecision `json:"decisions"`
			NextCursor *string            `json:"next_cursor"`
		}
		f.get("/api/timeline/decisions", q, &page)
		for _, d := range page.Decisions {
			seen = append(seen, d.ID)
		}
		if cursor == "" {
			// A newer decision arrives after the first page.
			f.clock.now = t0.Add(48 * time.Hour)
			f.saveSession("s9", "late.go")
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if strings.Join(seen, ",") != "d-s1,d-s2,d-s2-old" && strings.Join(seen, ",") != "d-s1,d-s2-old,d-s2" {
		t.Fatalf("pages = %v", seen)
	}
	seen = nil
	cursor = ""
	for range 5 {
		q := url.Values{"repository": {repo}, "limit": {"1"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page featurePage
		f.get("/api/timeline/features", q, &page)
		seen = append(seen, page.ids())
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if strings.Join(seen, ",") != "F1,F2" {
		t.Fatalf("feature pages = %v", seen)
	}
	f.clones = 0
	f.get("/api/timeline/features/F1", nil, &struct{}{})
	if f.clones != 1 {
		t.Fatalf("a detail opened %d clones", f.clones)
	}
}

// A store from before the index is indexed when opened; pruned decisions
// leave it, and a prefix finds a longer word.
func TestTimelineSearchIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions", "decisions.sqlite")
	s, err := Open(path, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := &clock{t0}
	s.now = c.Now
	ctx := context.Background()
	save := func(id string, list ...decisions.Decision) {
		t.Helper()
		if err := s.Save(ctx, Extraction{Meta: Meta{Agent: "claude-code", ID: id, Repository: repo}, ContentHash: "c-" + id + list[0].ID, Decisions: list}); err != nil {
			t.Fatal(err)
		}
	}
	save("s1", decision("d1", "Exporting widgets as spreadsheets", 1))
	// Drop the index, as a store from before it had none.
	for _, name := range []string{"decision_search", "feature_search", "pull_search"} {
		if _, err := s.db.Exec(`DROP TABLE ` + name); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"decision", "feature", "pull"} {
		for _, op := range []string{"insert", "delete", "update"} {
			if _, err := s.db.Exec(`DROP TRIGGER ` + name + `_search_` + op); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err = Open(path, OpenOptions{}); err != nil {
		t.Fatal(err)
	}
	s.now = c.Now
	t.Cleanup(func() { _ = s.Close() })
	match := func(q string) []string {
		t.Helper()
		ids, err := collect(scanString)(s.db.Query(`SELECT d.decision_id FROM decision d WHERE d.rowid IN
(SELECT rowid FROM decision_search WHERE decision_search MATCH ?) ORDER BY 1`, matchQuery(q)))
		if err != nil {
			t.Fatal(err)
		}
		return ids
	}
	if got := match("export spreadsheet"); len(got) != 1 || got[0] != "d1" {
		t.Fatalf("export spreadsheet = %v", got)
	}
	// A new extraction makes d1 obsolete; pruned later, it leaves the index.
	save("s1", decision("d2", "Exporting widgets as CSV", 1))
	c.now = t0.Add(Retention + 48*time.Hour)
	if err := s.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if got := match("spreadsheet"); len(got) != 0 {
		t.Fatalf("pruned decision still found: %v", got)
	}
	if got := match("csv"); len(got) != 1 || got[0] != "d2" {
		t.Fatalf("csv = %v", got)
	}
	if _, err := s.db.Exec(`INSERT INTO decision_search(decision_search) VALUES ('integrity-check')`); err != nil {
		t.Fatal(err)
	}
}

// The set queries read the sessions by their (agent, session_id) index.
func TestCurrentOfUsesTheSessionIndex(t *testing.T) {
	s := openStore(t, &clock{t0})
	set, err := sessionSet([]SessionKey{{Agent: "claude-code", ID: "s1"}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := collect(func(rows *sql.Rows) (string, error) {
		var id, parent, unused int
		var detail string
		return detail, rows.Scan(&id, &parent, &unused, &detail)
	})(s.db.Query(`EXPLAIN QUERY PLAN SELECT decision_id FROM current_decision
WHERE (agent, session_id)`+inSessions, set))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(plan, "\n"), "decision_session") {
		t.Fatalf("plan = %v", plan)
	}
}
