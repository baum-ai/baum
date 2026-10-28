package decisionlog

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/decisions"
	"github.com/baum-ai/baum/internal/livesessions"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/transcript"
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
	timeline *Timeline
	registry *timelineRegistry
	// clones counts the clones opened, one per request.
	clones int
}

// fakeClone names the sessions' person by its address, Ada by default, and
// has a current review of pull request 1 at h1.
type fakeClone struct{ email string }

func (c fakeClone) Person(context.Context) *Person {
	name := "Ada"
	return &Person{Email: cmp.Or(c.email, "me@example.com"), Name: &name}
}
func (fakeClone) Review(_ context.Context, number int, head string) (string, string) {
	if number == 1 && head == "h1" {
		return "Reviewed export.", "current"
	}
	return "", "none"
}

const gadgets = "acme/gadgets"

// newTimelineFixture stores two features of acme/widgets' current version
// and one merged into the first: "Widget export" with pull requests 1
// (open, linked to session s1's commit) and 2 (merged, with a generated
// summary), and an untitled feature behind its members holding session s2
// on its own. acme/gadgets has feature G1 holding codex session g1 on its
// own; acme/off has its decisions turned off.
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
	// acme/gadgets: session g1 on its own in feature G1.
	if err := f.store.Save(ctx, Extraction{Meta: Meta{Agent: "codex", ID: "g1", Repository: gadgets, Branch: "main"}, ContentHash: "c-g1",
		Decisions: []decisions.Decision{{ID: "d-g1", Summary: "Count gadgets nightly.", Category: decisions.CategoryScope, Who: decisions.WhoHuman,
			Turns: []int{1}, Files: []string{"count.go"}}}}); err != nil {
		t.Fatal(err)
	}
	gadgetVersion, err := f.store.LiveVersion(ctx, gadgets)
	if err != nil {
		t.Fatal(err)
	}
	f.exec(`INSERT INTO feature(id, version_id, repository, title, description, digest, key_decisions_json, membership_version, summarized_version,
changed_at, created_at, updated_at) VALUES ('G1',?,?,'Gadget counts','About counts','Digest','[]',1,1,0,?,?)`, gadgetVersion, gadgets,
		t0.Add(-time.Hour).UnixMilli(), t0.UnixMilli())
	f.exec(`INSERT INTO feature_session(version_id, repository, agent, session_id, feature_id, assigned_at) VALUES (?,?,'codex','g1','G1',?)`,
		gadgetVersion, gadgets, t0.UnixMilli())
	off := false
	f.registry = &timelineRegistry{prefs: repos.DefaultPreferences(),
		list: []repos.Repository{{Repository: repo}, {Repository: gadgets}, {Repository: "acme/off", Settings: repos.Settings{Decisions: &off}}}}
	f.timeline = &Timeline{Store: f.store, Registry: f.registry, Home: t.TempDir(),
		Clone: func(_ context.Context, repository string) TimelineClone {
			f.clones++
			// acme/gadgets, served first, names the user.
			if repository == repo {
				return fakeClone{email: "ada@widgets.example"}
			}
			return fakeClone{}
		}}
	f.handler = f.timeline.Handler()
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
		Repository       *timelineRepository `json:"repository"`
		ID               string              `json:"id"`
		Title            *string             `json:"title"`
		Status           string              `json:"status"`
		SummaryPending   bool                `json:"summary_pending"`
		PullRequestCount int                 `json:"pull_request_count"`
		SessionCount     int                 `json:"session_count"`
		DecisionCount    int                 `json:"decision_count"`
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
	if len(list.Repositories) != 2 || list.Repositories[0].Repository != gadgets || list.Repositories[1].Repository != repo {
		t.Fatalf("repositories = %+v", list)
	}
	for _, name := range []string{"acme/off", "acme/unknown", ""} {
		for _, route := range []string{"features", "decisions"} {
			if code := f.get("/api/timeline/"+route, url.Values{"repository": {repo, name}}, nil); code != http.StatusBadRequest {
				t.Fatalf("%s of %q = %d", route, name, code)
			}
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
	var page featurePage
	f.get("/api/timeline/features", nil, &page)
	if page.ids() != "G1" {
		t.Fatalf("features of the repositories on = %s", page.ids())
	}
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
	if len(one.People) != 2 || one.People[0].Email != "me@example.com" || *one.People[0].Name != "Ada" || *one.People[1].Login != "octo" {
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

// Every list reads the repositories asked for, or every one served, merged
// in one order; every row and detail names its repository.
func TestTimelineAcrossRepositories(t *testing.T) {
	f := newTimelineFixture(t)
	var page featurePage
	f.get("/api/timeline/features", nil, &page)
	if page.ids() != "F1,F2,G1" {
		t.Fatalf("features = %s", page.ids())
	}
	for i, want := range []string{repo, repo, gadgets} {
		if r := page.Features[i].Repository; r == nil || r.FullName != want || r.ID != want {
			t.Fatalf("feature %d repository = %+v", i, r)
		}
	}
	page = featurePage{}
	f.get("/api/timeline/features", url.Values{"repository": {"ACME/gadgets", gadgets}}, &page)
	if page.ids() != "G1" {
		t.Fatalf("features of gadgets = %s", page.ids())
	}
	// Pages follow on across the repositories.
	var seen []string
	cursor := ""
	for range 5 {
		q := url.Values{"repository": {repo, gadgets}, "limit": {"2"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		page = featurePage{}
		f.get("/api/timeline/features", q, &page)
		seen = append(seen, page.ids())
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if strings.Join(seen, "|") != "F1,F2|G1" {
		t.Fatalf("pages = %v", seen)
	}
	var decisions struct {
		Decisions []timelineDecision `json:"decisions"`
		Coverage  timelineCoverage   `json:"coverage"`
	}
	f.get("/api/timeline/decisions", nil, &decisions)
	if len(decisions.Decisions) != 3 || decisions.Coverage.Extracted != 3 {
		t.Fatalf("decisions = %+v", decisions)
	}
	for _, d := range decisions.Decisions {
		if want := map[bool]string{true: gadgets, false: repo}[d.ID == "d-g1"]; d.Repository == nil || d.Repository.FullName != want {
			t.Fatalf("%s repository = %+v", d.ID, d.Repository)
		}
	}
	var detail struct {
		Repository *timelineRepository `json:"repository"`
		Feature    timelineFeature     `json:"feature"`
	}
	f.get("/api/timeline/features/G1", nil, &detail)
	if detail.Repository == nil || detail.Repository.FullName != gadgets || detail.Feature.Repository.FullName != gadgets {
		t.Fatalf("G1 = %+v", detail)
	}
	detail.Repository = nil
	f.get("/api/timeline/decisions/d-g1", nil, &detail)
	if detail.Repository == nil || detail.Repository.FullName != gadgets {
		t.Fatalf("d-g1 = %+v", detail)
	}
}

// The user is one person: the first clone's address and name, every clone's
// address and their login's GitHub address naming them too, with the pull
// requests they opened folded in once their login is known.
func TestTimelinePeople(t *testing.T) {
	f := newTimelineFixture(t)
	type counted struct {
		Email         string  `json:"email"`
		Name          *string `json:"name"`
		Login         *string `json:"login"`
		DecisionCount int     `json:"decision_count"`
		FeatureCount  int     `json:"feature_count"`
	}
	var people struct{ People []counted }
	f.get("/api/timeline/people", nil, &people)
	if len(people.People) != 1 || people.People[0].Email != "me@example.com" || *people.People[0].Name != "Ada" || people.People[0].DecisionCount != 4 {
		t.Fatalf("people = %+v", people)
	}
	f.get("/api/timeline/features/people", nil, &people)
	if len(people.People) != 2 || people.People[0].Email != "me@example.com" || people.People[0].FeatureCount != 3 ||
		people.People[1].Email != "octo@users.noreply.github.com" || people.People[1].FeatureCount != 1 {
		t.Fatalf("feature people = %+v", people)
	}
	decisionIDs := func(person string) int {
		var page struct{ Decisions []timelineDecision }
		f.get("/api/timeline/decisions", url.Values{"person": {person}}, &page)
		return len(page.Decisions)
	}
	featureIDs := func(person string) string {
		var page featurePage
		f.get("/api/timeline/features", url.Values{"person": {person}}, &page)
		return page.ids()
	}
	if decisionIDs("ME@example.com") != 3 || decisionIDs("ada@widgets.example") != 3 || decisionIDs("octo@users.noreply.github.com") != 0 {
		t.Fatal("the decisions of a person")
	}
	if got := featureIDs("ada@widgets.example"); got != "F1,F2,G1" {
		t.Fatalf("the user's features = %s", got)
	}
	if got := featureIDs("octo@users.noreply.github.com"); got != "F1" {
		t.Fatalf("octo's features = %s", got)
	}
	// Once their login is known, octo is the user.
	f.timeline.Login = func(context.Context) string { return "octo" }
	f.timeline.selfAt = time.Time{}
	f.get("/api/timeline/features/people", nil, &people)
	if len(people.People) != 1 || people.People[0].Email != "me@example.com" || *people.People[0].Login != "octo" || people.People[0].FeatureCount != 3 {
		t.Fatalf("feature people with the login = %+v", people)
	}
	if decisionIDs("octo@users.noreply.github.com") != 3 {
		t.Fatal("the login's address does not name the user")
	}
	// A feature with only their pull requests is theirs.
	f.exec(`DELETE FROM session_commit`)
	if got := featureIDs("octo@users.noreply.github.com"); got != "F1,F2,G1" {
		t.Fatalf("features by the login = %s", got)
	}
}

// The cited files and their folders, and the filters both lists share.
func TestTimelineFilesAndFilters(t *testing.T) {
	f := newTimelineFixture(t)
	if err := f.store.Save(context.Background(), Extraction{Meta: Meta{Agent: "codex", ID: "g2", Repository: gadgets}, ContentHash: "c-g2",
		Decisions: []decisions.Decision{
			decision("d-g2", "Split the tool.", 1),
			{ID: "d-g3", Summary: "Name the binary.", Category: decisions.CategoryScope, Who: decisions.WhoHuman, Turns: []int{2},
				Files: []string{"cmd/gadget/main.go", "cmd/tool.go"}},
		}}); err != nil {
		t.Fatal(err)
	}
	type file struct {
		Path          string
		Kind          string
		DecisionCount int `json:"decision_count"`
	}
	list := func(query url.Values) string {
		var answer struct{ Files []file }
		if code := f.get("/api/timeline/files", query, &answer); code != http.StatusOK {
			t.Fatalf("files %v = %d", query, code)
		}
		var out []string
		for _, each := range answer.Files {
			out = append(out, fmt.Sprintf("%s %s %d", each.Kind, each.Path, each.DecisionCount))
		}
		return strings.Join(out, ", ")
	}
	if got := list(url.Values{"prefix": {"cmd/"}}); got != "folder cmd/ 1, folder cmd/gadget/ 1, file cmd/gadget/main.go 1, file cmd/tool.go 1" {
		t.Fatalf("cmd/ = %s", got)
	}
	if got := list(url.Values{"prefix": {"c"}, "limit": {"2"}}); got != "folder cmd/ 1, folder cmd/gadget/ 1" {
		t.Fatalf("c = %s", got)
	}
	if got := list(url.Values{"prefix": {"a"}, "repository": {gadgets}}); got != "file a.go 1" {
		t.Fatalf("a in gadgets = %s", got)
	}
	if got := list(url.Values{"prefix": {"a"}, "repository": {repo}}); got != "" {
		t.Fatalf("a in widgets = %s", got)
	}
	for _, bad := range []url.Values{{"limit": {"51"}}, {"repository": {"acme/off"}}} {
		if code := f.get("/api/timeline/files", bad, nil); code != http.StatusBadRequest {
			t.Fatalf("files %v = %d", bad, code)
		}
	}
	features := func(query url.Values) string {
		var page featurePage
		if code := f.get("/api/timeline/features", query, &page); code != http.StatusOK {
			t.Fatalf("features %v = %d", query, code)
		}
		return page.ids()
	}
	// A pull request's path, or a decision's file, or anything under it.
	for query, want := range map[string]string{"file=export.go": "F1", "file=button.go": "F1", "file=count.go": "G1", "file=cmd": "",
		"from=2026-01-01": "F1,F2,G1", "from=2027-01-01": "", "to=2026-01-01T00:00:00Z": "", "status=shipped&status=abandoned": "", "status=shipped&status=in_progress": "F1,F2,G1"} {
		q, _ := url.ParseQuery(query)
		if got := features(q); got != want {
			t.Errorf("features %s = %q, want %q", query, got, want)
		}
	}
	decisionCount := func(query url.Values) int {
		var page struct{ Decisions []timelineDecision }
		if code := f.get("/api/timeline/decisions", query, &page); code != http.StatusOK {
			t.Fatalf("decisions %v = %d", query, code)
		}
		return len(page.Decisions)
	}
	later := t0.Add(time.Second).Format(time.RFC3339)
	if n := decisionCount(url.Values{"from": {later}}); n != 0 {
		t.Fatalf("from %s = %d", later, n)
	}
	if n := decisionCount(url.Values{"to": {later}}); n != 5 {
		t.Fatalf("to %s = %d", later, n)
	}
	for _, bad := range []string{"from=yesterday", "to=2026-13-01"} {
		q, _ := url.ParseQuery(bad)
		for _, route := range []string{"decisions", "features"} {
			if code := f.get("/api/timeline/"+route, q, nil); code != http.StatusBadRequest {
				t.Fatalf("%s %s = %d", route, bad, code)
			}
		}
	}
}

// A decision's detail has the prompts it cites from its session's transcript
// on this computer, stripped and redacted as capture reads them, until the
// transcript is gone.
func TestTimelineCitedPrompts(t *testing.T) {
	f := newTimelineFixture(t)
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.timeline.Home = home
	path := filepath.Join(home, ".claude", "projects", "-work-demo", "p1.jsonl")
	write(t, path, claudeTurn("p1", 1, "Export widgets as CSV and mail "+plantedMail+" when done.", "Done.")+
		claudeTurn("p1", 2, "<local-command-stdout>Set model to opus</local-command-stdout>", "Ok.")+
		claudeTurn("p1", 3, "Keep the header row.", "Done."))
	if err := f.store.Save(context.Background(), Extraction{Meta: Meta{Agent: "claude-code", ID: "p1", Repository: repo, Path: path}, ContentHash: "c-p1",
		Decisions: []decisions.Decision{decision("d-p1", "Export as CSV.", 1, 2), decision("d-p2", "Keep headers.", 3)}}); err != nil {
		t.Fatal(err)
	}
	type detail struct {
		PromptsWithheld bool           `json:"prompts_withheld"`
		Turns           []timelineTurn `json:"turns"`
	}
	var got detail
	f.get("/api/timeline/decisions/d-p1", nil, &got)
	if got.PromptsWithheld || len(got.Turns) != 2 || got.Turns[0].Ordinal != 1 || got.Turns[1].Ordinal != 3 {
		t.Fatalf("turns = %+v", got)
	}
	first := got.Turns[0]
	if strings.Contains(first.Text, plantedMail) || !strings.HasPrefix(first.Text, "Export widgets as CSV and mail ") ||
		first.TriggeredAt == nil || !first.TriggeredAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("turn 1 = %+v", first)
	}
	// The session opens though no tracker lists it.
	readAt := t0
	f.timeline.Transcripts = &livesessions.Transcripts{Home: home, Render: transcript.NewRenderer(home).Render, Now: func() time.Time { return readAt }}
	type answer struct {
		Available  bool            `json:"available"`
		Live       bool            `json:"live"`
		Transcript json.RawMessage `json:"transcript"`
		Message    string          `json:"message"`
	}
	var opened answer
	if code := f.get("/api/timeline/sessions/claude-code:p1/transcript", nil, &opened); code != http.StatusOK || !opened.Available || opened.Live ||
		!strings.Contains(string(opened.Transcript), "Keep the header row.") || strings.Contains(string(opened.Transcript), plantedMail) {
		t.Fatalf("transcript = %d %+v", code, opened)
	}
	for _, path := range []string{"/api/timeline/sessions/claude-code:nope/transcript", "/api/timeline/sessions/p1/transcript"} {
		if code := f.get(path, nil, nil); code != http.StatusNotFound {
			t.Fatalf("%s = %d", path, code)
		}
	}
	off := false
	f.registry.list[0].Settings.Decisions = &off
	if code := f.get("/api/timeline/sessions/claude-code:p1/transcript", nil, nil); code != http.StatusNotFound {
		t.Fatalf("a session of a repository turned off = %d", code)
	}
	f.registry.list[0].Settings.Decisions = nil
	// Over capture's cap: not read, as capture would not read it.
	if err := os.Truncate(path, MaxTranscriptBytes+1); err != nil {
		t.Fatal(err)
	}
	got = detail{}
	f.get("/api/timeline/decisions/d-p1", nil, &got)
	if !got.PromptsWithheld || len(got.Turns) != 0 {
		t.Fatalf("over the cap = %+v", got)
	}
	// Gone from this computer: the prompts are withheld.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	got = detail{}
	f.get("/api/timeline/decisions/d-p1", nil, &got)
	if !got.PromptsWithheld || len(got.Turns) != 0 {
		t.Fatalf("without the transcript = %+v", got)
	}
	opened = answer{}
	readAt = readAt.Add(time.Minute)
	if f.get("/api/timeline/sessions/claude-code:p1/transcript", nil, &opened); opened.Available || opened.Message == "" {
		t.Fatalf("transcript without its file = %+v", opened)
	}
}

// A decision a later capture replaced still opens, with the decisions
// replaced with it.
func TestTimelineReplacedDecision(t *testing.T) {
	f := newTimelineFixture(t)
	f.clock.now = t0.Add(time.Hour)
	if err := f.store.Save(context.Background(), Extraction{Meta: Meta{Agent: "claude-code", ID: "s1", Repository: repo}, ContentHash: "c-s1-again",
		Decisions: []decisions.Decision{decision("d-s1-new", "Export as spreadsheets.", 1)}}); err != nil {
		t.Fatal(err)
	}
	var detail struct {
		Replaced  bool `json:"replaced"`
		Decisions []struct {
			ID    string
			Files []string
		}
	}
	f.get("/api/timeline/decisions/d-s1", nil, &detail)
	if !detail.Replaced || len(detail.Decisions) != 1 || detail.Decisions[0].ID != "d-s1" || len(detail.Decisions[0].Files) != 1 {
		t.Fatalf("replaced = %+v", detail)
	}
	detail.Replaced = false
	f.get("/api/timeline/decisions/d-s1-new", nil, &detail)
	if detail.Replaced || len(detail.Decisions) != 1 || detail.Decisions[0].ID != "d-s1-new" {
		t.Fatalf("current = %+v", detail)
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
	handler := (&Timeline{Store: s, Registry: timelineRegistry{prefs: repos.DefaultPreferences(), list: []repos.Repository{{Repository: repo}}}}).Handler()
	match := func(q string) []string {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/timeline/decisions?"+url.Values{"q": {q}}.Encode(), nil))
		var page struct{ Decisions []timelineDecision }
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(w.Code, err)
		}
		var ids []string
		for _, d := range page.Decisions {
			ids = append(ids, d.ID)
		}
		return ids
	}
	if got := match("export spreadsheet"); len(got) != 1 || got[0] != "d1" {
		t.Fatalf("export spreadsheet = %v", got)
	}
	// A new extraction makes d1 obsolete: kept for good through a prune
	// (the integrity check below holds the index to it), and only current
	// decisions are searched.
	save("s1", decision("d2", "Exporting widgets as CSV", 1))
	c.now = t0.Add(Retention + 48*time.Hour)
	if err := s.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if got := match("spreadsheet"); len(got) != 0 {
		t.Fatalf("an obsolete decision is listed: %v", got)
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

// A repository's first build in progress, and its features, which are not
// shown until it is current.
func TestTimelineBuildState(t *testing.T) {
	f := newTimelineFixture(t)
	var answer struct {
		Building     *timelineBuild `json:"building"`
		HasFeatures  bool           `json:"has_features"`
		CurrentSince *time.Time     `json:"current_since"`
	}
	f.get("/api/timeline/build", url.Values{"repository": {repo}}, &answer)
	if answer.Building != nil || !answer.HasFeatures || answer.CurrentSince == nil {
		t.Fatalf("build = %+v", answer)
	}
	f.exec(`INSERT INTO feature_version(repository, state, created_at, total, placed, stopped, spent_usd) VALUES (?, 'building', 0, 40, 12, 'backend_unavailable', 0.5)`, repo)
	f.exec(`INSERT INTO feature(id, version_id, repository, title, changed_at, created_at, updated_at)
VALUES ('B1', (SELECT id FROM feature_version WHERE state='building'), ?, 'Being built', 0, 0, 0)`, repo)
	f.get("/api/timeline/build", url.Values{"repository": {repo}}, &answer)
	if b := answer.Building; b == nil || b.Placed != 12 || b.Total != 40 || b.Stopped != "backend_unavailable" || b.SpentUSD != 0.5 {
		t.Fatalf("building = %+v", answer.Building)
	}
	if code := f.get("/api/timeline/features/B1", nil, nil); code != http.StatusNotFound {
		t.Fatalf("a feature being built = %d", code)
	}
	var page featurePage
	f.get("/api/timeline/features", url.Values{"repository": {repo}}, &page)
	if page.ids() != "F1,F2" {
		t.Fatalf("features while building = %s", page.ids())
	}
	if code := f.get("/api/timeline/build", url.Values{"repository": {"acme/off"}}, nil); code != http.StatusBadRequest {
		t.Fatalf("build of a repository turned off = %d", code)
	}
}

// A chain of merges too long to follow is not found.
func TestTimelineBrokenMergeChain(t *testing.T) {
	f := newTimelineFixture(t)
	f.exec(`UPDATE feature SET merged_into='F3' WHERE id='F1'`)
	if code := f.get("/api/timeline/features/F3", nil, nil); code != http.StatusNotFound {
		t.Fatalf("a merge cycle = %d", code)
	}
}

// current_since is when the current version became current: its switch,
// else its creation; null without one.
func TestTimelineCurrentSince(t *testing.T) {
	f := newTimelineFixture(t)
	since := func() *time.Time {
		t.Helper()
		var answer struct {
			CurrentSince *time.Time `json:"current_since"`
		}
		f.get("/api/timeline/build", url.Values{"repository": {repo}}, &answer)
		return answer.CurrentSince
	}
	created, switched := t0.Add(-time.Hour), t0.Add(time.Hour)
	f.exec(`UPDATE feature_version SET created_at=?, switched_at=? WHERE state='current'`, created.UnixMilli(), switched.UnixMilli())
	if got := since(); got == nil || !got.Equal(switched) {
		t.Fatalf("switched = %v", got)
	}
	f.exec(`UPDATE feature_version SET switched_at=NULL WHERE state='current'`)
	if got := since(); got == nil || !got.Equal(created) {
		t.Fatalf("created = %v", got)
	}
	f.exec(`UPDATE feature_version SET state='retired'`)
	if got := since(); got != nil {
		t.Fatalf("without a current version = %v", got)
	}
}
