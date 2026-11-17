package agentcontext

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// readsDaemon is fakeDaemon with the routes the review's reads add, for
// acme/web#13: a saved review at versions 2 and 3, a session transcript, its
// GitHub timeline and threads, an issue, and the clone's history.
type readsDaemon struct{ fakeDaemon }

const reviewIndex = `{"entries":[
 {"pull_request":{"repository":"acme/web","number":13,"head_sha":"h13","title":"Fix search","author":"ana","state":"OPEN","head_ref":"feature/search","base_ref":"main"},"clone_id":"c-web","version_count":3},
 {"pull_request":{"repository":"acme/web","number":12,"head_sha":"h12","title":"Search UI","author":"bo","state":"OPEN","head_ref":"feature/search-ui","base_ref":"feature/search"},"clone_id":"c-web","version_count":0},
 {"pull_request":{"repository":"acme/web","number":11,"head_sha":"h11","title":"Other","author":"cy","state":"OPEN","head_ref":"feature/other","base_ref":"main"},"clone_id":"c-web","version_count":1},
 {"pull_request":{"repository":"acme/web","number":10,"head_sha":"h10","title":"Unfetched","state":"OPEN","head_ref":"fix/missing","base_ref":"main"},"clone_id":"c-web","version_count":0},
 {"pull_request":{"repository":"acme/web","number":9,"head_sha":"h9","title":"Old fix","author":"di","state":"MERGED","head_ref":"fix/old","base_ref":"main"},"clone_id":"c-web","version_count":1,"historical":true},
 {"pull_request":{"repository":"evil/secrets","number":1,"head_sha":"hx","title":"Steal the keys"},"clone_id":"c-evil","version_count":0}],"problems":[]}`

var reviewVersions = map[string]string{
	"2": `{"schema":"baum.review/1","review":{"head":"h13a","base":"b13","merge_base":"m13","version":2},
	 "summary":{"recommendation":{"decision":"look_first","text":"x"},"risk_score":{"status":"scored","score":40}},
	 "sections":[{"id":"s-api","title":"API","kind":"change","hunks":["h1","h2"],"risks":[{"id":"r1","tier":"high","text":"Breaks clients","state":"open"}]},
	  {"id":"s-old","title":"Old","kind":"change","hunks":["h3"],"risks":[{"id":"r9","tier":"low","text":"Gone","state":"open"}]}],
	 "index":{"hunks":[{"id":"h1","path":"a.go"},{"id":"h2","path":"b.go"},{"id":"h3","path":"c.go"}]}}`,
	"3": `{"schema":"baum.review/1","review":{"head":"h13","base":"b13","merge_base":"m13","version":3},
	 "summary":{"recommendation":{"decision":"approve","text":"y"},"risk_score":{"status":"scored","score":20}},
	 "sections":[{"id":"s-api","title":"Public API","kind":"change","hunks":["h1"],"risks":[{"id":"r1","tier":"high","text":"Breaks clients","state":"mitigated","mitigated_version":3}]},
	  {"id":"s-new","title":"New","kind":"change","hunks":["h2","h4"],"risks":[{"id":"r2","tier":"medium","text":"New risk","state":"open"}]}],
	 "stats":{"validation":{"head":1,"earlier":0,"uncommitted":0,"unknown":0,"set_aside":0,"not_verified":1}},
	 "index":{"hunks":[{"id":"h1","path":"a.go"},{"id":"h2","path":"b.go"},{"id":"h4","path":"b.go"}],
	  "validation":[{"id":"v1","session":"s2","method":"go test ./search/...","checked":"the search tests","outcome":"passed","evidence_class":"ran","commit_label":{"state":"head","reason":"no edits after"},"event_ids":["e1"]}],
	  "sessions":[{"id":"s2","vendor":"claude","restricted":false,"not_verified":[{"what":"the UI"}],"events":[{"id":"e1","at":"t","verb":"ran","summary":"go test"},{"id":"e2","at":"t","verb":"read","summary":"read a.go"}]},
	   {"id":"s3","vendor":"codex","restricted":true}]}}`,
}

const reviewTranscript = `{"session":{"working_dir":"/home/dev/secret","user_email":"dev@example.com","prompts":[
 {"turn_id":"t1","ordinal":1,"text":"Add search"},
 {"turn_id":"t2","ordinal":2,"text":"Fix the flaky test","replies":[{"text":"on it"}]},
 {"turn_id":"t3","ordinal":3,"text":"Ship it"}]},
 "activity":{"t1":{"messages":[{"text":"Added the index"}]},
  "t2":{"messages":[{"text":"The test was flaky because it read the clock"}],"tool_calls":[{"name":"Bash","input":"go test ./search -run TestFlaky","output":"ok"}]},
  "t3":{"messages":[{"text":"Pushed"}]}}}`

func (d *readsDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := r.URL.Path
	reply := func(status int, body string) {
		d.mu.Lock()
		d.seen = append(d.seen, r.Method+" "+r.URL.RequestURI())
		d.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}
	cloned := q.Get("clone") == "c-web"
	switch {
	case p == "/api/index":
		reply(200, reviewIndex)
	case p == "/api/document" && cloned && q.Get("pr") == "13":
		version := q.Get("version")
		if version == "" {
			version = "3"
		}
		if doc, ok := reviewVersions[version]; ok {
			reply(200, doc)
		} else {
			reply(404, "not found")
		}
	case p == "/api/sessions/s2" && cloned && q.Get("pr") == "13":
		reply(200, reviewTranscript)
	case p == "/api/prs/threads":
		reply(200, fmt.Sprintf(`{"state":%q,"threads":[{"id":"t","resolved":true,"messages":[{"author":"ana","body":"done"}]}],"capped":false}`, q.Get("state")))
	case p == "/api/prs/timeline":
		reply(200, `{"events":[
		 {"type":"force_pushed","at":"2026-11-01T00:00:00Z","actor":"ana","before":"aaa","after":"bbb"},
		 {"type":"cross_referenced","at":"2026-11-02T00:00:00Z","actor":"bo","source":{"kind":"pull_request","repository":"acme/web","number":11,"title":"Other","state":"OPEN","url":"u11"},"will_close":false},
		 {"type":"cross_referenced","at":"2026-11-02T00:00:00Z","actor":"bo","source":{"kind":"issue","repository":"acme/web","number":7,"title":"Search is slow","state":"OPEN","url":"u7"},"will_close":true},
		 {"type":"referenced","at":"2026-11-03T00:00:00Z","actor":"cy","commit_oid":"c0ffee1","repository":"acme/web"}],"capped":false}`)
	case p == "/api/issue":
		if q.Get("number") != "7" {
			reply(404, `{"code":"not_found","message":"no issue"}`)
			return
		}
		reply(200, `{"repository":"acme/web","number":7,"title":"Search is slow","state":"OPEN","body":"It is slow.","labels":["perf"],"comments":[],"linked_pull_requests":[]}`)
	case strings.HasPrefix(p, "/api/clone/") && !cloned:
		reply(404, `{"code":"clone_unknown","message":"no clone"}`)
	case p == "/api/clone/log" && q.Get("rev") == "b13":
		reply(200, `{"rev":"b13","commits":[{"sha":"m1","date":"2026-10-01T00:00:00Z","subject":"Old fix (#9)"},{"sha":"m2","date":"2026-09-01T00:00:00Z","subject":"Merge pull request #8 from x/y"},{"sha":"m3","subject":"No pull request"},{"sha":"m4","subject":"Again (#13)"}],"truncated":false}`)
	case p == "/api/clone/log" && q.Get("rev") == "h13":
		reply(200, fmt.Sprintf(`{"rev":"h13","paths":[%q],"commits":[{"sha":"c9","subject":"Fix thing (#9)"},{"sha":"h12","subject":"wip"},{"sha":"c1","subject":"plain"}],"truncated":false}`, q.Get("path")))
	case p == "/api/clone/log" || p == "/api/clone/blame":
		reply(422, `{"code":"commit_missing","message":"the clone lacks it"}`)
	case p == "/api/clone/file":
		reply(200, fmt.Sprintf(`{"path":%q,"rev":%q,"content":"text at %s","truncated":false}`, q.Get("path"), q.Get("rev"), q.Get("rev")))
	case p == "/api/clone/changed":
		files := map[string]string{"h11": `["a.go","z.go"]`, "h12": `["c.go"]`}[q.Get("head")]
		if files == "" || q.Get("base") != "b13" {
			reply(422, `{"code":"commit_missing","message":"x"}`)
			return
		}
		reply(200, `{"files":`+files+`}`)
	default:
		d.fakeDaemon.ServeHTTP(w, r)
	}
}

func TestReviewReadToolsAnswer(t *testing.T) {
	s, d, token := readsServer(t)
	for _, tt := range []struct {
		tool   string
		args   any
		want   []string
		not    []string
		reads  string
		failed string
	}{
		// Threads: unresolved as the app lists them, or any state.
		{tool: "review_threads", args: map[string]any{"state": "all"}, want: []string{`"state":"all"`, `"resolved":true`}, reads: "GET /api/prs/threads?pr=13&repository=acme%2Fweb&state=all"},
		{tool: "review_threads", reads: "GET /api/prs/review-threads?pr=13&repository=acme%2Fweb"},
		// A transcript by turn, and searched.
		{tool: "session_transcript", args: map[string]any{"id": "s2", "from_turn": 2, "limit": 1},
			want: []string{`"turn_id":"t2"`, `"next_turn":3`, `The test was flaky`}, not: []string{`"t1"`, `"t3"`, "secret", "dev@example.com"}},
		{tool: "session_transcript", args: map[string]any{"id": "s2"}, want: []string{`"turn_id":"t1"`, `"turn_id":"t3"`}},
		{tool: "transcript_search", args: map[string]any{"session": "s2", "query": "Flaky TEST"},
			want: []string{`"turn":2`, `"where":"prompt"`, `"where":"tool call Bash input"`, `"more":false`}, not: []string{`"turn":1`, `"turn":3`}},
		{tool: "transcript_search", args: map[string]any{"session": "s2", "query": "nothing like this"}, want: []string{`"turns":[]`}},
		// GitHub's timeline and an issue.
		{tool: "pull_request_timeline", want: []string{`"before":"aaa"`, `"after":"bbb"`}, reads: "GET /api/prs/timeline?pr=13&repository=acme%2Fweb"},
		{tool: "issue", args: map[string]any{"number": 7}, want: []string{`"title":"Search is slow"`}, reads: "GET /api/issue?number=7&repository=acme%2Fweb"},
		{tool: "issue", args: map[string]any{"number": 13}, failed: "may be a pull request"},
		// The clone's history, tied to pull requests.
		{tool: "git_log", args: map[string]any{"path": "a.go"}, reads: "GET /api/clone/log?clone=c-web&limit=30&path=a.go&rev=h13",
			want: []string{`{"pull_request":9,"pull_request_from":"subject","sha":"c9"`, `"pull_request":12,"pull_request_from":"pull request head","sha":"h12"`, `"rev":"h13"`, `"rev_asked":"head"`}, not: []string{`"pull_request":0`}},
		{tool: "git_log", args: map[string]any{"rev": "base"}, failed: "does not have the commit", reads: "GET /api/clone/log?clone=c-web&limit=30&rev=m13"},
		{tool: "git_log", args: map[string]any{"rev": "HEAD~1"}, failed: "head, base or a commit id"},
		{tool: "git_blame", args: map[string]any{"path": "a.go", "start": 10}, failed: "does not have the commit", reads: "GET /api/clone/blame?clone=c-web&end=109&path=a.go&rev=h13&start=10"},
		{tool: "git_blame", args: map[string]any{"path": "a.go", "start": 10, "end": 5000}, failed: "2000 lines"},
		// Any file, at the head or the base.
		{tool: "file", args: map[string]any{"path": "go.mod", "rev": "base"}, want: []string{`"content":"text at m13"`}, reads: "GET /api/clone/file?clone=c-web&path=go.mod&rev=m13"},
		// What changed between versions.
		{tool: "review_version_diff", want: []string{`"versions":[2,3]`, `"heads":{"from":"h13a","to":"h13"}`,
			`"sections_added":[{"id":"s-new","title":"New"}]`, `"sections_removed":[{"id":"s-old","title":"Old"}]`,
			`"sections_renamed":[{"id":"s-api","from":"API","to":"Public API"}]`, `"hunks_moved":[{"hunk":"h2","path":"b.go","from":"s-api","to":"s-new"}]`,
			`"hunks_added":1`, `"hunks_removed":1`, `"risks_added":[{"section":"s-new","id":"r2","tier":"medium","text":"New risk"}]`,
			`"risks_mitigated":[{"section":"s-api","id":"r1"`, `"risks_removed":[{"section":"s-old","id":"r9"`,
			`"recommendation":{"from":"look_first","to":"approve"}`, `"risk_score":{"from":40,"to":20}`}},
		{tool: "review_version_diff", args: map[string]any{"from": 3}, failed: "before 3"},
		// The checks its sessions ran.
		{tool: "validation_runs", want: []string{`"method":"go test ./search/..."`, `"outcome":"passed"`, `"commit_label":{"state":"head"`, `"not_verified":[{"what":"the UI"}]`,
			`"cited_events":[{"id":"e1"`, `"version":3`, `"from":"review"`}, not: []string{`"s3"`, `read a.go`}},
	} {
		d.reset()
		text, failed := data(t, s, token, tt.tool, tt.args)
		if (tt.failed != "") != failed || (failed && !strings.Contains(text, tt.failed)) {
			t.Errorf("%s %v: failed=%v %s", tt.tool, tt.args, failed, text)
			continue
		}
		for _, want := range tt.want {
			if !strings.Contains(text, want) {
				t.Errorf("%s %v lacks %s:\n%s", tt.tool, tt.args, want, text)
			}
		}
		for _, not := range tt.not {
			if strings.Contains(text, not) {
				t.Errorf("%s %v has %s:\n%s", tt.tool, tt.args, not, text)
			}
		}
		if tt.reads != "" && !slices.Contains(d.requests(), tt.reads) {
			t.Errorf("%s %v read %v, want %s", tt.tool, tt.args, d.requests(), tt.reads)
		}
	}
}

func TestRelatedPullRequests(t *testing.T) {
	s, _, token := readsServer(t)
	text, failed := data(t, s, token, "related_pull_requests", nil)
	if failed {
		t.Fatal(text)
	}
	var out struct {
		Stack struct {
			BasedOn     []relatedPull    `json:"based_on"`
			BasedOnThis []relatedPull    `json:"based_on_this"`
			Groups      []map[string]any `json:"groups"`
		} `json:"stack"`
		MentionedBy []relatedPull `json:"mentioned_by"`
		SameFiles   struct {
			Paths  []string      `json:"paths"`
			Open   []relatedPull `json:"open"`
			Merged []relatedPull `json:"merged"`
		} `json:"same_files"`
		Notes []string `json:"notes"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err, text)
	}
	numbers := func(list []relatedPull) []int {
		var n []int
		for _, p := range list {
			n = append(n, p.Number)
		}
		return n
	}
	if len(out.Stack.BasedOn) != 0 || !slices.Equal(numbers(out.Stack.BasedOnThis), []int{12}) || out.Stack.BasedOnThis[0].Title != "Search UI" {
		t.Errorf("stack %+v", out.Stack)
	}
	if len(out.Stack.Groups) != 2 { // g-web and g-solo hold 13
		t.Errorf("groups %v", out.Stack.Groups)
	}
	if got := numbers(out.MentionedBy); !slices.Equal(got, []int{11, 7, 0}) || !out.MentionedBy[0].SavedReview || out.MentionedBy[1].Kind != "issue" || out.MentionedBy[2].Commit != "c0ffee1" {
		t.Errorf("mentioned by %+v", out.MentionedBy)
	}
	if !slices.Equal(out.SameFiles.Paths, []string{"a.go", "b.go"}) {
		t.Errorf("paths %v", out.SameFiles.Paths)
	}
	if !slices.Equal(numbers(out.SameFiles.Open), []int{11}) || !slices.Equal(out.SameFiles.Open[0].Shared, []string{"a.go"}) || out.SameFiles.Open[0].Author != "cy" {
		t.Errorf("open %+v", out.SameFiles.Open)
	}
	if !slices.Equal(numbers(out.SameFiles.Merged), []int{9, 8}) || out.SameFiles.Merged[0].Title != "Old fix" || !out.SameFiles.Merged[0].SavedReview || out.SameFiles.Merged[1].MergedAt != "2026-09-01T00:00:00Z" {
		t.Errorf("merged %+v", out.SameFiles.Merged)
	}
	if !slices.ContainsFunc(out.Notes, func(n string) bool { return strings.Contains(n, "1 open pull requests not compared") }) {
		t.Errorf("notes %v", out.Notes)
	}
	// Only the open ones, at most one.
	text, _ = data(t, s, token, "related_pull_requests", map[string]any{"state": "open", "paths": []string{"c.go"}})
	if !strings.Contains(text, `"number":12`) || strings.Contains(text, `"merged"`) {
		t.Errorf("open only: %s", text)
	}
}

// An action's reads of the new routes stay in its repository.
func TestActionsReadTheNewRoutesOfTheirRepositoryOnly(t *testing.T) {
	s, d, _ := readsServer(t)
	action, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 13})
	defer revoke()
	for _, tt := range []struct {
		tool   string
		args   any
		failed bool
	}{
		{"pull_request_timeline", nil, false},
		{"issue", map[string]any{"number": 7}, false},
		{"issue", map[string]any{"repository": "evil/secrets", "number": 7}, true},
		{"review_threads", map[string]any{"state": "resolved"}, false},
		{"file", map[string]any{"path": "go.mod", "rev": "abcdef1"}, false},
		{"git_log", map[string]any{"repository": "evil/secrets", "pr": 1}, true},
		{"validation_runs", nil, false},
	} {
		d.reset()
		if text, failed := data(t, s, action, tt.tool, tt.args); failed != tt.failed {
			t.Errorf("%s %v: failed=%v %s", tt.tool, tt.args, failed, text)
		}
		for _, request := range d.requests() {
			if strings.Contains(request, "evil") || strings.Contains(request, "c-evil") {
				t.Errorf("%s read %s", tt.tool, request)
			}
		}
	}
}
