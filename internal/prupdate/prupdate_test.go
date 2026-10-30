package prupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/github"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// fakeGitHub answers the REST calls of one pull request and records the
// writes it receives.
type fakeGitHub struct {
	// head is the pull request's current head; mergeableState and mergeable
	// are what GitHub reports of it.
	head           string
	state          string
	mergeableState string
	mergeable      string
	canPush        bool
	// updateStatus and updateBody answer the update-branch call.
	updateStatus int
	updateBody   string
	// runs are the workflow runs of the head, as JSON objects; checks are the
	// check runs.
	runs   []string
	checks []string
	// statuses are the commit statuses.
	statuses []string
	// rerunStatus answers a re-run by run ID, 201 when absent.
	rerunStatus map[int]int
	rerunBody   map[int]string
	// graphql answers each GraphQL document.
	graphql func(query string, variables map[string]any) string

	writes []string
	bodies []string
	// gets are the REST reads it answered.
	gets []string
}

func (f *fakeGitHub) client() *github.Client {
	return github.New(func(context.Context, string) (string, error) { return "token", nil }, github.Options{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		answer := func(status int, body string) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		}
		path := r.URL.Path
		if r.Method == "GET" {
			f.gets = append(f.gets, path)
		}
		switch {
		case path == "/graphql":
			var body struct {
				Query     string
				Variables map[string]any
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if strings.HasPrefix(strings.TrimSpace(body.Query), "mutation") {
				f.writes = append(f.writes, fmt.Sprint("graphql ", body.Variables["thread"]))
			}
			return answer(200, f.graphql(body.Query, body.Variables))
		case r.Method != "GET":
			var body []byte
			if r.Body != nil {
				body, _ = io.ReadAll(r.Body)
			}
			f.writes = append(f.writes, r.Method+" "+path)
			f.bodies = append(f.bodies, string(body))
			if strings.HasSuffix(path, "/update-branch") {
				return answer(f.updateStatus, f.updateBody)
			}
			var id int
			if i := strings.Index(path, "/jobs/"); i >= 0 {
				_, _ = fmt.Sscanf(path[i:], "/jobs/%d/rerun", &id)
			} else {
				_, _ = fmt.Sscanf(path[strings.Index(path, "/runs/"):], "/runs/%d/rerun-failed-jobs", &id)
			}
			status := 201
			if s, ok := f.rerunStatus[id]; ok {
				status = s
			}
			return answer(status, f.rerunBody[id])
		case strings.HasSuffix(path, "/pulls/7"):
			state := f.state
			if state == "" {
				state = "open"
			}
			mergeable := f.mergeable
			if mergeable == "" {
				mergeable = "true"
			}
			return answer(200, fmt.Sprintf(`{"title":"T","state":%q,"draft":false,"mergeable":%s,"mergeable_state":%q,"head":{"sha":%q,"ref":"feature","repo":{"full_name":"acme/app"}},"base":{"ref":"main","repo":{"full_name":"acme/app"}}}`, state, mergeable, f.mergeableState, f.head))
		case path == "/repos/acme/app":
			return answer(200, fmt.Sprintf(`{"permissions":{"push":%v}}`, f.canPush))
		case strings.HasSuffix(path, "/actions/runs"):
			if r.URL.Query().Get("head_sha") != f.head {
				return answer(200, `{"workflow_runs":[]}`)
			}
			return answer(200, `{"workflow_runs":[`+strings.Join(f.runs, ",")+`]}`)
		case strings.HasSuffix(path, "/check-runs"):
			return answer(200, `{"check_runs":[`+strings.Join(f.checks, ",")+`]}`)
		case strings.HasSuffix(path, "/status"):
			return answer(200, `{"statuses":[`+strings.Join(f.statuses, ",")+`]}`)
		}
		return answer(404, `{"message":"Not Found"}`)
	})})
}

var repo = github.Repository{Host: "github.com", Owner: "acme", Name: "app"}

func run(id int, name, status, conclusion string) string {
	return fmt.Sprintf(`{"id":%d,"name":%q,"status":%q,"conclusion":%q}`, id, name, status, conclusion)
}

func checkRun(id int, name, conclusion, app string) string {
	return fmt.Sprintf(`{"id":%d,"name":%q,"conclusion":%q,"app":{"slug":%q},"output":{}}`, id, name, conclusion, app)
}

func code(t *testing.T, err error, want string) {
	t.Helper()
	var refusal *Error
	switch {
	case want == "" && err != nil:
		t.Fatal(err)
	case want != "" && (!errors.As(err, &refusal) || refusal.Code != want):
		t.Fatalf("error %v, want code %s", err, want)
	}
}

func TestUpdateBranch(t *testing.T) {
	accepted := `{"message":"Updating pull request branch."}`
	cases := []struct {
		name   string
		fake   fakeGitHub
		expect string
		code   string
		writes int
	}{
		{"success", fakeGitHub{head: "abc", canPush: true, mergeableState: "behind", updateStatus: 202, updateBody: accepted}, "abc", "", 1},
		{"head moved before the request", fakeGitHub{head: "new", canPush: true, updateStatus: 202}, "abc", HeadMoved, 0},
		{"closed", fakeGitHub{head: "abc", state: "closed", canPush: true}, "abc", NotOpen, 0},
		{"conflicting", fakeGitHub{head: "abc", canPush: true, mergeable: "false", mergeableState: "dirty"}, "abc", Conflict, 0},
		{"cannot push", fakeGitHub{head: "abc", canPush: false}, "abc", Forbidden, 0},
		{"head moved at GitHub", fakeGitHub{head: "abc", canPush: true, updateStatus: 422, updateBody: `{"message":"expected head sha didn't match current head ref."}`}, "abc", HeadMoved, 1},
		{"conflict at GitHub", fakeGitHub{head: "abc", canPush: true, updateStatus: 422, updateBody: `{"message":"merge conflict between base and head"}`}, "abc", Conflict, 1},
		{"already up to date", fakeGitHub{head: "abc", canPush: true, updateStatus: 422, updateBody: `{"message":"There are no new commits on the base branch."}`}, "abc", UpToDate, 1},
		{"branch protection", fakeGitHub{head: "abc", canPush: true, updateStatus: 403, updateBody: `{"message":"Cannot update protected branch."}`}, "abc", Blocked, 1},
		{"permission at GitHub", fakeGitHub{head: "abc", canPush: true, updateStatus: 403, updateBody: `{"message":"Resource not accessible by personal access token"}`}, "abc", Forbidden, 1},
		{"unknown", fakeGitHub{head: "abc", canPush: true, updateStatus: 500, updateBody: `{"message":"Something odd"}`}, "abc", Failed, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Service{Client: c.fake.client()}.UpdateBranch(context.Background(), repo, 7, c.expect)
			code(t, err, c.code)
			if len(c.fake.writes) != c.writes {
				t.Fatalf("writes %v, want %d", c.fake.writes, c.writes)
			}
			if c.writes == 1 {
				var body map[string]string
				if c.fake.writes[0] != "PUT /repos/acme/app/pulls/7/update-branch" || json.Unmarshal([]byte(c.fake.bodies[0]), &body) != nil || body["expected_head_sha"] != "abc" {
					t.Fatalf("write %v %v", c.fake.writes, c.fake.bodies)
				}
			}
		})
	}
}

func TestUpdateOptionsReportsBehind(t *testing.T) {
	fake := fakeGitHub{head: "abc", mergeableState: "behind"}
	options, err := Service{Client: fake.client()}.UpdateOptions(context.Background(), repo, 7)
	if err != nil || !options.Behind || options.Conflict || options.Fork || options.HeadRef != "feature" || options.BaseRef != "main" || options.HeadSHA != "abc" {
		t.Fatalf("options %+v, %v", options, err)
	}
}

func TestRerunFailedChecks(t *testing.T) {
	cases := []struct {
		name    string
		fake    fakeGitHub
		expect  string
		code    string
		started []string
		refused int
	}{
		{"one failed run", fakeGitHub{head: "abc", canPush: true, runs: []string{run(11, "CI", "completed", "failure")}}, "abc", "", []string{"POST /repos/acme/app/actions/runs/11/rerun-failed-jobs"}, 0},
		{"mixed runs", fakeGitHub{head: "abc", canPush: true, runs: []string{
			run(11, "CI", "completed", "failure"), run(12, "Lint", "completed", "success"), run(13, "E2E", "completed", "cancelled"),
			run(14, "Deploy", "in_progress", ""), run(15, "Docs", "completed", "timed_out"), run(16, "Skipped", "completed", "skipped"),
		}}, "abc", "", []string{
			"POST /repos/acme/app/actions/runs/11/rerun-failed-jobs", "POST /repos/acme/app/actions/runs/13/rerun-failed-jobs", "POST /repos/acme/app/actions/runs/15/rerun-failed-jobs",
		}, 0},
		{"no Actions runs", fakeGitHub{head: "abc", canPush: true, checks: []string{checkRun(1, "ci/circle", "failure", "circleci")}}, "abc", NoRuns, nil, 0},
		{"head moved", fakeGitHub{head: "new", canPush: true, runs: []string{run(11, "CI", "completed", "failure")}}, "abc", HeadMoved, nil, 0},
		{"cannot push", fakeGitHub{head: "abc", canPush: false, runs: []string{run(11, "CI", "completed", "failure")}}, "abc", Forbidden, nil, 0},
		{"permission at GitHub", fakeGitHub{head: "abc", canPush: true, runs: []string{run(11, "CI", "completed", "failure")}, rerunStatus: map[int]int{11: 403}, rerunBody: map[int]string{11: `{"message":"Resource not accessible by integration"}`}}, "abc", Forbidden, []string{"POST /repos/acme/app/actions/runs/11/rerun-failed-jobs"}, 1},
		{"one refused beside a started one", fakeGitHub{head: "abc", canPush: true, runs: []string{run(11, "CI", "completed", "failure"), run(12, "E2E", "completed", "failure")}, rerunStatus: map[int]int{11: 403}, rerunBody: map[int]string{11: `{"message":"The workflow run is too old to be re-run"}`}}, "abc", "", []string{
			"POST /repos/acme/app/actions/runs/11/rerun-failed-jobs", "POST /repos/acme/app/actions/runs/12/rerun-failed-jobs",
		}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result, err := Service{Client: c.fake.client()}.RerunFailedChecks(context.Background(), repo, 7, c.expect)
			code(t, err, c.code)
			if strings.Join(c.fake.writes, "\n") != strings.Join(c.started, "\n") {
				t.Fatalf("writes %v, want %v", c.fake.writes, c.started)
			}
			if len(result.Refused) != c.refused {
				t.Fatalf("refused %+v, want %d", result.Refused, c.refused)
			}
		})
	}
}

func TestRerunOptionsNamesWhatBaumCannotRerun(t *testing.T) {
	fake := fakeGitHub{head: "abc", canPush: true,
		runs: []string{run(11, "CI", "completed", "failure"), run(12, "Lint", "completed", "success")},
		checks: []string{
			checkRun(1, "build", "failure", "github-actions"),
			checkRun(2, "ci/circle", "failure", "circleci"),
			checkRun(3, "deploy", "success", "vercel"),
		}}
	options, err := Service{Client: fake.client()}.RerunOptions(context.Background(), repo, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(options.Runs) != 1 || options.Runs[0].Name != "CI" || len(options.NotRerunnable) != 1 || options.NotRerunnable[0] != "ci/circle" || options.HeadSHA != "abc" {
		t.Fatalf("options %+v", options)
	}
	if len(fake.writes) != 0 {
		t.Fatalf("reading wrote %v", fake.writes)
	}
}

func TestReviewThreadsListsTheUnresolvedOnes(t *testing.T) {
	long := strings.Repeat("é", 250)
	fake := fakeGitHub{graphql: func(string, map[string]any) string {
		return `{"data":{"repository":{"pullRequest":{"headRefOid":"abc","viewerDidAuthor":true,"reviewThreads":{"pageInfo":{"hasNextPage":false},"nodes":[
			{"id":"RT_1","path":"a.go","line":12,"isResolved":false,"viewerCanReply":true,"viewerCanResolve":true,"comments":{"totalCount":3,"nodes":[
				{"state":"SUBMITTED","body":"\n  Rename this.\nIt reads badly.","url":"https://github.com/acme/app/pull/7#discussion_r1","author":{"__typename":"User","login":"alice"}},
				{"state":"SUBMITTED","body":"ok","url":"https://github.com/acme/app/pull/7#discussion_r2","author":{"__typename":"User","login":"me"}}]}},
			{"id":"RT_2","path":"b.go","line":3,"isResolved":true,"comments":{"totalCount":1,"nodes":[{"state":"SUBMITTED","body":"done"}]}},
			{"id":"RT_3","path":"c.go","originalLine":7,"isResolved":false,"isOutdated":true,"viewerCanResolve":false,"comments":{"totalCount":1,"nodes":[
				{"state":"SUBMITTED","body":"` + long + `","url":"u3","author":null}]}}]}}}}}`
	}}
	got, err := Service{Client: fake.client()}.ReviewThreads(context.Background(), repo, 7, -1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Capped || len(got.Threads) != 2 {
		t.Fatalf("threads %+v", got)
	}
	first, second := got.Threads[0], got.Threads[1]
	wantFirst := Thread{ID: "RT_1", Path: "a.go", Line: 12, Author: "alice", Excerpt: "Rename this.", Comments: 3, URL: "https://github.com/acme/app/pull/7#discussion_r1", CanResolve: true, Messages: []ThreadComment{
		{Author: "alice", Body: "\n  Rename this.\nIt reads badly.", URL: "https://github.com/acme/app/pull/7#discussion_r1"},
		{Author: "me", Body: "ok", URL: "https://github.com/acme/app/pull/7#discussion_r2"},
	}}
	if !reflect.DeepEqual(first, wantFirst) {
		t.Fatalf("first %+v, want %+v", first, wantFirst)
	}
	if body := second.Messages[0].Body; body != strings.Repeat("é", 250) {
		t.Fatalf("second message %d characters", len([]rune(body)))
	}
	if second.ID != "RT_3" || second.Line != 7 || !second.Outdated || second.CanResolve || second.Author != "" || second.Excerpt != strings.Repeat("é", 199)+"…" {
		t.Fatalf("second %+v", second)
	}
	if len(fake.writes) != 0 {
		t.Fatalf("reading wrote %v", fake.writes)
	}
}

func TestResolveThread(t *testing.T) {
	state := func(repository string, number int, resolved, canResolve bool) string {
		return fmt.Sprintf(`{"data":{"node":{"id":"RT_1","isResolved":%v,"viewerCanReply":true,"viewerCanResolve":%v,"pullRequest":{"number":%d,"repository":{"nameWithOwner":%q}}}}}`, resolved, canResolve, number, repository)
	}
	resolvedReply := `{"data":{"resolveReviewThread":{"thread":{"id":"RT_1","isResolved":true}}}}`
	cases := []struct {
		name          string
		state, answer string
		code          string
		writes        int
	}{
		{"resolves", state("Acme/App", 7, false, true), resolvedReply, "", 1},
		{"already resolved", state("acme/app", 7, true, true), "", "", 0},
		{"another pull request", state("acme/app", 8, false, true), "", NotFound, 0},
		{"another repository", state("acme/other", 7, false, true), "", NotFound, 0},
		{"not allowed", state("acme/app", 7, false, false), "", Forbidden, 0},
		{"refused by GitHub", state("acme/app", 7, false, true), `{"errors":[{"type":"FORBIDDEN","message":"no"}]}`, Failed, 1},
		{"thread unreadable", `{"data":{"node":null}}`, "", Failed, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := fakeGitHub{graphql: func(query string, variables map[string]any) string {
				if variables["thread"] != "RT_1" {
					t.Errorf("variables %v", variables)
				}
				if strings.HasPrefix(strings.TrimSpace(query), "mutation") {
					return c.answer
				}
				return c.state
			}}
			code(t, Service{Client: fake.client()}.ResolveThread(context.Background(), repo, 7, "RT_1"), c.code)
			if len(fake.writes) != c.writes {
				t.Fatalf("writes %v, want %d", fake.writes, c.writes)
			}
		})
	}
}

func TestReviewThreadsAreCachedWhileTheCountMatches(t *testing.T) {
	reads := 0
	threads := `{"id":"RT_1","path":"a.go","line":1,"isResolved":false,"viewerCanReply":true,"viewerCanResolve":true,"comments":{"totalCount":1,"nodes":[{"state":"SUBMITTED","body":"One.","author":{"__typename":"User","login":"alice"}}]}},
		{"id":"RT_2","path":"b.go","line":2,"isResolved":false,"viewerCanReply":true,"viewerCanResolve":true,"comments":{"totalCount":1,"nodes":[{"state":"SUBMITTED","body":"Two.","author":{"__typename":"User","login":"bob"}}]}}`
	fake := fakeGitHub{graphql: func(query string, _ map[string]any) string {
		switch {
		case strings.HasPrefix(strings.TrimSpace(query), "mutation"):
			return `{"data":{"resolveReviewThread":{"thread":{"id":"RT_1","isResolved":true}}}}`
		case strings.Contains(query, "ReviewThreadState"):
			return `{"data":{"node":{"id":"RT_1","isResolved":false,"viewerCanResolve":true,"pullRequest":{"number":7,"repository":{"nameWithOwner":"acme/app"}}}}}`
		}
		reads++
		return `{"data":{"repository":{"pullRequest":{"headRefOid":"abc","reviewThreads":{"pageInfo":{"hasNextPage":false},"nodes":[` + threads + `]}}}}}`
	}}
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	var counted []int
	s := Service{Client: fake.client(), Cache: &Cache{Now: func() time.Time { return now }}, Counted: func(_ context.Context, _ github.Repository, number, unresolved int) {
		counted = append(counted, number, unresolved)
	}}
	ctx := context.Background()
	list := func(unresolved int) []string {
		t.Helper()
		got, err := s.ReviewThreads(ctx, repo, 7, unresolved)
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, thread := range got.Threads {
			ids = append(ids, thread.ID)
		}
		return ids
	}
	for _, step := range []struct {
		name       string
		unresolved int
		advance    time.Duration
		want       string
		reads      int
	}{
		{"first read", 2, 0, "RT_1,RT_2", 1},
		{"same count", 2, DefaultThreadTTL / 2, "RT_1,RT_2", 1},
		{"count unknown", -1, 0, "RT_1,RT_2", 1},
		{"count changed", 3, 0, "RT_1,RT_2", 2},
		{"expired", 2, DefaultThreadTTL, "RT_1,RT_2", 3},
	} {
		now = now.Add(step.advance)
		if got := strings.Join(list(step.unresolved), ","); got != step.want || reads != step.reads {
			t.Fatalf("%s: %s after %d reads, want %s after %d", step.name, got, reads, step.want, step.reads)
		}
	}
	// Only the read that found other than discovery's count reports it.
	if !slices.Equal(counted, []int{7, 2}) {
		t.Fatalf("counted %v, want pull request 7 at 2", counted)
	}
	if err := s.ResolveThread(ctx, github.Repository{Host: "github.com", Owner: "Acme", Name: "App"}, 7, "RT_1"); err != nil {
		t.Fatal(err)
	}
	// Discovery now counts one fewer: the cache already left the resolved one out.
	if got := strings.Join(list(1), ","); got != "RT_2" || reads != 3 {
		t.Fatalf("after resolving: %s after %d reads", got, reads)
	}
}

func TestReviewThreadsForgetAnAccountChange(t *testing.T) {
	reads := 0
	var cache *Cache
	resetDuringRead := false
	fake := fakeGitHub{graphql: func(string, map[string]any) string {
		reads++
		if resetDuringRead {
			cache.ResetIdentity()
		}
		return `{"data":{"repository":{"pullRequest":{"headRefOid":"abc","reviewThreads":{"pageInfo":{"hasNextPage":false},"nodes":[
			{"id":"RT_1","path":"a.go","line":1,"isResolved":false,"viewerCanResolve":true,"comments":{"totalCount":1,"nodes":[{"state":"SUBMITTED","body":"One.","author":{"__typename":"User","login":"alice"}}]}}]}}}}}`
	}}
	cache = &Cache{}
	s := Service{Client: fake.client(), Cache: cache}
	ctx := context.Background()
	read := func() {
		t.Helper()
		if _, err := s.ReviewThreads(ctx, repo, 7, 1); err != nil {
			t.Fatal(err)
		}
	}
	read()
	read()
	if reads != 1 {
		t.Fatalf("cached list read %d times", reads)
	}
	cache.ResetIdentity()
	read()
	if reads != 2 {
		t.Fatalf("after an account change: %d reads, want 2", reads)
	}
	// A read under way while the account changes is not kept.
	cache.ResetIdentity()
	resetDuringRead = true
	read()
	resetDuringRead = false
	read()
	if reads != 4 {
		t.Fatalf("read across an account change was kept: %d reads, want 4", reads)
	}
}

func TestFailingChecks(t *testing.T) {
	fake := fakeGitHub{head: "abc", canPush: true, runs: []string{run(11, "CI", "completed", "failure")}, checks: []string{
		`{"id":21,"name":"test","conclusion":"failure","html_url":"https://github.com/acme/app/runs/21","app":{"slug":"github-actions"},"output":{"title":"","summary":"**2 tests failed** in ` + "`pkg`" + `"}}`,
		`{"id":22,"name":"lint","conclusion":"success","app":{"slug":"github-actions"},"output":{}}`,
		`{"id":23,"name":"ci/circle","conclusion":"timed_out","details_url":"https://circleci.com/x","html_url":"https://github.com/acme/app/runs/23","app":{"slug":"circleci"},"output":{"title":"Timed out after 1h"}}`,
	}}
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	s := Service{Client: fake.client(), Cache: &Cache{Now: func() time.Time { return now }}}
	ctx := context.Background()
	got, err := s.FailingChecks(ctx, repo, 7, "")
	if err != nil {
		t.Fatal(err)
	}
	want := Checks{HeadSHA: "abc", Checks: []FailingCheck{
		{Name: "test", Conclusion: "failure", Title: "2 tests failed in pkg", URL: "https://github.com/acme/app/runs/21", JobID: 21},
		{Name: "ci/circle", Conclusion: "timed_out", Title: "Timed out after 1h", URL: "https://circleci.com/x"},
	}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("checks %+v, want %+v", got, want)
	}
	reads := func() int {
		n := 0
		for _, path := range fake.gets {
			if strings.HasSuffix(path, "/check-runs") {
				n++
			}
		}
		return n
	}
	// With no head the pull request was read for it, so nothing is cached under "".
	if _, err := s.FailingChecks(ctx, repo, 7, "abc"); err != nil || reads() != 1 {
		t.Fatalf("cached checks read again: %d reads, %v", reads(), err)
	}
	if _, err := s.FailingChecks(ctx, repo, 7, "other"); err != nil || reads() != 2 {
		t.Fatalf("another head: %d reads, %v", reads(), err)
	}
	if _, err := s.RerunFailedChecks(ctx, repo, 7, "abc"); err != nil {
		t.Fatal(err)
	}
	before := reads()
	if _, err := s.FailingChecks(ctx, repo, 7, "abc"); err != nil || reads() != before+1 {
		t.Fatalf("checks kept after a re-run: %d reads, %v", reads()-before, err)
	}
	now = now.Add(DefaultChecksTTL)
	if _, err := s.FailingChecks(ctx, repo, 7, "abc"); err != nil || reads() != before+2 {
		t.Fatalf("expired checks kept: %d reads, %v", reads()-before, err)
	}
}

// A comment longer than the bound is cut with an ellipsis.
func TestReviewThreadMessagesAreBounded(t *testing.T) {
	long := strings.Repeat("x", 5000)
	fake := fakeGitHub{graphql: func(string, map[string]any) string {
		return `{"data":{"repository":{"pullRequest":{"headRefOid":"abc","reviewThreads":{"pageInfo":{"hasNextPage":false},"nodes":[
			{"id":"RT_1","path":"a.go","line":1,"isResolved":false,"comments":{"totalCount":1,"nodes":[{"state":"SUBMITTED","body":"` + long + `","author":{"__typename":"User","login":"alice"}}]}}]}}}}}`
	}}
	got, err := Service{Client: fake.client()}.ReviewThreads(context.Background(), repo, 7, -1)
	if err != nil || len(got.Threads) != 1 {
		t.Fatalf("threads %+v, %v", got, err)
	}
	if body := got.Threads[0].Messages[0].Body; body != strings.Repeat("x", maxMessage-1)+"…" {
		t.Fatalf("message of %d characters", len([]rune(body)))
	}
}

func TestAllChecks(t *testing.T) {
	fake := fakeGitHub{head: "abc", checks: []string{
		`{"id":21,"name":"test","status":"completed","conclusion":"failure","html_url":"https://github.com/acme/app/runs/21","app":{"slug":"github-actions"},"output":{"title":"2 tests failed"}}`,
		`{"id":22,"name":"lint","status":"completed","conclusion":"success","app":{"slug":"github-actions"},"output":{}}`,
		`{"id":23,"name":"build","status":"in_progress","conclusion":null,"app":{"slug":"github-actions"},"output":{}}`,
		`{"id":24,"name":"deploy","status":"waiting","conclusion":null,"app":{"slug":"github-actions"},"output":{}}`,
		`{"id":25,"name":"ci/circle","status":"completed","conclusion":"timed_out","app":{"slug":"circleci"},"output":{}}`,
		`{"id":26,"name":"docs","status":"completed","conclusion":"skipped","app":{"slug":"github-actions"},"output":{}}`,
	}, statuses: []string{
		`{"context":"ci/travis","state":"pending","description":"Waiting","target_url":"https://travis.example/1"}`,
		`{"context":"ci/jenkins","state":"error","description":"Crashed"}`,
		`{"context":"ci/ok","state":"success"}`,
	}}
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	s := Service{Client: fake.client(), Cache: &Cache{Now: func() time.Time { return now }}}
	ctx := context.Background()
	got, err := s.AllChecks(ctx, repo, 7, "")
	if err != nil {
		t.Fatal(err)
	}
	want := CheckList{HeadSHA: "abc", Checks: []CheckStatus{
		{Name: "ci/circle", Status: "completed", Conclusion: "timed_out"},
		{Name: "ci/jenkins", Status: "completed", Conclusion: "error", Title: "Crashed"},
		{Name: "test", Status: "completed", Conclusion: "failure", Title: "2 tests failed", URL: "https://github.com/acme/app/runs/21", JobID: 21},
		{Name: "build", Status: "in_progress"},
		{Name: "ci/travis", Status: "in_progress", Title: "Waiting", URL: "https://travis.example/1"},
		{Name: "deploy", Status: "queued"},
		{Name: "ci/ok", Status: "completed", Conclusion: "success"},
		{Name: "docs", Status: "completed", Conclusion: "skipped"},
		{Name: "lint", Status: "completed", Conclusion: "success"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("checks %+v, want %+v", got, want)
	}
	reads := func() int {
		n := 0
		for _, path := range fake.gets {
			if strings.HasSuffix(path, "/check-runs") {
				n++
			}
		}
		return n
	}
	if _, err := s.AllChecks(ctx, repo, 7, "abc"); err != nil || reads() != 1 {
		t.Fatalf("cached checks read again: %d reads, %v", reads(), err)
	}
	now = now.Add(DefaultAllChecksTTL)
	if _, err := s.AllChecks(ctx, repo, 7, "abc"); err != nil || reads() != 2 {
		t.Fatalf("expired checks kept: %d reads, %v", reads(), err)
	}
	// A re-run drops the list, as it does the failing checks.
	fake.canPush = true
	if err := s.RerunJob(ctx, repo, 7, "abc", 21); err != nil {
		t.Fatal(err)
	}
	before := reads()
	if _, err := s.AllChecks(ctx, repo, 7, "abc"); err != nil || reads() != before+1 {
		t.Fatalf("checks kept after a re-run: %d reads, %v", reads()-before, err)
	}
}

func TestConflictsNeedsAClone(t *testing.T) {
	fake := fakeGitHub{head: "abc"}
	_, err := Service{Client: fake.client()}.Conflicts(context.Background(), repo, 7)
	code(t, err, NoClone)
	none := Service{Client: fake.client(), Clone: func(context.Context, github.Repository) (string, bool) { return "", false }}
	_, err = none.Conflicts(context.Background(), repo, 7)
	code(t, err, NoClone)
}

func TestRerunJob(t *testing.T) {
	failing := `{"id":21,"name":"test","conclusion":"failure","app":{"slug":"github-actions"},"output":{}}`
	other := `{"id":23,"name":"ci/circle","conclusion":"failure","app":{"slug":"circleci"},"output":{}}`
	cases := []struct {
		name   string
		fake   fakeGitHub
		job    int64
		code   string
		writes int
	}{
		{"reruns the job", fakeGitHub{head: "abc", canPush: true, checks: []string{failing}}, 21, "", 1},
		{"head moved", fakeGitHub{head: "new", canPush: true, checks: []string{failing}}, 21, HeadMoved, 0},
		{"cannot push", fakeGitHub{head: "abc", checks: []string{failing}}, 21, Forbidden, 0},
		{"not a failing job", fakeGitHub{head: "abc", canPush: true, checks: []string{failing}}, 99, NotFound, 0},
		{"another app's check", fakeGitHub{head: "abc", canPush: true, checks: []string{other}}, 23, NotFound, 0},
		{"refused by GitHub", fakeGitHub{head: "abc", canPush: true, checks: []string{failing}, rerunStatus: map[int]int{21: 403}, rerunBody: map[int]string{21: `{"message":"Resource not accessible"}`}}, 21, Forbidden, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := Service{Client: c.fake.client(), Cache: &Cache{}}
			code(t, s.RerunJob(context.Background(), repo, 7, "abc", c.job), c.code)
			if len(c.fake.writes) != c.writes || (c.writes == 1 && c.fake.writes[0] != "POST /repos/acme/app/actions/jobs/21/rerun") {
				t.Fatalf("writes %v, want %d", c.fake.writes, c.writes)
			}
		})
	}
}
