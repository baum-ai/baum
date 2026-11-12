package agentcontext

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

// readDaemon answers the routes the Inbox, Pull requests, Sessions and
// Timeline tools read, with the shapes the daemon's handlers answer.
type readDaemon struct {
	mu   sync.Mutex
	seen []string
}

func (d *readDaemon) requests() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.seen)
}

func (d *readDaemon) saw(substring string) bool {
	return slices.ContainsFunc(d.requests(), func(r string) bool { return strings.Contains(r, substring) })
}

const (
	repoA = `{"id":"acme/web","full_name":"acme/web"}`
	repoB = `{"id":"other/api","full_name":"other/api"}`
)

func (d *readDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	d.seen = append(d.seen, r.Method+" "+r.URL.RequestURI())
	d.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	q := r.URL.Query()
	pr := func(number int, title, extra string) string {
		return fmt.Sprintf(`{"pull_request":{"repository":"acme/web","number":%d,"title":%q,"author":"dana","state":"OPEN"%s},"clone_id":"c-1","status":"none","version_count":0,"review":"","merge":"","checks":"","unresolved":null,"review_minutes":null,"historical":false,"risk_score":null}`, number, title, extra)
	}
	switch r.URL.Path {
	case "/api/index":
		entries := []string{
			pr(1, "Review me", `,"review_requested_from_me":true`),
			strings.Replace(pr(1, "Review me", `,"review_requested_from_me":true`), "c-1", "c-2", 1),
			strings.Replace(pr(2, "Mine", `,"authored_by_me":true`), `"risk_score":null`, `"risk_score":{"score":4,"status":"ready"}`, 1),
			pr(3, "Theirs", ""),
			pr(4, "Snoozed", `,"review_requested_from_me":true`),
			pr(5, "Waiting", `,"authored_by_me":true`),
			strings.Replace(pr(9, "Old", `,"state":"MERGED"`), `"historical":false`, `"historical":true`, 1),
		}
		if q.Get("history") != "1" {
			entries = entries[:6]
		}
		_, _ = fmt.Fprintf(w, `{"entries":[%s],"problems":[]}`, strings.Join(entries, ","))
	case "/api/inbox":
		if q.Has("query") {
			if q.Get("query") == "(" {
				_, _ = fmt.Fprint(w, `{"query":"(","problem":"Unclosed (","matches":[]}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"query":`+jsonString(q.Get("query"))+`,"problem":"","matches":["acme/web#1","acme/web#3","acme/web#4","acme/web#5","acme/web#2"]}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"count":2,"items":[`+
			`{"key":"acme/web#1","kind":"review","inboxes":["i1"]},`+
			`{"key":"acme/web#2","kind":"fix","inboxes":[]},`+
			`{"key":"acme/web#3","kind":"other","inboxes":["i1"]},`+
			`{"key":"acme/web#4","kind":"review","snoozed":true,"inboxes":["i1"]},`+
			`{"key":"acme/web#5","kind":"","inboxes":[]}],`+
			`"inboxes":[{"id":"i1","query":"waiting=review","count":2},{"id":"i2","query":"author=@me","count":0}]}`)
	case "/api/inboxes":
		_, _ = fmt.Fprint(w, `{"available":true,"revision":4,"inboxes":[`+
			`{"id":"i1","name":"Reviews","color":"blue","what":"review","owners":[],"risk":"any","repositories":["acme/web"],"query":"","no_bots":false,"badge":true},`+
			`{"id":"i2","name":"Mine","color":"green","what":"mine","owners":[],"risk":"any","repositories":[],"query":"","no_bots":true,"badge":false}]}`)
	case "/api/ui-state/filters":
		_, _ = fmt.Fprint(w, `{"revision":3,"value":{"pages":{"pulls":"author=dana","tab:t1":"title"},"tabs":[{"id":"t1","name":"Closed ones","view":"history","query":"old"}]}}`)
	case "/api/live-sessions":
		_, _ = fmt.Fprint(w, `{"sessions":[`+
			`{"agent":"claude-code","session_id":"s-1","state":"idle","started_at":"2026-11-12T09:00:00Z","last_activity_at":"2026-11-12T10:00:00Z","waiting_reason":"permission","waiting_since":"2026-11-12T10:00:00Z","waiting_message":"Claude needs your permission to use Bash","repository":"acme/web","branch":"fix","pr":7,"pr_state":"OPEN","head_sha":"deadbeef","upstream":"origin/fix","title":"Fix the thing","activity":{"steps":[{"tool":"Bash","target":"go test"}],"last_message":"Done."},"endable":true},`+
			`{"agent":"codex","session_id":"s-2","state":"running","started_at":"2026-11-12T08:00:00Z","last_activity_at":"2026-11-12T09:30:00Z","repository":"other/api","branch":"main","title":"Other repository"}]}`)
	case "/api/live-sessions/claude-code/s-1/transcript":
		_, _ = fmt.Fprint(w, `{"available":true,"version":"v3","live":true,"summary":"Earlier work","images":{"t1":["image/png"]},"transcript":{"activity":{"t1":{"messages":[{"text":"One"}],"tool_calls":[{"name":"Bash","input":"go test"}]},"t2":{"messages":[{"text":"Two"}],"tool_calls":[{"name":"Bash","input":"go vet"}]},"t3":{"messages":[{"text":"Three"}],"tool_calls":[]}},"session":{"title":"Fix the thing","working_dir":"/home/dev/web","user_email":"dana@example.com","turn_count":3,"prompts":[`+
			`{"turn_id":"t1","ordinal":1,"text":"First","images":[{"src":"/api/live-sessions/claude-code/s-1/transcript/images/t1/0","media_type":"image/png"}],"replies":[{"text":"One"}]},`+
			`{"turn_id":"t2","ordinal":2,"text":"Second","replies":[]},`+
			`{"turn_id":"t3","ordinal":3,"text":"Third","replies":[]}],"prompts_total":3}}}`)
	case "/api/live-sessions/claude-code/gone/transcript":
		http.NotFound(w, r)
	case "/api/live-sessions/claude-code/off/transcript":
		_, _ = fmt.Fprint(w, `{"available":false,"live":false,"code":"transcript_unavailable","message":"The transcript is gone."}`)
	case "/api/agent-requests":
		_, _ = fmt.Fprint(w, `{"requests":[{"id":"r1","agent":"claude-code","session_id":"s-1","kind":"permission","tool":"Bash","created_at":"2026-11-12T10:00:00Z","input":[{"label":"Command","value":"go test"}],"suggestions":[],"questions":[]}],`+
			`"messages":[{"agent":"claude-code","session_id":"s-1","text":"Also run vet","queued_at":"2026-11-12T10:01:00Z"}],"settings":{"codex_approvals":true}}`)
	case "/api/timeline/decisions":
		_, _ = fmt.Fprint(w, `{"decisions":[{"id":"d1","session_id":"claude-code:s-1","turn":2,"who":"human","category":"architecture","summary":"Keep it local","superseded":false,"file_count":1,"repository":`+repoA+`,"person":{"email":"dana@example.com","name":"Dana","login":"dana","avatar_url":"https://avatars.example/dana"}}],"next_cursor":"1|d1","order":"newest","coverage":{"skipped_by_reason":{}},"features_enabled":true}`)
	case "/api/timeline/decisions/d1":
		_, _ = fmt.Fprint(w, `{"repository":`+repoA+`,"session":{"id":"claude-code:s-1","person":{"email":"dana@example.com","name":"Dana"}},"decisions":[{"id":"d1","summary":"Keep it local","files":["a.go"]}],"replaced":false,"turns":[],"prompts_withheld":false,"pull_requests":[]}`)
	case "/api/timeline/decisions/d2":
		_, _ = fmt.Fprint(w, `{"repository":`+repoB+`,"decisions":[{"id":"d2","summary":"Another repository"}]}`)
	case "/api/timeline/features":
		_, _ = fmt.Fprint(w, `{"features":[{"id":"f1","title":"Local search","status":"in_progress","repository":`+repoA+`,"people":[{"email":"dana@example.com","name":"Dana","avatar_url":"x"}],"pull_request_count":2,"session_count":1,"decision_count":3,"key_decisions":[]}],"next_cursor":null,"order":"newest"}`)
	case "/api/timeline/features/f1":
		_, _ = fmt.Fprint(w, `{"repository":`+repoA+`,"id":"f1","title":"Local search","digest":"It searches.","pull_requests":[],"sessions":[]}`)
	case "/api/timeline/features/f2":
		_, _ = fmt.Fprint(w, `{"repository":`+repoB+`,"id":"f2","title":"Another repository's"}`)
	case "/api/timeline/features/archived":
		_, _ = fmt.Fprint(w, `{"archived":true}`)
	case "/api/timeline/build":
		_, _ = fmt.Fprint(w, `{"building":{"placed":2,"total":9,"stopped":"","spent_usd":0.1},"has_features":false,"current_since":null}`)
	case "/api/timeline/files":
		_, _ = fmt.Fprint(w, `{"files":[{"path":"a.go","kind":"file","decision_count":2}]}`)
	case "/api/timeline/sessions/claude-code:s-1/transcript":
		_, _ = fmt.Fprint(w, `{"available":true,"version":"v1","live":false,"transcript":{"activity":{"t1":{"tool_calls":[{"name":"Bash","input":"first"}]},"t2":{"tool_calls":[{"name":"Bash","input":"second"}]}},"session":{"title":"Old","working_dir":"/home/dev/web","prompts":[{"turn_id":"t1","ordinal":1,"text":"Ask"},{"turn_id":"t2","ordinal":2,"text":"Again"}]}}}`)
	default:
		http.NotFound(w, r)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func readServer(t *testing.T) (*Server, *readDaemon, string, string) {
	t.Helper()
	d := &readDaemon{}
	s := New(Options{API: d, Version: "1"})
	token, id := chat(t, s)
	return s, d, token, id
}

func setPage(t *testing.T, s *Server, id string, p PageContext) {
	t.Helper()
	if err := s.Registry.SetPage(id, p); err != nil {
		t.Fatal(err)
	}
}

// out decodes a tool's data into v.
func out(t *testing.T, s *Server, token, tool string, args any, v any) string {
	t.Helper()
	text, failed := data(t, s, token, tool, args)
	if failed {
		t.Fatalf("%s %v: %s", tool, args, text)
	}
	// Each call decodes into a clean value.
	reflect.ValueOf(v).Elem().SetZero()
	if err := json.Unmarshal([]byte(text), v); err != nil {
		t.Fatalf("%s: %v %s", tool, err, text)
	}
	return text
}

func numbers(rows []inboxRow) []int {
	var out []int
	for _, r := range rows {
		out = append(out, r.Number)
	}
	return out
}

func TestTheNewPagesListTheirTools(t *testing.T) {
	s, _, token, id := readServer(t)
	want := map[string][]string{
		PageInbox:    {"inbox", "inboxes", "test_query"},
		PagePulls:    {"pull_requests"},
		PageSessions: {"live_sessions", "live_session_transcript", "agent_requests", "agent_messages"},
		PageSession:  {"live_sessions", "live_session_transcript", "agent_requests", "agent_messages"},
		PageTimeline: {"decisions", "decision", "features", "feature", "feature_build", "file_history", "timeline_transcript"},
		PageFeature:  {"decisions", "decision", "features", "feature", "feature_build", "file_history", "timeline_transcript"},
	}
	for page, tools := range want {
		setPage(t, s, id, PageContext{Page: page})
		var current struct {
			Tools []string `json:"tools"`
		}
		out(t, s, token, "current_page", nil, &current)
		for _, tool := range append(slices.Clone(tools), commonToolNames...) {
			if !slices.Contains(current.Tools, tool) {
				t.Errorf("page %s lacks %s: %v", page, tool, current.Tools)
			}
		}
		if len(current.Tools) != len(tools)+len(commonToolNames) {
			t.Errorf("page %s lists %v", page, current.Tools)
		}
	}
}

func TestInboxTool(t *testing.T) {
	s, d, token, id := readServer(t)
	var got struct {
		Waiting int        `json:"waiting"`
		Inbox   string     `json:"inbox"`
		Total   int        `json:"total"`
		More    int        `json:"more"`
		Items   []inboxRow `json:"items"`
	}
	// The whole queue: what waits on the person, not the snoozed, others' or
	// those waiting on others.
	out(t, s, token, "inbox", nil, &got)
	if got.Waiting != 2 || !slices.Equal(numbers(got.Items), []int{1, 2}) || got.Total != 2 {
		t.Fatalf("queue: %+v", got)
	}
	if first := got.Items[0]; first.Kind != "review" || first.Title != "Review me" || first.Inboxes[0] != "i1" || first.Repository != "acme/web" {
		t.Errorf("row: %+v", first)
	}
	if second := got.Items[1]; second.Kind != "fix" || string(second.Risk) != `{"score":4,"status":"ready"}` {
		t.Errorf("risk: %+v", second)
	}
	// An inbox lists what the queue holds and others' pull requests.
	out(t, s, token, "inbox", map[string]any{"inbox": "i1"}, &got)
	if !slices.Equal(numbers(got.Items), []int{1, 3}) || got.Items[1].Kind != "other" {
		t.Errorf("inbox i1: %+v", got)
	}
	out(t, s, token, "inbox", map[string]any{"inbox": "i1", "hidden": true}, &got)
	if !slices.Equal(numbers(got.Items), []int{1, 3, 4}) || !got.Items[2].Snoozed {
		t.Errorf("hidden: %+v", got)
	}
	// Kinds outside the queue are listed when asked for.
	out(t, s, token, "inbox", map[string]any{"kind": "waiting"}, &got)
	if !slices.Equal(numbers(got.Items), []int{5}) || got.Items[0].Kind != "waiting" {
		t.Errorf("waiting: %+v", got)
	}
	out(t, s, token, "inbox", map[string]any{"kind": "other"}, &got)
	if !slices.Equal(numbers(got.Items), []int{3}) {
		t.Errorf("other without an inbox: %+v", got)
	}
	out(t, s, token, "inbox", map[string]any{"inbox": "i1", "kind": "other"}, &got)
	if !slices.Equal(numbers(got.Items), []int{3}) {
		t.Errorf("kind: %+v", got)
	}
	out(t, s, token, "inbox", map[string]any{"limit": 1}, &got)
	if len(got.Items) != 1 || got.More != 1 || got.Total != 2 {
		t.Errorf("limit: %+v", got)
	}
	// An extra filter query keeps what the daemon says it holds.
	out(t, s, token, "inbox", map[string]any{"query": "waiting=review"}, &got)
	if !slices.Equal(numbers(got.Items), []int{1, 2}) || !d.saw("/api/inbox?query=waiting%3Dreview") {
		t.Errorf("query: %+v", got)
	}
	// The page's selected inbox is the default.
	setPage(t, s, id, PageContext{Page: PageInbox, Filters: map[string]string{"inbox": "i1"}})
	out(t, s, token, "inbox", nil, &got)
	if got.Inbox != "i1" || !slices.Equal(numbers(got.Items), []int{1, 3}) {
		t.Errorf("the page's inbox: %+v", got)
	}
	// On another page its filters are not the inbox's.
	setPage(t, s, id, PageContext{Page: PagePulls, Filters: map[string]string{"inbox": "i1"}})
	out(t, s, token, "inbox", nil, &got)
	if got.Inbox != "" {
		t.Errorf("another page's filter: %+v", got)
	}
	if text, failed := data(t, s, token, "inbox", map[string]any{"inbox": "nope"}); !failed || !strings.Contains(text, "no inbox") {
		t.Errorf("unknown inbox: %v %s", failed, text)
	}
	if text, failed := data(t, s, token, "inbox", map[string]any{"kind": "nap"}); !failed || !strings.Contains(text, "input schema") {
		t.Errorf("unknown kind: %v %s", failed, text)
	}
}

func TestInboxesTool(t *testing.T) {
	s, _, token, _ := readServer(t)
	var got struct {
		Revision int `json:"revision"`
		Inboxes  []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Query  string `json:"query"`
			Filter string `json:"filter"`
			Count  *int   `json:"count"`
			NoBots bool   `json:"no_bots"`
			Badge  bool   `json:"badge"`
		} `json:"inboxes"`
	}
	out(t, s, token, "inboxes", nil, &got)
	if got.Revision != 4 || len(got.Inboxes) != 2 || got.Inboxes[0].Name != "Reviews" || got.Inboxes[0].Count == nil || *got.Inboxes[0].Count != 2 ||
		got.Inboxes[0].Filter != "waiting=review" || !got.Inboxes[1].NoBots || got.Inboxes[1].Badge {
		t.Errorf("%+v", got)
	}
}

func TestTestQueryTool(t *testing.T) {
	s, d, token, _ := readServer(t)
	var got struct {
		Valid   bool       `json:"valid"`
		Problem string     `json:"problem"`
		Total   int        `json:"total"`
		More    int        `json:"more"`
		Matches []inboxRow `json:"matches"`
	}
	out(t, s, token, "test_query", map[string]any{"query": "waiting=review"}, &got)
	// The daemon also matches the snoozed, waiting and being-fixed items; the
	// preview counts the listed ones, as a saved inbox would.
	if !got.Valid || got.Total != 3 || !slices.Equal(numbers(got.Matches), []int{1, 2, 3}) || got.Matches[0].Kind != "review" || got.Matches[2].Kind != "other" {
		t.Errorf("matches: %+v", got)
	}
	// A query that does not read is told why, and holds nothing.
	before := len(d.requests())
	out(t, s, token, "test_query", map[string]any{"query": "("}, &got)
	if got.Valid || got.Problem == "" || len(got.Matches) != 0 || len(d.requests()) != before {
		t.Errorf("invalid: %+v (%d requests)", got, len(d.requests())-before)
	}
	if text, failed := data(t, s, token, "test_query", map[string]any{}); !failed || !strings.Contains(text, "input schema") {
		t.Errorf("no query: %v %s", failed, text)
	}
}

func TestPullRequestsTool(t *testing.T) {
	s, d, token, id := readServer(t)
	var got struct {
		View         string    `json:"view"`
		Tab          string    `json:"tab"`
		Query        string    `json:"query"`
		Total        int       `json:"total"`
		More         int       `json:"more"`
		PullRequests []pullRow `json:"pull_requests"`
	}
	titles := func() string {
		var titles []string
		for _, p := range got.PullRequests {
			titles = append(titles, p.Title)
		}
		return strings.Join(titles, ",")
	}
	// The open ones, each pull request once however many clones hold it.
	out(t, s, token, "pull_requests", nil, &got)
	if got.View != "open" || got.Total != 5 || titles() != "Review me,Mine,Theirs,Snoozed,Waiting" {
		t.Errorf("open: %+v %s", got, titles())
	}
	out(t, s, token, "pull_requests", map[string]any{"view": "closed"}, &got)
	if got.View != "closed" || titles() != "Old" || !got.PullRequests[0].Historical {
		t.Errorf("closed: %+v", got)
	}
	out(t, s, token, "pull_requests", map[string]any{"query": "author=dana mine", "limit": 1}, &got)
	if got.Query != "author=dana mine" || got.Total != 1 || titles() != "Mine" {
		t.Errorf("query: %+v", got)
	}
	out(t, s, token, "pull_requests", map[string]any{"limit": 2}, &got)
	if got.More != 3 || len(got.PullRequests) != 2 {
		t.Errorf("limit: %+v", got)
	}
	// A saved tab is its view with its filter.
	out(t, s, token, "pull_requests", map[string]any{"tab": "t1"}, &got)
	if got.Tab != "t1" || got.View != "closed" || got.Query != "old" || titles() != "Old" {
		t.Errorf("tab: %+v", got)
	}
	// The page's own tab and filter apply when none is asked for.
	setPage(t, s, id, PageContext{Page: PagePulls})
	out(t, s, token, "pull_requests", nil, &got)
	if got.Query != "author=dana" || got.Total != 5 {
		t.Errorf("the page's filter: %+v", got)
	}
	setPage(t, s, id, PageContext{Page: PagePulls, Filters: map[string]string{"filter": "tab:t1"}})
	out(t, s, token, "pull_requests", nil, &got)
	if got.Tab != "t1" || got.Query != "title" || got.Total != 0 {
		t.Errorf("the page's tab draft: %+v", got)
	}
	// A tab that is gone opens Open.
	setPage(t, s, id, PageContext{Page: PagePulls, Filters: map[string]string{"filter": "tab:gone"}})
	out(t, s, token, "pull_requests", nil, &got)
	if got.View != "open" || got.Tab != "" {
		t.Errorf("a gone tab: %+v", got)
	}
	if text, failed := data(t, s, token, "pull_requests", map[string]any{"tab": "gone"}); !failed || !strings.Contains(text, "no saved tab") {
		t.Errorf("unknown tab: %v %s", failed, text)
	}
	if text, failed := data(t, s, token, "pull_requests", map[string]any{"query": "("}); !failed || !strings.Contains(text, "does not read") {
		t.Errorf("bad query: %v %s", failed, text)
	}
	// Only the index and the saved filters are read.
	for _, r := range d.requests() {
		if !strings.Contains(r, "/api/index") && !strings.Contains(r, "/api/ui-state/filters") && !strings.Contains(r, "/api/inbox") && !strings.Contains(r, "/api/inboxes") {
			t.Errorf("read %s", r)
		}
	}
}

func TestLiveSessionsTool(t *testing.T) {
	s, _, token, _ := readServer(t)
	var got struct {
		Total    int          `json:"total"`
		More     int          `json:"more"`
		Sessions []sessionRow `json:"sessions"`
	}
	text := out(t, s, token, "live_sessions", nil, &got)
	if got.Total != 2 || got.Sessions[0].SessionID != "s-1" || got.Sessions[0].WaitingFor != "permission" || got.Sessions[0].PullRequest != 7 ||
		got.Sessions[0].Branch != "fix" || got.Sessions[0].Repository != "acme/web" || !got.Sessions[0].Endable {
		t.Errorf("sessions: %+v", got)
	}
	if strings.Contains(text, "deadbeef") || strings.Contains(text, "origin/fix") {
		t.Errorf("a field the page does not show: %s", text)
	}
	out(t, s, token, "live_sessions", map[string]any{"repository": "OTHER/api"}, &got)
	if got.Total != 1 || got.Sessions[0].SessionID != "s-2" {
		t.Errorf("repository: %+v", got)
	}
	out(t, s, token, "live_sessions", map[string]any{"state": "running", "limit": 1}, &got)
	if got.Total != 1 || got.Sessions[0].State != "running" {
		t.Errorf("state: %+v", got)
	}
	// An action's sessions are its repository's, whatever it asks for.
	action, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 7})
	defer revoke()
	out(t, s, action, "live_sessions", nil, &got)
	if got.Total != 1 || got.Sessions[0].Repository != "acme/web" {
		t.Errorf("action: %+v", got)
	}
	if text, failed := data(t, s, action, "live_sessions", map[string]any{"repository": "other/api"}); !failed || strings.Contains(text, "s-2") || !strings.Contains(text, "own pull request's repository") {
		t.Errorf("action asking another repository: %v %s", failed, text)
	}
}

func TestSessionTranscriptTools(t *testing.T) {
	s, d, token, id := readServer(t)
	var got struct {
		Available  bool   `json:"available"`
		Version    string `json:"version"`
		Summary    string `json:"summary"`
		Images     any    `json:"images"`
		Transcript struct {
			Session map[string]json.RawMessage `json:"session"`
		} `json:"transcript"`
	}
	text := out(t, s, token, "live_session_transcript", map[string]any{"session": "claude-code:s-1", "limit": 2}, &got)
	if !got.Available || got.Summary != "Earlier work" || got.Images != nil {
		t.Errorf("answer: %+v", got)
	}
	for _, leak := range []string{"/home/dev", "dana@example.com", "image/png", `"src"`, "transcript/images", "working_dir"} {
		if strings.Contains(text, leak) {
			t.Errorf("transcript carries %q: %s", leak, text)
		}
	}
	var prompts []struct{ Ordinal int }
	_ = json.Unmarshal(got.Transcript.Session["prompts"], &prompts)
	if len(prompts) != 2 || string(got.Transcript.Session["next_turn"]) != "3" || string(got.Transcript.Session["prompts_total"]) != "3" {
		t.Errorf("paging: %s", text)
	}
	out(t, s, token, "live_session_transcript", map[string]any{"session": "claude-code:s-1", "from_turn": 3}, &got)
	_ = json.Unmarshal(got.Transcript.Session["prompts"], &prompts)
	if len(prompts) != 1 || prompts[0].Ordinal != 3 || got.Transcript.Session["next_turn"] != nil {
		t.Errorf("from_turn: %v %v", prompts, got.Transcript.Session)
	}
	// The page's session is the default.
	setPage(t, s, id, PageContext{Page: PageSession, Session: "claude-code:s-1"})
	if text, failed := data(t, s, token, "live_session_transcript", nil); failed || !strings.Contains(text, "First") {
		t.Errorf("the page's session: %v %s", failed, text)
	}
	// The agent's activity is paged with the prompts: only the kept ones'.
	text = out(t, s, token, "live_session_transcript", map[string]any{"session": "claude-code:s-1", "limit": 1}, &got)
	if !strings.Contains(text, "go test") || strings.Contains(text, "go vet") || strings.Contains(text, "Three") {
		t.Errorf("activity of a page: %s", text)
	}
	text = out(t, s, token, "timeline_transcript", map[string]any{"session": "claude-code:s-1", "limit": 1}, &got)
	if !strings.Contains(text, "first") || strings.Contains(text, "second") || !strings.Contains(text, `"next_turn":2`) {
		t.Errorf("timeline activity of a page: %s", text)
	}
	// No image route is ever read.
	if d.saw("/images/") {
		t.Errorf("read an image: %v", d.requests())
	}
	// Not available, not listed, and not a session.
	if text, failed := data(t, s, token, "live_session_transcript", map[string]any{"session": "claude-code:off"}); failed || !strings.Contains(text, `"available":false`) || !strings.Contains(text, "The transcript is gone.") {
		t.Errorf("unavailable: %v %s", failed, text)
	}
	for _, session := range []string{"claude-code:gone", "claude-code", "a/b:c", "claude-code:s-1/transcript/images/t1/0", ":x"} {
		if text, failed := data(t, s, token, "live_session_transcript", map[string]any{"session": session}); !failed {
			t.Errorf("session %q: %s", session, text)
		}
	}
	if d.saw("/images/") {
		t.Errorf("read an image: %v", d.requests())
	}
	// The Timeline's transcript of a captured session.
	text = out(t, s, token, "timeline_transcript", map[string]any{"session": "claude-code:s-1"}, &got)
	if strings.Contains(text, "/home/dev") || !strings.Contains(text, `"title":"Old"`) || !d.saw("/api/timeline/sessions/claude-code:s-1/transcript") {
		t.Errorf("timeline transcript: %s", text)
	}
}

func TestAgentRequestAndMessageTools(t *testing.T) {
	s, _, token, _ := readServer(t)
	text, failed := data(t, s, token, "agent_requests", nil)
	if failed || !strings.Contains(text, `"id":"r1"`) || !strings.Contains(text, `"value":"go test"`) || strings.Contains(text, "settings") || strings.Contains(text, "codex_approvals") || strings.Contains(text, "Also run vet") {
		t.Errorf("requests: %v %s", failed, text)
	}
	text, failed = data(t, s, token, "agent_messages", nil)
	if failed || !strings.Contains(text, "Also run vet") || strings.Contains(text, "settings") || strings.Contains(text, "codex_approvals") || strings.Contains(text, `"id":"r1"`) {
		t.Errorf("messages: %v %s", failed, text)
	}
}

func TestTimelineTools(t *testing.T) {
	s, d, token, id := readServer(t)
	last := func() string {
		r := d.requests()
		return r[len(r)-1]
	}
	query := func() url.Values {
		u, err := url.Parse(strings.TrimPrefix(last(), "GET "))
		if err != nil {
			t.Fatal(err)
		}
		return u.Query()
	}
	// The page's filters apply to what it lists.
	setPage(t, s, id, PageContext{Page: PageTimeline, Filters: map[string]string{"repo": "acme/web, other/api", "who": "human", "q": "local", "file": "a.go", "category": "architecture", "tab": "all"}})
	text, failed := data(t, s, token, "decisions", nil)
	if failed || !strings.Contains(text, `"summary":"Keep it local"`) || !strings.Contains(text, `"next_cursor":"1|d1"`) {
		t.Fatalf("decisions: %v %s", failed, text)
	}
	if strings.Contains(text, "dana@example.com") || strings.Contains(text, "avatar") || !strings.Contains(text, `"name":"Dana"`) {
		t.Errorf("a person: %s", text)
	}
	q := query()
	if !slices.Equal(q["repository"], []string{"acme/web", "other/api"}) || q.Get("who") != "human" || q.Get("q") != "local" || q.Get("file") != "a.go" || q.Get("category") != "architecture" || q.Get("limit") != "25" {
		t.Errorf("the page's filters: %v", q)
	}
	// Arguments take the page's place; the limit is bounded.
	data(t, s, token, "decisions", map[string]any{"repository": "acme/web", "who": []string{"agent", "human"}, "limit": 100, "cursor": "1|d1", "superseded": true, "from": "2026-11-01"})
	q = query()
	if !slices.Equal(q["repository"], []string{"acme/web"}) || q.Get("who") != "agent,human" || q.Get("limit") != "100" || q.Get("cursor") != "1|d1" || q.Get("superseded") != "1" || q.Get("from") != "2026-11-01" {
		t.Errorf("arguments: %v", q)
	}
	if text, failed := data(t, s, token, "decisions", map[string]any{"limit": 101}); !failed || !strings.Contains(text, "input schema") {
		t.Errorf("limit 101: %v %s", failed, text)
	}
	if text, failed := data(t, s, token, "decisions", map[string]any{"who": []string{"robot"}}); !failed || !strings.Contains(text, "input schema") {
		t.Errorf("who robot: %v %s", failed, text)
	}
	// Hiding the agents' own decisions, as the page's address does.
	setPage(t, s, id, PageContext{Page: PageTimeline, Filters: map[string]string{"agent": "hide"}})
	data(t, s, token, "decisions", nil)
	if query().Get("who") != "human,agent_proposed_human_approved" {
		t.Errorf("hidden agents: %v", query())
	}
	// A page with no repo filter reads every repository.
	if _, has := query()["repository"]; has {
		t.Errorf("repository: %v", query())
	}

	if text, failed := data(t, s, token, "decision", map[string]any{"id": "d1"}); failed || !strings.Contains(text, "Keep it local") || strings.Contains(text, "dana@example.com") {
		t.Errorf("decision: %v %s", failed, text)
	}
	if text, failed := data(t, s, token, "decision", map[string]any{"id": "../x"}); !failed {
		t.Errorf("a path as id: %s", text)
	}
	if text, failed := data(t, s, token, "decision", map[string]any{"id": "nope"}); !failed || !strings.Contains(text, "not found") {
		t.Errorf("a missing decision: %v %s", failed, text)
	}

	data(t, s, token, "features", map[string]any{"status": []string{"in_progress"}, "repository": "acme/web"})
	if q := query(); q.Get("status") != "in_progress" || q.Get("repository") != "acme/web" {
		t.Errorf("features: %v", q)
	}
	if text, _ := data(t, s, token, "features", nil); strings.Contains(text, "avatar") || !strings.Contains(text, `"title":"Local search"`) {
		t.Errorf("features: %s", text)
	}
	// A feature's page is the default feature.
	setPage(t, s, id, PageContext{Page: PageFeature, Repository: "acme/web", Feature: "f1"})
	if text, failed := data(t, s, token, "feature", nil); failed || !strings.Contains(text, "It searches.") {
		t.Errorf("feature: %v %s", failed, text)
	}
	// Its repository is the features' default.
	data(t, s, token, "features", nil)
	if got := query()["repository"]; !slices.Equal(got, []string{"acme/web"}) {
		t.Errorf("the feature page's repository: %v", got)
	}
	if text, failed := data(t, s, token, "feature", map[string]any{"id": ""}); failed || !strings.Contains(text, "It searches.") {
		t.Errorf("default feature: %v %s", failed, text)
	}

	text, failed = data(t, s, token, "feature_build", nil)
	if failed || !strings.Contains(text, `"placed":2`) || query().Get("repository") != "acme/web" {
		t.Errorf("build: %v %s %v", failed, text, query())
	}
	text, failed = data(t, s, token, "file_history", map[string]any{"prefix": "a", "limit": 50})
	if failed || !strings.Contains(text, `"path":"a.go"`) || query().Get("prefix") != "a" || query().Get("limit") != "50" {
		t.Errorf("files: %v %s %v", failed, text, query())
	}
	if text, failed := data(t, s, token, "file_history", map[string]any{"limit": 51}); !failed || !strings.Contains(text, "input schema") {
		t.Errorf("limit 51: %v %s", failed, text)
	}
	// Without a page and a repository, the build has none to read.
	setPage(t, s, id, PageContext{Page: PageTimeline})
	if text, failed := data(t, s, token, "feature_build", nil); !failed || !strings.Contains(text, "name a repository") {
		t.Errorf("build without a repository: %v %s", failed, text)
	}
}

// An action reads its own repository's Timeline and refuses another's: by the
// repository it asks for, and by what a detail's response says it belongs to.
func TestTimelineToolsOfAnAction(t *testing.T) {
	s, d, _, _ := readServer(t)
	action, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 7})
	defer revoke()
	refused := func(what, text string, failed bool) {
		t.Helper()
		if !failed || !strings.Contains(text, "own pull request's repository") || strings.Contains(text, "input schema") ||
			strings.Contains(text, "Another repository") || strings.Contains(text, "other/api") {
			t.Errorf("%s: %v %s", what, failed, text)
		}
	}
	// Asked for its own repository by default and by name.
	for _, tool := range []string{"decisions", "features", "file_history", "feature_build"} {
		if text, failed := data(t, s, action, tool, nil); failed {
			t.Errorf("%s: %s", tool, text)
		}
		if text, failed := data(t, s, action, tool, map[string]any{"repository": "ACME/web"}); failed {
			t.Errorf("%s of its repository by name: %s", tool, text)
		}
		text, failed := data(t, s, action, tool, map[string]any{"repository": "other/api"})
		refused(tool+" of another repository", text, failed)
	}
	for _, r := range d.requests() {
		if strings.Contains(r, "other/api") {
			t.Errorf("a read named another repository: %s", r)
		}
		if strings.HasPrefix(r, "GET /api/timeline/") && !strings.Contains(r, "repository=acme%2Fweb") && !strings.Contains(r, "/decisions/") && !strings.Contains(r, "/features/") {
			t.Errorf("a list read without the repository: %s", r)
		}
	}
	// A detail names its repository only in its response: its own is read,
	// another's is refused after it is read and nothing of it is returned.
	text, failed := data(t, s, action, "decision", map[string]any{"id": "d1"})
	if failed || !strings.Contains(text, "Keep it local") {
		t.Errorf("own decision: %v %s", failed, text)
	}
	text, failed = data(t, s, action, "decision", map[string]any{"id": "d2"})
	refused("another repository's decision", text, failed)
	text, failed = data(t, s, action, "feature", map[string]any{"id": "f1"})
	if failed || !strings.Contains(text, "It searches.") {
		t.Errorf("own feature: %v %s", failed, text)
	}
	text, failed = data(t, s, action, "feature", map[string]any{"id": "f2"})
	refused("another repository's feature", text, failed)
	// An archived feature names no repository: an action never sees it.
	text, failed = data(t, s, action, "feature", map[string]any{"id": "archived"})
	refused("an archived feature", text, failed)
	text, failed = data(t, s, action, "feature", map[string]any{"id": "missing"})
	if !failed {
		t.Errorf("a missing feature: %s", text)
	}
	// The global tools do not exist for an action.
	for _, tool := range []string{"inbox", "inboxes", "test_query", "pull_requests", "agent_requests", "agent_messages", "live_session_transcript", "timeline_transcript"} {
		r := rpc(t, s, action, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+tool+`","arguments":{}}}`)
		if r.Error == nil || !strings.Contains(r.Error.Message, "unknown tool") {
			t.Errorf("an action called %s: %+v", tool, r)
		}
	}
	listedTools := listed(t, s, action)
	for _, tool := range []string{"live_sessions", "decisions", "decision", "features", "feature", "feature_build", "file_history"} {
		if !listedTools[tool] {
			t.Errorf("an action does not list %s", tool)
		}
	}
}

// Each route an action may read is bound to its repository: the request
// without it, with another's, or with an extra parameter is refused before
// the route answers, and a response of another repository is refused or
// filtered after.
func TestActionRoutesOfTheReadTools(t *testing.T) {
	d := &readDaemon{}
	reader := NewReader(d, nil).within([]string{"/api/timeline", "/api/live-sessions"}, "acme/web")
	get := func(target string) (Response, error) { return reader.Get(context.Background(), target) }
	own := "repository=acme%2Fweb"
	for _, route := range []string{"decisions", "features", "files", "build"} {
		if _, err := get("/api/timeline/" + route + "?" + own); err != nil {
			t.Errorf("%s of its repository: %v", route, err)
		}
		for _, target := range []string{
			"/api/timeline/" + route,
			"/api/timeline/" + route + "?repository=other%2Fapi",
			"/api/timeline/" + route + "?repository=acme%2Fweb&repository=other%2Fapi",
			"/api/timeline/" + route + "?repository=acme%2Fweb%2Cother%2Fapi",
			"/api/timeline/" + route + "?" + own + "&unknown=1",
		} {
			if _, err := get(target); err == nil {
				t.Errorf("%s was read", target)
			}
		}
	}
	for _, target := range []string{"/api/timeline/decisions/d2", "/api/timeline/features/f2", "/api/timeline/features/archived", "/api/timeline/features/people", "/api/timeline/decisions/missing"} {
		if _, err := get(target); err == nil || !strings.Contains(err.Error(), "own pull request's repository") {
			t.Errorf("%s: %v", target, err)
		}
	}
	for _, target := range []string{"/api/timeline/decisions/d1", "/api/timeline/features/f1"} {
		if _, err := get(target); err != nil {
			t.Errorf("%s: %v", target, err)
		}
	}
	// What no action reads: a session's transcript and the people lists.
	for _, target := range []string{"/api/timeline/sessions/claude-code:s-1/transcript", "/api/timeline/people?" + own, "/api/timeline/repositories", "/api/live-sessions/claude-code/s-1/transcript"} {
		if _, err := get(target); err == nil {
			t.Errorf("%s was read", target)
		}
	}
	// The sessions are the repository's alone.
	response, err := get("/api/live-sessions")
	if err != nil || !strings.Contains(string(response.Body), "s-1") || strings.Contains(string(response.Body), "s-2") || strings.Contains(string(response.Body), "other/api") {
		t.Errorf("live sessions: %v %s", err, response.Body)
	}
	if _, err := get("/api/live-sessions?x=1"); err == nil {
		t.Error("a parameter of the sessions list was read")
	}
	// No reader reads a pasted image.
	free := NewReader(d, nil).within([]string{"/api/live-sessions"}, "")
	if _, err := free.Get(context.Background(), "/api/live-sessions/claude-code/s-1/transcript/images/t1/0"); err == nil {
		t.Error("an image was read")
	}
	if d.saw("/images/") {
		t.Errorf("an image request reached the daemon: %v", d.requests())
	}
}

func TestToolsReportAnUnavailableDaemon(t *testing.T) {
	off := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"available":false,"code":"unavailable"}`)
	})
	s := New(Options{API: off, Version: "1"})
	token, _ := chat(t, s)
	for tool, args := range map[string]any{"inbox": nil, "inboxes": nil, "test_query": map[string]any{"query": "x"}, "pull_requests": nil,
		"live_sessions": nil, "agent_requests": nil, "agent_messages": nil, "decisions": nil, "features": nil} {
		if text, failed := data(t, s, token, tool, args); !failed || !strings.Contains(text, "unavailable") {
			t.Errorf("%s: %v %s", tool, failed, text)
		}
	}
}
