package prupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

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
	// rerunStatus answers a re-run by run ID, 201 when absent.
	rerunStatus map[int]int
	rerunBody   map[int]string

	writes []string
	bodies []string
}

func (f *fakeGitHub) client() *github.Client {
	return github.New(func(context.Context, string) (string, error) { return "token", nil }, github.Options{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		answer := func(status int, body string) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		}
		path := r.URL.Path
		switch {
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
			_, _ = fmt.Sscanf(path[strings.Index(path, "/runs/"):], "/runs/%d/rerun-failed-jobs", &id)
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
			return answer(200, `{"statuses":[]}`)
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
			err := Service{c.fake.client()}.UpdateBranch(context.Background(), repo, 7, c.expect)
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
	options, err := Service{fake.client()}.UpdateOptions(context.Background(), repo, 7)
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
			result, err := Service{c.fake.client()}.RerunFailedChecks(context.Background(), repo, 7, c.expect)
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
	options, err := Service{fake.client()}.RerunOptions(context.Background(), repo, 7)
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
