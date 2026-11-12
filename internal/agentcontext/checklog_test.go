package agentcontext

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// logDaemon answers the checks of a pull request with ids, and the log tail
// of its check runs, on top of fakeDaemon's index.
type logDaemon struct{ *fakeDaemon }

func (d logDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q, p := r.URL.Query(), r.URL.Path
	reply := func(format string, args ...any) {
		d.mu.Lock()
		d.seen = append(d.seen, r.Method+" "+r.URL.RequestURI())
		d.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, format, args...)
	}
	switch {
	case r.Method != http.MethodGet:
	case p == "/api/prs/checks":
		reply(`{"head_sha":%q,"checks":[`+
			`{"name":"test","status":"completed","conclusion":"failure","id":21,"job_id":21,"has_log":true},`+
			`{"name":"lint","status":"in_progress","conclusion":"","id":22},`+
			`{"name":"dup","status":"completed","conclusion":"failure","id":31,"has_log":true},`+
			`{"name":"dup","status":"completed","conclusion":"failure","id":32,"has_log":true},`+
			`{"name":"ci/circle","status":"completed","conclusion":"failure","id":41},`+
			`{"name":"ci/status","status":"completed","conclusion":"error"}]}`, q.Get("head_sha"))
		return
	case p == "/api/prs/check-log":
		reply(`{"id":%s,"name":"check","available":true,"log":"log of %s %s %s"}`, q.Get("check"), q.Get("repository"), q.Get("pr"), q.Get("check"))
		return
	}
	d.fakeDaemon.ServeHTTP(w, r)
}

func logServer(t *testing.T) (*Server, logDaemon) {
	t.Helper()
	d := logDaemon{&fakeDaemon{}}
	return New(Options{API: d, Version: "test", Clone: func(context.Context, string) (string, error) { return "", errors.New("unknown clone") }}), d
}

func TestCheckLogReadsTheRouteOfAChecksId(t *testing.T) {
	s, d := logServer(t)
	token, id := chat(t, s)
	if err := s.Registry.SetPage(id, PageContext{Page: PagePullRequest, Repository: "acme/web", PullRequest: 12}); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		args   map[string]any
		want   []string
		failed string
	}{
		{args: map[string]any{"check": "test"}, want: []string{`"available":true`, `log of acme/web 12 21`}},
		{args: map[string]any{"check": 21}, want: []string{`log of acme/web 12 21`}},
		{args: map[string]any{"check": "21"}, failed: "not one of the pull request's checks"},
		{args: map[string]any{"pr": 13, "check": 32}, want: []string{`log of acme/web 13 32`}},
		{args: map[string]any{"check": "ci/circle"}, want: []string{`log of acme/web 12 41`}},
		{args: map[string]any{"check": "ci/status"}, want: []string{`"available":false`, `"reason":"no_log"`}},
		{args: map[string]any{"check": "dup"}, failed: "give the check's id"},
		{args: map[string]any{"check": "nothing"}, failed: "not one of the pull request's checks"},
		{args: map[string]any{"check": 999}, failed: "not one of the pull request's checks"},
		{args: map[string]any{"check": ""}, failed: "arguments"},
		{args: map[string]any{}, failed: "arguments"},
		{args: map[string]any{"check": -4}, failed: "check id"},
	} {
		d.reset()
		text, failed := data(t, s, token, "check_log", tt.args)
		if (tt.failed != "") != failed || (failed && !strings.Contains(text, tt.failed)) {
			t.Errorf("%v: failed=%v %s", tt.args, failed, text)
			continue
		}
		for _, want := range tt.want {
			if !strings.Contains(text, want) {
				t.Errorf("%v lacks %s:\n%s", tt.args, want, text)
			}
		}
		for _, request := range d.requests() {
			if !strings.HasPrefix(request, "GET ") {
				t.Errorf("%v sent %s", tt.args, request)
			}
			// The log route is asked only for a check run the checks listed.
			if strings.Contains(request, "/api/prs/check-log") && (tt.failed != "" || !strings.Contains(request, "check=")) {
				t.Errorf("%v read %s", tt.args, request)
			}
		}
	}
	if spec, _ := s.page(PagePullRequest); !contains(spec.Tools, "check_log") {
		t.Error("the pull request page does not list check_log")
	}
	if got := s.ToolReads()["check_log"]; !reflect.DeepEqual(got, []string{"/api/index", "/api/prs/checks", "/api/prs/check-log"}) {
		t.Errorf("check_log reads %v", got)
	}
}

// An action reads the log of its own repository's pull requests only: the
// binding refuses another repository before the daemon answers.
func TestCheckLogIsBoundToTheActionsRepository(t *testing.T) {
	s, d := logServer(t)
	token, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 12})
	defer revoke()
	d.reset()
	text, failed := data(t, s, token, "check_log", map[string]any{"repository": "evil/secrets", "pr": 1, "check": "test"})
	if !failed || !strings.Contains(text, "own pull request's repository") || len(d.requests()) != 0 {
		t.Errorf("another repository: failed=%v %s %v", failed, text, d.requests())
	}
	// Its own pull request by default, and the repository's others.
	if text, failed = data(t, s, token, "check_log", map[string]any{"check": "test"}); failed || !strings.Contains(text, "log of acme/web 12 21") {
		t.Errorf("its own pull request: %v %s", failed, text)
	}
	if text, failed = data(t, s, token, "check_log", map[string]any{"repository": "ACME/web", "pr": 13, "check": 21}); failed || !strings.Contains(text, "log of acme/web 13 21") {
		t.Errorf("another pull request of its repository: %v %s", failed, text)
	}
	if strings.Contains(strings.Join(d.requests(), " "), "evil") {
		t.Errorf("another repository was read: %v", d.requests())
	}
	have := listed(t, s, token)
	if !have["check_log"] {
		t.Error("an action does not list check_log")
	}

	// The binding itself, reached directly: the route's own repository must
	// be the action's, and no other parameter is allowed.
	reader := NewReader(d, nil).within([]string{"/api"}, "acme/web")
	reader.pull = 12
	get := func(target string) error {
		d.reset()
		_, err := reader.Get(context.Background(), target)
		if err != nil && len(d.requests()) > 0 {
			t.Errorf("%s reached the daemon after a refusal: %v", target, d.requests())
		}
		return err
	}
	for _, ok := range []string{"/api/prs/check-log?repository=acme%2Fweb&pr=12&check=21", "/api/prs/check-log?repository=ACME%2Fweb&pr=13&check=21"} {
		if err := get(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"/api/prs/check-log?repository=evil%2Fsecrets&pr=1&check=21",
		"/api/prs/check-log?pr=12&check=21",
		"/api/prs/check-log?repository=acme%2Fweb&repository=evil%2Fsecrets&pr=12&check=21",
		"/api/prs/check-log?repository=acme%2Fweb&pr=12&check=21&head_sha=x",
		"/api/prs/check-log?repository=acme%2Fweb&pr=12&check=21&refresh=1",
	} {
		if err := get(bad); err == nil {
			t.Errorf("%s was read", bad)
		}
	}
}
