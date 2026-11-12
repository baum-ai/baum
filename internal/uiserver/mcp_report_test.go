package uiserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/analytics"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/clonefind"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/decisionlog"
	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/doctor"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/githubrepos"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/planusage"
	"github.com/baum-ai/baum/internal/reviewindex"
	"github.com/baum-ai/baum/internal/uiserver"
	"github.com/baum-ai/baum/internal/uistate"
)

// reportFixture is the daemon's LocalAPI with the real saved UI state, decision
// capture, registry, clone finder, GitHub repository picker, plan usage and
// analytics, behind an MCP server. Fakes: the review index is a snapshot, the
// jobs are a fixed list (a real queue needs an executor), the GitHub lister
// answers a fixed list and the diagnostics are a fixed report.
type reportFixture struct {
	*readFixture
	ui    *uistate.Store
	home  string
	clone string
	// clonePath is the registered clone's folder, which no tool may return.
	clonePath string
	plans     *planusage.Service
	fetches   *atomic.Int32
}

type countingPlanSource struct{ n *atomic.Int32 }

func (c countingPlanSource) Read(context.Context) planusage.Plan {
	c.n.Add(1)
	used := 41.0
	return planusage.Plan{Provider: "claude", Available: true, Plan: "Max 5x", Windows: []planusage.Window{{Name: "5-hour", UsedPercent: &used}}, FetchedAt: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)}
}

type fixedLister struct{}

func (fixedLister) ViewerRepositories(context.Context, string, int, *string) (github.ViewerRepositoryList, error) {
	return github.ViewerRepositoryList{TotalCount: 2, Repositories: []github.ViewerRepository{
		{Repository: "owner/repo", Private: true, Description: "The repo", PushedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
		{Repository: "owner/lib", Fork: true},
	}}, nil
}

func (fixedLister) SearchRepositories(context.Context, string, string, github.SearchScope, *string) (github.ViewerRepositoryList, error) {
	return github.ViewerRepositoryList{}, nil
}

const (
	reportHome = "/home/dev/hidden-home"
	reportEnv  = "SENTINEL_ENV_VALUE"
)

// reportJobs answers the jobs routes from a fixed list: one fix job of
// owner/repo that ran, another repository's, and a failed review.
func reportJobs(cloneID string) http.Handler {
	fix := jobs.Job{ID: "j-fix", State: "completed", Phase: "done", Summary: "Renamed the helper", Prepared: jobs.Prepared{Repository: "owner/repo", Backend: "claude", Model: "opus", Item: jobs.Item{Clone: cloneID, PR: 7, Kind: jobs.KindFix}}}
	failed := jobs.Job{ID: "j-failed", State: "failed", Phase: "done", Reason: "agent_failed", Detail: "The agent stopped.", Prepared: jobs.Prepared{Repository: "owner/repo", Backend: "codex", Model: "gpt", HeadSHA: "deadbeef", ConfigDigest: reportEnv, Item: jobs.Item{Clone: cloneID, PR: 7}}}
	foreign := jobs.Job{ID: "j-foreign", State: "failed", Reason: "timeout", Prepared: jobs.Prepared{Repository: "other/repo", Item: jobs.Item{PR: 1}}}
	all := []jobs.Job{fix, failed, foreign}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/jobs", func(w http.ResponseWriter, r *http.Request) {
		readWriteJSON(w, map[string]any{"available": true, "jobs": all})
	})
	mux.HandleFunc("GET /api/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		for _, j := range all {
			if j.ID == r.PathValue("id") {
				readWriteJSON(w, j)
				return
			}
		}
		http.NotFound(w, r)
	})
	return mux
}

func newReportFixture(t *testing.T) *reportFixture {
	t.Helper()
	registry, clone := readRegistry(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := appdbtest.TempDir(t)
	ui := &uistate.Store{Path: filepath.Join(dir, "ui.sqlite")}
	store, err := decisionlog.Open(filepath.Join(dir, "decisions.sqlite"), decisionlog.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	decisionsAPI := &decisionlog.API{Store: store, Registry: registry}
	fetches := new(atomic.Int32)
	plans := &planusage.Service{Sources: []planusage.Source{countingPlanSource{fetches}}}
	index := reviewindex.Snapshot{Entries: []reviewindex.Entry{{PullRequest: discovery.PullRequest{Repository: "owner/repo", Number: 7, HeadSHA: "aaaaaaaa", Title: "Add search"}, CloneID: clone.ID, Status: "none"}}}
	picker := &githubrepos.Service{Lister: fixedLister{}, Registry: registry, Home: home}
	finder := &clonefind.Service{Registry: registry}
	report := doctor.Report{Status: doctor.StatusWarning, Checks: []doctor.Check{
		{ID: "claude", Label: "Claude Code", Status: doctor.StatusOK, Detail: "DETAIL " + reportEnv, Path: reportHome + "/bin/claude", Version: "2.1.0", Source: "PATH", Fix: "FIX " + reportEnv},
		{ID: "git", Label: "git", Status: doctor.StatusWarning, Detail: "DETAIL " + reportEnv, Path: reportHome + "/bin/git", Version: "2.50", Fix: "FIX"},
	}}
	local := uiserver.LocalAPI{Registry: registry, Config: config.Default(), Index: readJSONHandler(index), UIState: ui.Handler(), Jobs: reportJobs(clone.ID),
		Decisions:          func(uiserver.LinkedSessions) http.Handler { return decisionsAPI.Handler() },
		Clones:             finder.Handler(),
		GitHubRepositories: picker.Handler(),
		PlanUsage:          plans.Handler(),
		PlanUsageCached: func() []json.RawMessage {
			var out []json.RawMessage
			for _, p := range plans.Cached() {
				raw, _ := json.Marshal(p)
				out = append(out, raw)
			}
			return out
		},
		Analytics:   (&analytics.Service{Registry: registry}).Handler(),
		Diagnostics: func(context.Context) doctor.Report { return report },
		Version:     "test",
	}
	api := local.Handler()
	t.Cleanup(api.Close)
	mcp := agentcontext.New(agentcontext.Options{API: api, Version: "test", PlanUsage: local.CachedPlans, Clone: func(ctx context.Context, id string) (string, error) {
		return uiserver.RegisteredRepository(ctx, registry, id)
	}})
	return &reportFixture{readFixture: &readFixture{api: api, mcp: mcp}, ui: ui, home: home, clone: clone.ID, clonePath: clone.Path, plans: plans, fetches: fetches}
}

func TestKeptStateAndDecisionsToolsReadTheRealRoutes(t *testing.T) {
	f := newReportFixture(t)
	chat, action := f.tokens(t)
	// The person's draft review and walkthrough place of pull request 7, and
	// drafts the action must not read: pull request 8's, another repository's.
	draft := func(number int, body string) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"comments":[{"id":"c1","target":{"kind":"file","id":"main.go","title":"main.go"},"body":%q,"place":{"path":"main.go","line":3,"side":"RIGHT"},"head":"aaaaaaaa","created":1700000000000}],"note":"Note of %d","sent":[]}`, body, number))
	}
	n := 0
	for key, value := range map[string]json.RawMessage{
		"owner/repo#7":    draft(7, "Draft of seven"),
		"owner/repo#8":    draft(8, "SECRET draft of eight"),
		"other/repo#7":    draft(7, "SECRET other repository"),
		"owner/repo#7@v2": json.RawMessage(`{"seen":["stop_1","stop_2"],"notes":{"stop_1":"This looks off"},"note":"Overall seven"}`),
		"owner/repo#8@v1": json.RawMessage(`{"seen":["stop_9"],"notes":{},"note":"SECRET walkthrough of eight"}`),
	} {
		kind := "review"
		if strings.Contains(key, "@v") {
			kind = "walkthrough"
		}
		n++
		if _, err := f.ui.Replace(kind, key, 0, fmt.Sprintf("clientclientclient:%d", n), value); err != nil {
			t.Fatal(err)
		}
	}
	type comment struct {
		Body    string          `json:"body"`
		Place   json.RawMessage `json:"place"`
		Head    string          `json:"head"`
		Created int64           `json:"created"`
	}
	for name, token := range map[string]string{"chat": chat, "action": action} {
		t.Run(name, func(t *testing.T) {
			args := map[string]any{"repository": "owner/repo", "pr": 7}
			text, failed := f.callTool(t, token, "pending_review", args)
			if failed {
				t.Fatalf("pending_review: %s", text)
			}
			// The route's own comments, projected: the same text, place and head.
			var route struct {
				Value struct {
					Comments []comment `json:"comments"`
					Note     string    `json:"note"`
				} `json:"value"`
			}
			if err := json.Unmarshal(f.route(t, "/api/ui-state/review?key=owner%2Frepo%237"), &route); err != nil {
				t.Fatal(err)
			}
			var tool struct {
				Comments []comment `json:"comments"`
				Note     string    `json:"note"`
			}
			if err := json.Unmarshal([]byte(text), &tool); err != nil || len(tool.Comments) != 1 || len(route.Value.Comments) != 1 {
				t.Fatalf("pending_review %v: %s", err, text)
			}
			a, b := tool.Comments[0], route.Value.Comments[0]
			if a.Body != b.Body || a.Head != b.Head || a.Created != b.Created || tool.Note != route.Value.Note || a.Body != "Draft of seven" {
				t.Errorf("the draft %+v, the route's %+v", tool, route.Value)
			}
			readSameJSON(t, "place", a.Place, b.Place)
			if strings.Contains(text, "SECRET") || strings.Contains(text, `"id":"c1"`) {
				t.Errorf("pending_review: %s", text)
			}

			text, failed = f.callTool(t, token, "walkthrough_position", map[string]any{"repository": "owner/repo", "pr": 7, "version": 2})
			if failed || !strings.Contains(text, `"seen":["stop_1","stop_2"]`) || !strings.Contains(text, "This looks off") || !strings.Contains(text, "Overall seven") || strings.Contains(text, "SECRET") {
				t.Errorf("walkthrough_position: %v %s", failed, text)
			}

			// Another pull request's, or another repository's: a chat reads
			// them; an action reads neither.
			for tool, args := range map[string]map[string]any{
				"pending_review":       {"repository": "owner/repo", "pr": 8},
				"walkthrough_position": {"repository": "owner/repo", "pr": 8, "version": 1},
				"pending_review ":      {"repository": "other/repo", "pr": 7},
			} {
				text, failed := f.callTool(t, token, strings.TrimSpace(tool), args)
				if name == "action" && (!failed || strings.Contains(text, "SECRET")) {
					t.Errorf("an action's %s %v: %v %s", tool, args, failed, text)
				}
				if name == "chat" && (failed || !strings.Contains(text, "SECRET")) {
					t.Errorf("a chat's %s %v: %v %s", tool, args, failed, text)
				}
			}
		})
	}
	// Decisions: the route's answer, for the pull request.
	for name, token := range map[string]string{"chat": chat, "action": action} {
		text, failed := f.callTool(t, token, "pull_request_decisions", map[string]any{"repository": "owner/repo", "pr": 7})
		if failed {
			t.Fatalf("%s: pull_request_decisions: %s", name, text)
		}
		readSameJSON(t, name+" decisions", json.RawMessage(text), f.route(t, "/api/decisions?repository=owner/repo&pr=7"))
	}
}

func TestRepositoryAndReportToolsCarryNoPathOrEnvironment(t *testing.T) {
	f := newReportFixture(t)
	chat, _ := f.tokens(t)
	noPaths := func(what, text string) {
		t.Helper()
		for _, leak := range []string{f.home, f.clonePath, reportHome, reportEnv, "common_dir", `"path"`, "DETAIL", "FIX", "report_issue", "organization_access"} {
			if strings.Contains(text, leak) {
				t.Errorf("%s carries %q: %s", what, leak, text)
			}
		}
	}

	// The registry's repositories: the route has the clone's paths, the tool
	// does not.
	routeText := string(f.route(t, "/api/repositories"))
	if !strings.Contains(routeText, f.clonePath) {
		t.Fatalf("the route has no path to leave out: %s", routeText)
	}
	text, failed := f.callTool(t, chat, "repositories", nil)
	if failed {
		t.Fatalf("repositories: %s", text)
	}
	noPaths("repositories", text)
	var rows struct {
		Repositories []struct {
			Repository string `json:"repository"`
			Clones     int    `json:"clone_count"`
			Available  int    `json:"available_clones"`
			CloneList  []struct {
				ID        string `json:"id"`
				Available bool   `json:"available"`
			} `json:"clones"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal([]byte(text), &rows); err != nil || len(rows.Repositories) == 0 || rows.Repositories[0].Repository != "owner/repo" ||
		rows.Repositories[0].Clones != 1 || rows.Repositories[0].Available != 1 || rows.Repositories[0].CloneList[0].ID != f.clone {
		t.Errorf("repositories %v: %s", err, text)
	}

	// The clone finder found nothing yet: the real route's own empty answer.
	text, failed = f.callTool(t, chat, "clones_found", nil)
	if failed || !strings.Contains(text, `"repositories":[]`) || !strings.Contains(text, `"attached":[]`) {
		t.Errorf("clones_found: %v %s", failed, text)
	}
	noPaths("clones_found", text)

	// The picker's list, through the exception: the route is the account's
	// own list, which only the tool's projection reaches.
	text, failed = f.callTool(t, chat, "github_repositories", nil)
	if failed {
		t.Fatalf("github_repositories: %s", text)
	}
	noPaths("github_repositories", text)
	for _, want := range []string{`"repository":"owner/repo","private":true`, `"description":"The repo"`, `"picked":true,"cloned":true`, `"repository":"owner/lib"`, `"fork":true`} {
		if !strings.Contains(text, want) {
			t.Errorf("github_repositories lacks %s: %s", want, text)
		}
	}

	// The clone target, from the real service: written from home.
	var target struct {
		Dir string `json:"dir"`
	}
	if err := json.Unmarshal(f.route(t, "/api/repositories/owner/new/clone-target"), &target); err != nil || !strings.HasPrefix(target.Dir, f.home) {
		t.Fatalf("the route's target %q: %v", target.Dir, err)
	}
	text, failed = f.callTool(t, chat, "clone_target", map[string]any{"repository": "owner/new"})
	if failed || !strings.Contains(text, `"folder":"~`+strings.TrimPrefix(target.Dir, f.home)+`"`) {
		t.Errorf("clone_target %q: %v %s", target.Dir, failed, text)
	}
	noPaths("clone_target", text)

	// Diagnostics: the route carries every planted value; the tool none.
	routeText = string(f.route(t, "/api/diagnostics"))
	for _, planted := range []string{reportHome, "DETAIL", "FIX", "report_issue"} {
		if !strings.Contains(routeText, planted) {
			t.Fatalf("the route lacks %q: %s", planted, routeText)
		}
	}
	text, failed = f.callTool(t, chat, "diagnostics_summary", nil)
	if failed {
		t.Fatalf("diagnostics_summary: %s", text)
	}
	noPaths("diagnostics_summary", text)
	for _, want := range []string{`"status":"warning"`, `{"id":"claude","label":"Claude Code","status":"ok","version":"2.1.0","source":"PATH"}`, `{"id":"git","label":"git","status":"warning","version":"2.50"}`} {
		if !strings.Contains(text, want) {
			t.Errorf("diagnostics_summary lacks %s: %s", want, text)
		}
	}
}

// plan_usage reads the plans the daemon last read and never makes it read:
// the provider is never called, whatever state the cache is in.
func TestPlanUsageToolNeverFetches(t *testing.T) {
	f := newReportFixture(t)
	chat, _ := f.tokens(t)
	call := func() string {
		t.Helper()
		text, failed := f.callTool(t, chat, "plan_usage", nil)
		if failed {
			t.Fatalf("plan_usage: %s", text)
		}
		return text
	}
	// Nothing read yet: no plans, and no read.
	if text := call(); !strings.Contains(text, `"plans":[]`) || f.fetches.Load() != 0 {
		t.Errorf("an empty cache: %s after %d reads", text, f.fetches.Load())
	}
	// The app reads (as its sidebar does), and the tool shows what it read.
	f.route(t, "/api/plan-usage")
	if f.fetches.Load() != 1 {
		t.Fatalf("the route read %d times", f.fetches.Load())
	}
	text := call()
	if !strings.Contains(text, `"provider":"claude"`) || !strings.Contains(text, `"used_percent":41`) || !strings.Contains(text, `"plan":"Max 5x"`) || f.fetches.Load() != 1 {
		t.Errorf("plan_usage: %s after %d reads", text, f.fetches.Load())
	}
	// A cache older than its lifetime is still shown as it is, not read again.
	f.plans.Now = func() time.Time { return time.Now().Add(24 * time.Hour) }
	for range 3 {
		call()
	}
	if f.fetches.Load() != 1 {
		t.Errorf("the tool caused %d reads", f.fetches.Load()-1)
	}
	// And the route stays out of every tool's reach.
	for tool, routes := range f.mcp.ToolReads() {
		for _, route := range routes {
			if strings.HasPrefix(route, "/api/plan-usage") {
				t.Errorf("%s reads %s", tool, route)
			}
		}
	}
}

func TestAnalyticsAndJobToolsReadTheRealRoutes(t *testing.T) {
	f := newReportFixture(t)
	chat, action := f.tokens(t)
	text, failed := f.callTool(t, chat, "analytics", map[string]any{"days": 7})
	if failed {
		t.Fatalf("analytics: %s", text)
	}
	var route map[string]json.RawMessage
	if err := json.Unmarshal(f.route(t, "/api/analytics?days=7"), &route); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"filters", "options", "summary", "by_agent", "by_repository", "baum", "delivery", "commits"} {
		readSameJSON(t, "analytics "+key, readField(t, text, key), route[key])
	}
	// The window's end is the moment of the read; its length and start are the route's.
	var window, routeWindow struct {
		Days  int    `json:"days"`
		Start string `json:"start"`
	}
	if err := json.Unmarshal(readField(t, text, "window"), &window); err != nil || json.Unmarshal(route["window"], &routeWindow) != nil || window != routeWindow || window.Days != 7 {
		t.Errorf("window %+v, the route's %+v", window, routeWindow)
	}
	if text, failed := f.callTool(t, chat, "analytics", map[string]any{"repository": "nobody/known"}); !failed {
		t.Errorf("an unknown repository: %s", text)
	}

	// Jobs: every repository's for a chat; an action has no list.
	text, failed = f.callTool(t, chat, "job_list", nil)
	if failed || !strings.Contains(text, `"id":"j-fix"`) || !strings.Contains(text, `"id":"j-foreign"`) || strings.Contains(text, "accounting") || strings.Contains(text, "config_digest") || strings.Contains(text, reportEnv) {
		t.Errorf("job_list: %v %s", failed, text)
	}
	if text, failed = f.callTool(t, chat, "job_list", map[string]any{"repository": "other/repo"}); failed || strings.Contains(text, "j-fix") || !strings.Contains(text, "j-foreign") {
		t.Errorf("job_list of one repository: %v %s", failed, text)
	}
	if text, failed = f.callTool(t, chat, "job_list", map[string]any{"state": "failed", "repository": "owner/repo"}); failed || strings.Contains(text, "j-fix") || !strings.Contains(text, "j-failed") {
		t.Errorf("job_list of failed jobs: %v %s", failed, text)
	}
	text, failed = f.callTool(t, chat, "job", map[string]any{"id": "j-fix"})
	if failed || !strings.Contains(text, `"type":"fix"`) || !strings.Contains(text, `"summary":"Renamed the helper"`) || !strings.Contains(text, `"state":"completed"`) {
		t.Errorf("job: %v %s", failed, text)
	}
	text, failed = f.callTool(t, chat, "job_failure", map[string]any{"id": "j-failed"})
	if failed || !strings.Contains(text, `"reason":"agent_failed"`) || !strings.Contains(text, `"detail":"The agent stopped."`) || !strings.Contains(text, `"backend":"codex"`) ||
		strings.Contains(text, "deadbeef") || strings.Contains(text, reportEnv) {
		t.Errorf("job_failure: %v %s", failed, text)
	}
	if text, failed = f.callTool(t, chat, "job_failure", map[string]any{"id": "j-fix"}); !failed || !strings.Contains(text, "has not failed") {
		t.Errorf("job_failure of a job that did not fail: %v %s", failed, text)
	}
	// An action's job is its repository's: the daemon's answer is checked.
	for _, tool := range []string{"job", "job_failure"} {
		if text, failed = f.callTool(t, action, tool, map[string]any{"id": "j-failed"}); failed {
			t.Errorf("an action's %s of its own job: %s", tool, text)
		}
		if text, failed = f.callTool(t, action, tool, map[string]any{"id": "j-foreign"}); !failed || strings.Contains(text, "timeout") {
			t.Errorf("an action's %s of another repository's job: %v %s", tool, failed, text)
		}
	}
}
