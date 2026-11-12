package uiserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/agentrequests"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/automatic"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/inboxes"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewgroups"
	"github.com/baum-ai/baum/internal/uiserver"
	"github.com/baum-ai/baum/internal/uistate"
)

// proposalFixture is the daemon's LocalAPI over the real registry, automatic
// scheduler, agent request settings, saved inboxes, review groups and ui
// state, with the proposers over the same stores behind an MCP server, and a
// chat. What the routes do after a save is counted.
type proposalFixture struct {
	t         *testing.T
	registry  *repos.Registry
	requests  *agentrequests.Store
	api       *uiserver.LocalHandler
	mcp       *agentcontext.Server
	proposers *uiserver.Proposers
	token, id string
	// group is the id of the group made in owner/repo with #1 and #2.
	group string

	queue                                                   *countingQueue
	notified, settingsChanged, decisionsChanged, concurrent atomic.Int32
	inboxesChanged, groupsChanged                           atomic.Int32
	// afterCurrent, when set, runs once after a proposer's Current returns:
	// between Save's check of the revision and the proposer's write.
	afterCurrent atomic.Pointer[func()]
}

// countingQueue counts the cancels of disallowed automatic jobs.
type countingQueue struct{ cancels atomic.Int32 }

func (q *countingQueue) SubmitAutomatic(context.Context, jobs.AutomaticRequest) (jobs.Job, error) {
	return jobs.Job{}, errors.New("no jobs here")
}
func (q *countingQueue) Interrupted() []jobs.Job     { return nil }
func (q *countingQueue) Refused() []jobs.Job         { return nil }
func (q *countingQueue) QueuedAutomatic() []jobs.Job { return nil }
func (q *countingQueue) CancelDisallowedAutomatic() []jobs.Job {
	q.cancels.Add(1)
	return nil
}

// hooked runs the fixture's afterCurrent hook after Current.
type hooked struct {
	agentcontext.Proposer
	f *proposalFixture
}

func (h hooked) Current(ctx context.Context, page agentcontext.PageContext, target string) (json.RawMessage, string, error) {
	value, revision, err := h.Proposer.Current(ctx, page, target)
	if hook := h.f.afterCurrent.Swap(nil); hook != nil {
		(*hook)()
	}
	return value, revision, err
}

const (
	envSentinel    = "SENTINEL_ENV_VALUE"
	promptSentinel = "SENTINEL global conflicts prompt"
)

func newProposalFixture(t *testing.T) *proposalFixture {
	t.Helper()
	return newProposalFixtureWith(t, config.Default())
}

// newProposalFixtureWith is newProposalFixture over the config file cfg.
func newProposalFixtureWith(t *testing.T, cfg config.Config) *proposalFixture {
	t.Helper()
	ctx := context.Background()
	registry, _ := readRegistry(t)
	if _, err := registry.Pick(ctx, "other/repo", nil); err != nil {
		t.Fatal(err)
	}
	dir := appdbtest.TempDir(t)
	app := filepath.Join(dir, "app.sqlite")
	f := &proposalFixture{t: t, registry: registry, queue: &countingQueue{}}
	scheduler := &automatic.Scheduler{Registry: registry, Queue: f.queue, Path: app, Notify: func(automatic.Status) { f.notified.Add(1) }}
	groups, err := reviewgroups.Open(app)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = groups.Close() })
	groupsAPI := &reviewgroups.API{Store: groups, Registry: registry, Changed: func() { f.groupsChanged.Add(1) }}
	inboxesAPI := &inboxes.API{Store: inboxes.Open(app), Changed: func() { f.inboxesChanged.Add(1) }}
	uiState := &uistate.Store{Path: app}
	f.requests = &agentrequests.Store{Database: filepath.Join(dir, "requests.sqlite")}
	local := uiserver.LocalAPI{Registry: registry, Config: cfg,
		Automatic: scheduler.Handler(), AgentRequests: f.requests.Handler(), Inboxes: inboxesAPI.Handler(), Groups: groupsAPI.Handler(), UIState: uiState.Handler(),
		Changed:            func() { f.settingsChanged.Add(1) },
		DecisionsChanged:   func() { f.decisionsChanged.Add(1) },
		ConcurrencyChanged: func(n int) { f.concurrent.Store(int32(n)) },
	}
	f.api = local.Handler()
	t.Cleanup(f.api.Close)
	f.proposers = &uiserver.Proposers{API: local, Automatic: scheduler, Inboxes: inboxesAPI, Groups: groupsAPI, UIState: uiState,
		CodexApprovals: func() bool { return f.requests.Settings().CodexApprovals },
		SetCodexApprovals: func(on bool, check func(bool) error) error {
			_, err := f.requests.SetSettingsIf(agentrequests.Settings{CodexApprovals: on}, func(c agentrequests.Settings) error { return check(c.CodexApprovals) })
			return err
		}}
	pages := map[string]agentcontext.Proposer{}
	for name, p := range f.proposers.Pages() {
		pages[name] = hooked{p, f}
	}
	f.mcp = agentcontext.New(agentcontext.Options{API: f.api, Version: "test", Proposers: pages})
	token, id, revoke, err := f.mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Chat})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(revoke)
	f.token, f.id = token, id

	// What the person set, through the app's own routes.
	f.must("PUT", "/api/settings/agent-env", fmt.Sprintf(`{"agent_env":{"ZED_TOKEN":%q}}`, envSentinel))
	f.must("PUT", "/api/settings/prompts/fix-conflicts", fmt.Sprintf(`{"text":%q}`, promptSentinel))
	f.must("PUT", "/api/repositories/owner/repo/prompts/fix-conflicts", `{"text":"The repository's conflicts prompt."}`)
	var group struct{ ID string }
	if err := json.Unmarshal([]byte(f.must("POST", "/api/groups", `{"name":"Checkout","members":[{"repository":"owner/repo","number":1},{"repository":"owner/repo","number":2}]}`)), &group); err != nil || group.ID == "" {
		t.Fatal(err)
	}
	f.group = group.ID
	f.must("PUT", "/api/ui-state/review?key="+url.QueryEscape("owner/repo#7"),
		`{"revision":0,"mutation_id":"personwindow1:1","value":{"comments":[{"id":"mine","target":{"kind":"general","id":"","title":"The pull request"},"body":"My own draft.","place":null,"head":"","created":1}],"note":"My note.","sent":[]}}`)
	// An inbox an older app saved with its filter in the older fields.
	var inboxList struct {
		Revision int              `json:"revision"`
		Inboxes  []map[string]any `json:"inboxes"`
	}
	_ = json.Unmarshal([]byte(f.must("GET", "/api/inboxes", "")), &inboxList)
	inboxList.Inboxes = append(inboxList.Inboxes, olderInbox())
	seeded, _ := json.Marshal(inboxList)
	f.must("PUT", "/api/inboxes", string(seeded))
	f.settingsChanged.Store(0)
	f.groupsChanged.Store(0)
	f.inboxesChanged.Store(0)
	return f
}

// do serves one request through the LocalAPI.
func (f *proposalFixture) do(method, path, body string) (int, string) {
	f.t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, "http://local"+path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	f.api.ServeHTTP(w, r)
	return w.Code, strings.TrimSpace(w.Body.String())
}

func (f *proposalFixture) must(method, path, body string) string {
	f.t.Helper()
	code, out := f.do(method, path, body)
	if code/100 != 2 {
		f.t.Fatalf("%s %s: %d %s", method, path, code, out)
	}
	return out
}

func (f *proposalFixture) page(p agentcontext.PageContext) {
	f.t.Helper()
	if err := f.mcp.Registry.SetPage(f.id, p); err != nil {
		f.t.Fatal(err)
	}
}

// propose calls a propose tool. The tool answers its receipt and nothing
// else; the proposal recorded is returned.
func (f *proposalFixture) propose(tool string, args any) (agentcontext.Proposal, error) {
	f.t.Helper()
	text, failed := (&readFixture{mcp: f.mcp}).callTool(f.t, f.token, tool, args)
	if failed {
		return agentcontext.Proposal{}, errors.New(text)
	}
	var receipt map[string]string
	if err := json.Unmarshal([]byte(text), &receipt); err != nil {
		f.t.Fatalf("%s: %v %s", tool, err, text)
	}
	keys := slices.Sorted(func(yield func(string) bool) {
		for k := range receipt {
			if !yield(k) {
				return
			}
		}
	})
	if !slices.Equal(keys, []string{"id", "page", "target", "status"}) && !slices.Equal(keys, []string{"id", "page", "status", "target"}) {
		f.t.Fatalf("%s answered more than its receipt: %s", tool, text)
	}
	for _, p := range f.mcp.Proposals.List(f.id) {
		if p.ID == receipt["id"] {
			return p, nil
		}
	}
	f.t.Fatalf("%s: proposal %s not listed", tool, receipt["id"])
	return agentcontext.Proposal{}, nil
}

func (f *proposalFixture) save(p agentcontext.Proposal, input agentcontext.SaveInput) error {
	return f.mcp.Proposals.Save(context.Background(), f.id, p.ID, input)
}

func (f *proposalFixture) prefs() string {
	f.t.Helper()
	p, err := f.registry.Preferences(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	return string(raw)
}

// state is everything a proposal can change, in a form two fixtures share:
// generated ids and times left out.
func (f *proposalFixture) state() string {
	f.t.Helper()
	ctx := context.Background()
	repository, _, err := f.registry.RepositorySettings(ctx, "owner/repo")
	if err != nil {
		f.t.Fatal(err)
	}
	var inboxState struct {
		Revision int
		Inboxes  []map[string]any
	}
	_ = json.Unmarshal([]byte(f.must("GET", "/api/inboxes", "")), &inboxState)
	for _, inbox := range inboxState.Inboxes {
		delete(inbox, "id")
	}
	var group map[string]any
	_ = json.Unmarshal([]byte(f.must("GET", "/api/groups/"+f.group, "")), &group)
	for _, key := range []string{"id", "created_at", "updated_at"} {
		delete(group, key)
	}
	var review struct {
		Revision int
		Value    struct {
			Comments []map[string]any
			Note     string
			Sent     []any
		}
	}
	_ = json.Unmarshal([]byte(f.must("GET", "/api/ui-state/review?key="+url.QueryEscape("owner/repo#7"), "")), &review)
	for _, c := range review.Value.Comments {
		if c["id"] != "mine" {
			c["id"], c["created"] = "new", 0
		}
	}
	out, _ := json.Marshal(map[string]any{
		"prefs": json.RawMessage(f.prefs()), "repository": repository, "codex": f.requests.Settings(),
		"prompts": json.RawMessage(f.must("GET", "/api/settings/prompts", "")), "repository_prompts": json.RawMessage(f.must("GET", "/api/repositories/owner/repo/prompts", "")),
		"inboxes": inboxState, "group": group, "review": review,
	})
	return string(out)
}

// effects are what the routes did after their saves.
func (f *proposalFixture) effects() string {
	return fmt.Sprintf("cancels %d, notices %d, settings changed %d, decisions changed %d, concurrency %d, inboxes changed %d, groups changed %d",
		f.queue.cancels.Load(), f.notified.Load(), f.settingsChanged.Load(), f.decisionsChanged.Load(), f.concurrent.Load(), f.inboxesChanged.Load(), f.groupsChanged.Load())
}

// proposalCase is one proposal and the request the app's own page would send
// for the same change.
type proposalCase struct {
	name  string
	page  func(*proposalFixture) agentcontext.PageContext
	tool  string
	args  map[string]any
	input *string
	route func(*proposalFixture) (method, path, body string)
	// change is the route a person's concurrent edit of the target sends.
	change func(*proposalFixture) (method, path, body string)
	// inbox, from 1, is the saved inbox whose id the args take.
	inbox int
}

// caseArgs are the case's args, with the id of the inbox it edits.
func (tc proposalCase) caseArgs(f *proposalFixture) map[string]any {
	if tc.inbox == 0 {
		return tc.args
	}
	var state struct{ Inboxes []struct{ ID string } }
	_ = json.Unmarshal([]byte(f.must("GET", "/api/inboxes", "")), &state)
	args := map[string]any{"id": state.Inboxes[tc.inbox-1].ID}
	for k, v := range tc.args {
		args[k] = v
	}
	return args
}

func settingsAt(section string) func(*proposalFixture) agentcontext.PageContext {
	return func(*proposalFixture) agentcontext.PageContext {
		return agentcontext.PageContext{Page: agentcontext.PageSettings, Section: section}
	}
}

func repositoryAt(section string) func(*proposalFixture) agentcontext.PageContext {
	return func(*proposalFixture) agentcontext.PageContext {
		return agentcontext.PageContext{Page: agentcontext.PageRepositories, Repository: "owner/repo", Section: section}
	}
}

func fixed(method, path, body string) func(*proposalFixture) (string, string, string) {
	return func(*proposalFixture) (string, string, string) { return method, path, body }
}

// inboxPut is PUT /api/inboxes of the stored list changed by edit.
// olderInbox is an inbox saved with the older fields, which the Inbox folds
// into its query.
func olderInbox() map[string]any {
	return map[string]any{"name": "Reviews", "color": "purple", "what": "review", "owners": []string{"owner"}, "risk": "4",
		"repositories": []string{}, "query": "is!=draft", "no_bots": true, "badge": true}
}

// olderInboxFolded is olderInbox as the Inbox holds it (folded in
// app/src/inboxes.ts, the query as prfilter's shared cases fold it).
func olderInboxFolded(inbox map[string]any) map[string]any {
	inbox["what"], inbox["owners"], inbox["risk"], inbox["repositories"], inbox["no_bots"] = "any", []string{}, "any", []string{}, false
	inbox["query"] = "waiting=review org=owner risk>=4 is!=bot is!=draft"
	return inbox
}

// inboxPut is PUT /api/inboxes of the list as the Inbox holds it (every
// inbox folded) changed by edit.
func inboxPut(edit func([]map[string]any) []map[string]any) func(*proposalFixture) (string, string, string) {
	return func(f *proposalFixture) (string, string, string) {
		var state struct {
			Revision int              `json:"revision"`
			Inboxes  []map[string]any `json:"inboxes"`
		}
		_ = json.Unmarshal([]byte(f.must("GET", "/api/inboxes", "")), &state)
		for _, inbox := range state.Inboxes {
			if inbox["name"] == "Reviews" && inbox["what"] == "review" {
				olderInboxFolded(inbox)
			}
		}
		state.Inboxes = edit(state.Inboxes)
		body, _ := json.Marshal(state)
		return "PUT", "/api/inboxes", string(body)
	}
}

func proposalCases() []proposalCase {
	value := "value typed by the person"
	return []proposalCase{{
		name: "appearance", page: settingsAt("general"), tool: "propose_appearance", args: map[string]any{"theme": "light", "page_modifier": "alt"},
		route:  fixed("PUT", "/api/settings/appearance", `{"theme":"light","page_modifier":"alt"}`),
		change: fixed("PUT", "/api/settings/appearance", `{"key_hints":false}`),
	}, {
		name: "appearance without a section", page: settingsAt(""), tool: "propose_appearance", args: map[string]any{"key_hints": false},
		route:  fixed("PUT", "/api/settings/appearance", `{"key_hints":false}`),
		change: fixed("PUT", "/api/settings/appearance", `{"theme":"dark"}`),
	}, {
		name: "plan usage", page: settingsAt("general"), tool: "propose_plan_usage", args: map[string]any{"provider": "codex", "shown": false},
		route:  fixed("PUT", "/api/settings/plan-usage", `{"provider":"codex","shown":false}`),
		change: fixed("PUT", "/api/settings/plan-usage", `{"provider":"codex","shown":true}`),
	}, {
		name: "reviews", page: settingsAt("reviews"), tool: "propose_reviews", args: map[string]any{"enabled": false, "pull_requests": "all", "backend": "codex", "model": " gpt-5 ", "effort": "High"},
		route:  fixed("PUT", "/api/automatic", `{"enabled":false,"pull_requests":"all","backend":"codex","model":" gpt-5 ","effort":"High"}`),
		change: fixed("PUT", "/api/automatic", `{"model":"other"}`),
	}, {
		name: "decision capture", page: settingsAt("reviews"), tool: "propose_decision_capture", args: map[string]any{"enabled": false, "model": "haiku"},
		// The section sends its whole state.
		route:  fixed("PUT", "/api/settings/decisions", `{"enabled":false,"backend":"","model":"haiku","effort":""}`),
		change: fixed("PUT", "/api/settings/decisions", `{"enabled":true,"backend":"codex","model":"","effort":""}`),
	}, {
		name: "fixes", page: settingsAt("fixes"), tool: "propose_fixes", args: map[string]any{"backend": "codex", "model": "gpt-5", "effort": "low"},
		route:  fixed("PUT", "/api/automatic", `{"fix_backend":"codex","fix_model":"gpt-5","fix_effort":"low"}`),
		change: fixed("PUT", "/api/automatic", `{"fix_backend":"claude"}`),
	}, {
		name: "time limits", page: settingsAt("computer"), tool: "propose_time_limits", args: map[string]any{"review_time_limit_minutes": 30},
		// The section sends both limits, the other as it shows it.
		route: func(f *proposalFixture) (string, string, string) {
			var s struct {
				Run int `json:"run_time_limit_minutes"`
			}
			_ = json.Unmarshal([]byte(f.must("GET", "/api/settings", "")), &s)
			return "PUT", "/api/settings/time-limits", fmt.Sprintf(`{"review_time_limit_minutes":30,"run_time_limit_minutes":%d}`, s.Run)
		},
		change: fixed("PUT", "/api/settings/time-limits", `{"review_time_limit_minutes":20,"run_time_limit_minutes":20}`),
	}, {
		name: "job concurrency", page: settingsAt("computer"), tool: "propose_job_concurrency", args: map[string]any{"job_concurrency": 2},
		route:  fixed("PUT", "/api/settings/job-concurrency", `{"job_concurrency":2}`),
		change: fixed("PUT", "/api/settings/job-concurrency", `{"job_concurrency":3}`),
	}, {
		name: "codex approvals", page: settingsAt("computer"), tool: "propose_codex_approvals", args: map[string]any{"codex_approvals": true},
		route:  fixed("PUT", "/api/agent-requests/settings", `{"codex_approvals":true}`),
		change: fixed("PUT", "/api/agent-requests/settings", `{"codex_approvals":true}`),
	}, {
		name: "agent environment: add", page: settingsAt("computer"), tool: "propose_agent_env", args: map[string]any{"name": "NEW_VAR", "action": "add"}, input: &value,
		route:  fixed("PUT", "/api/settings/agent-env", fmt.Sprintf(`{"agent_env":{"ZED_TOKEN":%q,"NEW_VAR":%q}}`, envSentinel, value)),
		change: fixed("PUT", "/api/settings/agent-env", `{"agent_env":{"ZED_TOKEN":"changed"}}`),
	}, {
		name: "agent environment: remove", page: settingsAt("computer"), tool: "propose_agent_env", args: map[string]any{"name": "ZED_TOKEN", "action": "remove"},
		route:  fixed("PUT", "/api/settings/agent-env", `{"agent_env":{}}`),
		change: fixed("PUT", "/api/settings/agent-env", `{"agent_env":{"ZED_TOKEN":"x","OTHER":"y"}}`),
	}, {
		name: "global prompt", page: settingsAt("prompts"), tool: "propose_prompt", args: map[string]any{"id": "fix-checks", "text": "Fix the failing checks."},
		route:  fixed("PUT", "/api/settings/prompts/fix-checks", `{"text":"Fix the failing checks."}`),
		change: fixed("PUT", "/api/settings/prompts/fix-checks", `{"text":"The person's own text."}`),
	}, {
		name: "global prompt reset", page: settingsAt("prompts"), tool: "propose_prompt_reset", args: map[string]any{"id": "fix-conflicts"},
		route:  fixed("PUT", "/api/settings/prompts/fix-conflicts", `{"text":null}`),
		change: fixed("PUT", "/api/settings/prompts/fix-conflicts", `{"text":"Edited by hand."}`),
	}, {
		name: "repository prompt", page: repositoryAt("prompts"), tool: "propose_prompt", args: map[string]any{"id": "fix-checks", "text": "Repository checks text."},
		route:  fixed("PUT", "/api/repositories/owner/repo/prompts/fix-checks", `{"text":"Repository checks text."}`),
		change: fixed("PUT", "/api/repositories/owner/repo/prompts/fix-checks", `{"text":"Edited by hand."}`),
	}, {
		name: "repository prompt reset", page: repositoryAt("prompts"), tool: "propose_prompt_reset", args: map[string]any{"id": "fix-conflicts"},
		route:  fixed("PUT", "/api/repositories/owner/repo/prompts/fix-conflicts", `{"text":null}`),
		change: fixed("PUT", "/api/settings/prompts/fix-conflicts", `{"text":"The global text the repository inherits, edited."}`),
	}, {
		name: "repository settings", page: repositoryAt(""), tool: "propose_repository_settings", args: map[string]any{"automatic": false},
		// The page sends the whole settings object.
		route:  fixed("PUT", "/api/repositories/owner/repo/settings", `{"automatic":false,"decisions":null}`),
		change: fixed("PUT", "/api/repositories/owner/repo/settings", `{"automatic":null,"decisions":false}`),
	}, {
		name: "new inbox", page: func(*proposalFixture) agentcontext.PageContext {
			return agentcontext.PageContext{Page: agentcontext.PageInbox}
		},
		tool: "propose_inbox", args: map[string]any{"name": "Platform", "query": "repo=owner/repo", "color": "green"},
		route: inboxPut(func(list []map[string]any) []map[string]any {
			return append(list, map[string]any{"name": "Platform", "color": "green", "what": "any", "owners": []string{}, "risk": "any", "repositories": []string{}, "query": "repo=owner/repo", "no_bots": false, "badge": true})
		}),
		change: inboxPut(func(list []map[string]any) []map[string]any { list[0]["badge"] = false; return list }),
	}, {
		name: "rename an inbox", page: func(*proposalFixture) agentcontext.PageContext {
			return agentcontext.PageContext{Page: agentcontext.PageInbox}
		},
		tool: "propose_inbox", inbox: 1, args: map[string]any{"name": "Mine", "query": "author=@me"},
		route: inboxPut(func(list []map[string]any) []map[string]any {
			list[0]["name"], list[0]["query"] = "Mine", "author=@me"
			return list
		}),
		change: inboxPut(func(list []map[string]any) []map[string]any { list[0]["color"] = "red"; return list }),
	}, {
		name: "query of an inbox with the older fields", page: func(*proposalFixture) agentcontext.PageContext {
			return agentcontext.PageContext{Page: agentcontext.PageInbox}
		},
		// The page shows the folded query; the new one replaces all of it.
		tool: "propose_inbox", inbox: 2, args: map[string]any{"query": "author=@me"},
		route: inboxPut(func(list []map[string]any) []map[string]any {
			list[1]["query"] = "author=@me"
			return list
		}),
		change: inboxPut(func(list []map[string]any) []map[string]any { list[1]["badge"] = false; return list }),
	}, {
		name: "group", page: func(f *proposalFixture) agentcontext.PageContext {
			return agentcontext.PageContext{Page: agentcontext.PageGroup, Group: f.group, Repository: "owner/repo"}
		},
		tool: "propose_group", args: map[string]any{"name": "Checkout flow", "add": []int{3}, "remove": []int{1}},
		route: func(f *proposalFixture) (string, string, string) {
			return "PATCH", "/api/groups/" + f.group, `{"name":"Checkout flow","members":[2,3]}`
		},
		change: func(f *proposalFixture) (string, string, string) {
			return "PATCH", "/api/groups/" + f.group, `{"name":"Renamed by hand"}`
		},
	}, {
		name: "review comment", page: func(*proposalFixture) agentcontext.PageContext {
			return agentcontext.PageContext{Page: agentcontext.PagePullRequest, Repository: "owner/repo", PullRequest: 7}
		},
		tool: "propose_review_comment", args: map[string]any{"body": "Check the retry.", "target": map[string]any{"kind": "file", "id": "a.go", "title": "a.go"},
			"place": map[string]any{"path": "a.go", "line": 12, "side": "RIGHT", "start_line": 10}, "head": "abcdef1"},
		// The app appends the comment to the record it read.
		route: func(f *proposalFixture) (string, string, string) {
			var record struct {
				Revision int                        `json:"revision"`
				Value    map[string]json.RawMessage `json:"value"`
			}
			_ = json.Unmarshal([]byte(f.must("GET", "/api/ui-state/review?key="+url.QueryEscape("owner/repo#7"), "")), &record)
			var comments []json.RawMessage
			_ = json.Unmarshal(record.Value["comments"], &comments)
			comments = append(comments, json.RawMessage(`{"id":"x","target":{"kind":"file","id":"a.go","title":"a.go"},"body":"Check the retry.","place":{"path":"a.go","line":12,"side":"RIGHT","start_line":10},"head":"abcdef1","created":5}`))
			record.Value["comments"], _ = json.Marshal(comments)
			value, _ := json.Marshal(record.Value)
			return "PUT", "/api/ui-state/review?key=" + url.QueryEscape("owner/repo#7"), fmt.Sprintf(`{"revision":%d,"mutation_id":"personwindow1:2","value":%s}`, record.Revision, value)
		},
		change: func(f *proposalFixture) (string, string, string) {
			return "PUT", "/api/ui-state/review?key=" + url.QueryEscape("owner/repo#7"),
				`{"revision":1,"mutation_id":"personwindow1:3","value":{"comments":[],"note":"Rewritten.","sent":[]}}`
		},
	}}
}

// A proposal changes nothing until the person saves it; its save is the same
// change, with the same effects, as the page's own save of it.
func TestProposalSavesAsThePageDoes(t *testing.T) {
	for _, tc := range proposalCases() {
		t.Run(tc.name, func(t *testing.T) {
			proposed, saved := newProposalFixture(t), newProposalFixture(t)
			args := tc.caseArgs(proposed)
			proposed.page(tc.page(proposed))
			before, effects := proposed.state(), proposed.effects()
			p, err := proposed.propose(tc.tool, args)
			if err != nil {
				t.Fatal(err)
			}
			if proposed.state() != before || proposed.effects() != effects {
				t.Fatal("a proposal changed something before it was saved")
			}
			input := agentcontext.SaveInput{Value: tc.input}
			if err := proposed.save(p, input); err != nil {
				t.Fatal(err)
			}
			if len(proposed.mcp.Proposals.List(proposed.id)) != 0 {
				t.Error("a saved proposal is still open")
			}
			saved.must(tc.route(saved))
			if got, want := proposed.state(), saved.state(); got != want {
				t.Errorf("saved:\n%s\nthe page's own save:\n%s", got, want)
			}
			if got, want := proposed.effects(), saved.effects(); got != want {
				t.Errorf("after the save: %s; after the page's own save: %s", got, want)
			}
		})
	}
}

// A target changed between the proposal and its save, through the page's own
// route, refuses the save and nothing is written. So does a change that lands
// after Save checked the revision and before the proposer writes: the
// proposer compares again where it writes.
func TestStaleProposalsAreNotSaved(t *testing.T) {
	for _, tc := range proposalCases() {
		for _, when := range []string{"before the save", "during the save"} {
			t.Run(tc.name+" "+when, func(t *testing.T) {
				f := newProposalFixture(t)
				args := tc.caseArgs(f)
				f.page(tc.page(f))
				p, err := f.propose(tc.tool, args)
				if err != nil {
					t.Fatal(err)
				}
				var changed string
				edit := func() {
					f.must(tc.change(f))
					changed = f.state()
				}
				if when == "before the save" {
					edit()
				} else {
					hook := edit
					f.afterCurrent.Store(&hook)
				}
				if err := f.save(p, agentcontext.SaveInput{Value: tc.input}); !errors.Is(err, agentcontext.ErrStale) {
					t.Fatalf("saved over a newer change: %v", err)
				}
				if f.state() != changed {
					t.Error("a stale save wrote something")
				}
				if len(f.mcp.Proposals.List(f.id)) != 1 {
					t.Error("a refused proposal was closed")
				}
			})
		}
	}
}

// The saves that read outside their store's transaction (a saved inbox, a
// pending review) leave the comparison to the store's own revision: a change
// that lands between the read and the write refuses the save.
func TestAChangeBetweenReadAndWriteRefusesTheSave(t *testing.T) {
	for _, tc := range proposalCases() {
		if !strings.Contains(tc.name, "inbox") && tc.name != "review comment" {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			f := newProposalFixture(t)
			args := tc.caseArgs(f)
			f.page(tc.page(f))
			p, err := f.propose(tc.tool, args)
			if err != nil {
				t.Fatal(err)
			}
			var changed string
			uiserver.SetProposalsBeforeWrite(f.proposers, func() {
				uiserver.SetProposalsBeforeWrite(f.proposers, nil)
				f.must(tc.change(f))
				changed = f.state()
			})
			if err := f.save(p, agentcontext.SaveInput{}); !errors.Is(err, agentcontext.ErrStale) {
				t.Fatalf("saved over a change: %v", err)
			}
			if changed == "" || f.state() != changed {
				t.Error("the save wrote over the change")
			}
		})
	}
}

// An invalid proposal is refused with the reason the page's own route
// answers, and nothing is recorded or changed.
func TestInvalidProposalsAreRefusedWithThePagesReason(t *testing.T) {
	for _, tc := range []struct {
		name   string
		page   agentcontext.PageContext
		tool   string
		args   map[string]any
		method string
		path   string
		body   string
	}{
		{"theme", agentcontext.PageContext{Page: agentcontext.PageSettings}, "propose_appearance", map[string]any{"theme": "neon"}, "PUT", "/api/settings/appearance", `{"theme":"neon"}`},
		{"page modifier", agentcontext.PageContext{Page: agentcontext.PageSettings}, "propose_appearance", map[string]any{"page_modifier": "shift"}, "PUT", "/api/settings/appearance", `{"page_modifier":"shift"}`},
		{"plan", agentcontext.PageContext{Page: agentcontext.PageSettings}, "propose_plan_usage", map[string]any{"provider": "gemini", "shown": true}, "PUT", "/api/settings/plan-usage", `{"provider":"gemini","shown":true}`},
		{"effort", agentcontext.PageContext{Page: agentcontext.PageSettings, Section: "reviews"}, "propose_reviews", map[string]any{"effort": "extreme"}, "PUT", "/api/automatic", `{"effort":"extreme"}`},
		{"backend", agentcontext.PageContext{Page: agentcontext.PageSettings, Section: "reviews"}, "propose_reviews", map[string]any{"backend": "cursor-agent"}, "PUT", "/api/automatic", `{"backend":"cursor-agent"}`},
		{"unknown backend", agentcontext.PageContext{Page: agentcontext.PageSettings, Section: "reviews"}, "propose_reviews", map[string]any{"backend": "gpt"}, "PUT", "/api/automatic", `{"backend":"gpt"}`},
		{"pull requests", agentcontext.PageContext{Page: agentcontext.PageSettings, Section: "reviews"}, "propose_reviews", map[string]any{"pull_requests": "some"}, "PUT", "/api/automatic", `{"pull_requests":"some"}`},
		{"decision backend", agentcontext.PageContext{Page: agentcontext.PageSettings, Section: "reviews"}, "propose_decision_capture", map[string]any{"backend": "anthropic"}, "PUT", "/api/settings/decisions", `{"enabled":true,"backend":"anthropic","model":"","effort":""}`},
		{"fix model without its backend", agentcontext.PageContext{Page: agentcontext.PageSettings, Section: "fixes"}, "propose_fixes", map[string]any{"model": "gpt-5"}, "PUT", "/api/automatic", `{"fix_model":"gpt-5"}`},
		{"fix backend", agentcontext.PageContext{Page: agentcontext.PageSettings, Section: "fixes"}, "propose_fixes", map[string]any{"backend": "anthropic"}, "PUT", "/api/automatic", `{"fix_backend":"anthropic"}`},
		{"time limit", agentcontext.PageContext{Page: agentcontext.PageSettings, Section: "computer"}, "propose_time_limits", map[string]any{"review_time_limit_minutes": 999}, "PUT", "/api/settings/time-limits", `{"review_time_limit_minutes":999,"run_time_limit_minutes":20}`},
		{"concurrency", agentcontext.PageContext{Page: agentcontext.PageSettings, Section: "computer"}, "propose_job_concurrency", map[string]any{"job_concurrency": 9}, "PUT", "/api/settings/job-concurrency", `{"job_concurrency":9}`},
		{"prompt fence", agentcontext.PageContext{Page: agentcontext.PageSettings, Section: "prompts"}, "propose_prompt", map[string]any{"id": "fix-checks", "text": "```\nopen"}, "PUT", "/api/settings/prompts/fix-checks", `{"text":"` + "```" + `\nopen"}`},
		{"prompt heading", agentcontext.PageContext{Page: agentcontext.PageSettings, Section: "prompts"}, "propose_prompt", map[string]any{"id": "review-group", "text": "Review.\n\n## Submitting\n"}, "PUT", "/api/settings/prompts/review-group", `{"text":"Review.\n\n## Submitting\n"}`},
		{"inbox color", agentcontext.PageContext{Page: agentcontext.PageInbox}, "propose_inbox", map[string]any{"name": "Pink", "color": "pink"}, "", "", ""},
		{"group size", agentcontext.PageContext{Page: agentcontext.PageGroup}, "propose_group", map[string]any{"remove": []int{1}}, "PATCH", "", `{"members":[2]}`},
		{"comment side", agentcontext.PageContext{Page: agentcontext.PagePullRequest, Repository: "owner/repo", PullRequest: 7}, "propose_review_comment", map[string]any{"body": "x", "place": map[string]any{"path": "a.go", "line": 3, "side": "UP"}}, "", "", ""},
		{"comment body", agentcontext.PageContext{Page: agentcontext.PagePullRequest, Repository: "owner/repo", PullRequest: 7}, "propose_review_comment", map[string]any{"body": "   "}, "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProposalFixture(t)
			if tc.page.Page == agentcontext.PageGroup {
				tc.page.Group, tc.path = f.group, "/api/groups/"+f.group
			}
			f.page(tc.page)
			before := f.state()
			_, err := f.propose(tc.tool, tc.args)
			if err == nil {
				t.Fatal("an invalid proposal was recorded")
			}
			if len(f.mcp.Proposals.List(f.id)) != 0 || f.state() != before {
				t.Error("an invalid proposal left something behind")
			}
			if tc.path == "" {
				return
			}
			code, reason := f.do(tc.method, tc.path, tc.body)
			if code != 400 {
				t.Fatalf("the route took it: %d %s", code, reason)
			}
			var coded struct{ Message string }
			if json.Unmarshal([]byte(reason), &coded) == nil && coded.Message != "" {
				reason = coded.Message
			}
			if !strings.Contains(err.Error(), reason) {
				t.Errorf("refused with %q, the page with %q", err, reason)
			}
			if f.state() != before {
				t.Error("the refused route changed something")
			}
		})
	}
}

// A proposal is for what the page the person has open shows: another
// section, repository, group or pull request is refused when proposed, and
// when saved after the person navigated.
func TestProposalsBelongToTheOpenPage(t *testing.T) {
	ctx := context.Background()
	f := newProposalFixture(t)
	refused := func(what string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s was accepted", what)
		}
	}
	f.page(agentcontext.PageContext{Page: agentcontext.PageSettings, Section: "general"})
	_, err := f.propose("propose_reviews", map[string]any{"enabled": false})
	refused("a Reviews change on General", err)
	_, err = f.propose("propose_inbox", map[string]any{"name": "X"})
	refused("an inbox on Settings", err)
	_, err = f.propose("propose_prompt", map[string]any{"id": "fix-checks", "text": "x", "repository": "owner/repo"})
	refused("a repository's prompt on Settings", err)

	f.page(agentcontext.PageContext{Page: agentcontext.PageRepositories, Repository: "owner/repo", Section: "prompts"})
	_, err = f.propose("propose_prompt", map[string]any{"id": "fix-checks", "text": "x", "repository": "other/repo"})
	refused("another repository's prompt", err)
	_, err = f.propose("propose_repository_settings", map[string]any{"automatic": false})
	refused("a Reviews change on Prompts", err)
	_, err = f.mcp.Proposals.Propose(ctx, f.id, agentcontext.Proposal{Page: agentcontext.PageRepositories, Target: agentcontext.RepositoryTarget("other/repo", agentcontext.TargetPrompt+"fix-checks"), After: json.RawMessage(`{"text":"x"}`)})
	refused("another repository's target", err)
	f.page(agentcontext.PageContext{Page: agentcontext.PageRepositories})
	_, err = f.propose("propose_repository_settings", map[string]any{"automatic": false})
	refused("a repository change on the Repositories list", err)

	f.page(agentcontext.PageContext{Page: agentcontext.PageGroup, Group: f.group})
	_, err = f.mcp.Proposals.Propose(ctx, f.id, agentcontext.Proposal{Page: agentcontext.PageGroup, Target: agentcontext.TargetGroup + "another", After: json.RawMessage(`{"name":"x"}`)})
	refused("another group", err)
	f.page(agentcontext.PageContext{Page: agentcontext.PagePullRequest, Repository: "owner/repo", PullRequest: 7})
	for _, target := range []string{agentcontext.PullRequestTarget("owner/repo", 8), agentcontext.PullRequestTarget("other/repo", 7)} {
		_, err = f.mcp.Proposals.Propose(ctx, f.id, agentcontext.Proposal{Page: agentcontext.PagePullRequest, Target: target, After: json.RawMessage(`{"body":"x"}`)})
		refused("another pull request", err)
	}

	// Saved after the person navigated: nothing is written until they are
	// back on the page that shows the target.
	for _, tc := range []struct {
		name  string
		page  agentcontext.PageContext
		other []agentcontext.PageContext
		tool  string
		args  map[string]any
	}{
		{"settings", agentcontext.PageContext{Page: agentcontext.PageSettings, Section: "reviews"},
			[]agentcontext.PageContext{{Page: agentcontext.PageSettings, Section: "general"}, {Page: agentcontext.PageSettings}, {Page: agentcontext.PageInbox}},
			"propose_reviews", map[string]any{"enabled": false}},
		{"repository", agentcontext.PageContext{Page: agentcontext.PageRepositories, Repository: "owner/repo", Section: "reviews"},
			[]agentcontext.PageContext{{Page: agentcontext.PageRepositories, Repository: "other/repo", Section: "reviews"}, {Page: agentcontext.PageRepositories, Repository: "owner/repo", Section: "prompts"}, {Page: agentcontext.PageRepositories}},
			"propose_repository_settings", map[string]any{"decisions": false}},
		{"group", agentcontext.PageContext{Page: agentcontext.PageGroup, Group: f.group},
			[]agentcontext.PageContext{{Page: agentcontext.PageGroup, Group: "another"}, {Page: agentcontext.PageInbox}},
			"propose_group", map[string]any{"name": "Moved"}},
		{"pull request", agentcontext.PageContext{Page: agentcontext.PagePullRequest, Repository: "owner/repo", PullRequest: 7},
			[]agentcontext.PageContext{{Page: agentcontext.PagePullRequest, Repository: "owner/repo", PullRequest: 8}, {Page: agentcontext.PagePullRequest, Repository: "other/repo", PullRequest: 7}},
			"propose_review_comment", map[string]any{"body": "Later."}},
	} {
		f.page(tc.page)
		p, err := f.propose(tc.tool, tc.args)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		before := f.state()
		for _, other := range tc.other {
			f.page(other)
			if err := f.save(p, agentcontext.SaveInput{}); err == nil || f.state() != before {
				t.Errorf("%s: saved on %+v: %v", tc.name, other, err)
			}
		}
		f.page(tc.page)
		if err := f.save(p, agentcontext.SaveInput{}); err != nil || f.state() == before {
			t.Errorf("%s: not saved back on its page: %v", tc.name, err)
		}
	}
}

// The agent proposes an agent environment variable by name; the value is
// the person's, typed when saving. It is never in the proposal, the tool's
// answer or anything listed, and a save without it, or with one the page
// refuses, writes nothing.
func TestAgentEnvironmentValuesNeverPassThroughTheAgent(t *testing.T) {
	f := newProposalFixture(t)
	f.page(agentcontext.PageContext{Page: agentcontext.PageSettings, Section: "computer"})
	secret := "PERSON_TYPED_SECRET"
	noValue := func(what string) {
		t.Helper()
		for _, p := range f.mcp.Proposals.List(f.id) {
			raw, _ := json.Marshal(p)
			for _, leak := range []string{envSentinel, secret} {
				if strings.Contains(string(raw), leak) || strings.Contains(fmt.Sprintf("%+v %#v", p, p), leak) {
					t.Errorf("%s: the proposal carries %s: %s", what, leak, raw)
				}
			}
		}
	}
	text, failed := (&readFixture{mcp: f.mcp}).callTool(t, f.token, "propose_agent_env", map[string]any{"name": "NEW_VAR", "action": "add"})
	if failed || strings.Contains(text, envSentinel) {
		t.Fatalf("%v %s", failed, text)
	}
	p := f.mcp.Proposals.List(f.id)[0]
	if string(p.After) != `{"name":"NEW_VAR","needs_value":true}` || string(p.Before) != `{"name":"NEW_VAR","set":false}` {
		t.Errorf("the proposal: before %s after %s", p.Before, p.After)
	}
	noValue("proposed")
	before := f.state()
	if err := f.save(p, agentcontext.SaveInput{}); err == nil || f.state() != before {
		t.Fatalf("an addition was saved without its value: %v", err)
	}
	long := strings.Repeat("x", 5<<10) + secret
	for _, bad := range []string{long, "a\x00" + secret} {
		err := f.save(p, agentcontext.SaveInput{Value: &bad})
		if err == nil || f.state() != before {
			t.Fatalf("a value the page refuses was saved: %v", err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the refusal quotes the value: %v", err)
		}
		body, _ := json.Marshal(map[string]map[string]string{"agent_env": {"ZED_TOKEN": envSentinel, "NEW_VAR": bad}})
		_, reason := f.do("PUT", "/api/settings/agent-env", string(body))
		if !strings.Contains(err.Error(), reason) {
			t.Errorf("refused with %q, the page with %q", err, reason)
		}
	}
	noValue("after refused saves")
	if input := (agentcontext.SaveInput{Value: &secret}); strings.Contains(fmt.Sprintf("%v %+v %#v %s", input, input, input, input), secret) {
		t.Error("a save input prints its value")
	}
	if err := f.save(p, agentcontext.SaveInput{Value: &secret}); err != nil {
		t.Fatal(err)
	}
	if prefs := f.prefs(); !strings.Contains(prefs, `"NEW_VAR":"`+secret+`"`) || !strings.Contains(prefs, envSentinel) {
		t.Errorf("saved: %s", prefs)
	}

	// Adding a name that is set, or removing one that is not, is refused;
	// a removal takes no value.
	for _, args := range []map[string]any{{"name": "ZED_TOKEN", "action": "add"}, {"name": "MISSING", "action": "remove"}, {"name": "1BAD", "action": "add"}} {
		if _, err := f.propose("propose_agent_env", args); err == nil || strings.Contains(err.Error(), envSentinel) {
			t.Errorf("%v: %v", args, err)
		}
	}
	removal, err := f.propose("propose_agent_env", map[string]any{"name": "ZED_TOKEN", "action": "remove"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.save(removal, agentcontext.SaveInput{Value: &secret}); err == nil {
		t.Error("a removal took a value")
	}
	noValue("removal")
	// Only the agent environment takes a value.
	f.page(agentcontext.PageContext{Page: agentcontext.PageSettings, Section: "general"})
	theme, err := f.propose("propose_appearance", map[string]any{"theme": "dark"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.save(theme, agentcontext.SaveInput{Value: &secret}); err == nil {
		t.Error("a theme took a value")
	}
}

// An agent may not take a safety rule out of the prompt in use; a text that
// keeps them, or a prompt whose text in use already lacks one, is fine.
func TestPromptProposalsKeepTheSafetyRules(t *testing.T) {
	f := newProposalFixture(t)
	f.page(agentcontext.PageContext{Page: agentcontext.PageSettings, Section: "prompts"})
	var entry struct {
		DefaultText string `json:"default_text"`
	}
	var list struct {
		Prompts []json.RawMessage `json:"prompts"`
	}
	_ = json.Unmarshal([]byte(f.must("GET", "/api/settings/prompts", "")), &list)
	for _, raw := range list.Prompts {
		var e struct {
			ID          string `json:"id"`
			DefaultText string `json:"default_text"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.ID == "fix-threads" {
			entry.DefaultText = e.DefaultText
		}
	}
	if entry.DefaultText == "" {
		t.Fatal("no fix-threads prompt")
	}
	if _, err := f.propose("propose_prompt", map[string]any{"id": "fix-threads", "text": "Fix the review comments."}); err == nil || !strings.Contains(err.Error(), "safety rule") {
		t.Errorf("a text without the safety rules: %v", err)
	}
	if _, err := f.propose("propose_prompt", map[string]any{"id": "fix-threads", "text": entry.DefaultText + "\nKeep commits small.\n"}); err != nil {
		t.Errorf("a text that keeps them: %v", err)
	}
	if _, err := f.propose("propose_prompt_reset", map[string]any{"id": "fix-threads"}); err == nil {
		t.Error("a reset of a prompt that is not customised")
	}
	// The person took the rules out of the global text themselves; the
	// repository inherits it.
	f.must("PUT", "/api/settings/prompts/fix-threads", `{"text":"The person's own text."}`)
	if _, err := f.propose("propose_prompt", map[string]any{"id": "fix-threads", "text": "Shorter still."}); err != nil {
		t.Errorf("a rule the text in use lacks already: %v", err)
	}
	f.must("PUT", "/api/repositories/owner/repo/prompts/fix-threads", fmt.Sprintf(`{"text":%q}`, entry.DefaultText))
	f.page(agentcontext.PageContext{Page: agentcontext.PageRepositories, Repository: "owner/repo", Section: "prompts"})
	if _, err := f.propose("propose_prompt_reset", map[string]any{"id": "fix-threads"}); err == nil || !strings.Contains(err.Error(), "safety rule") {
		t.Errorf("a reset to an inherited text without the rules: %v", err)
	}
	if _, err := f.propose("propose_prompt", map[string]any{"id": "fix-checks", "text": "Repository text.", "repository": "OWNER/repo"}); err != nil {
		t.Errorf("the page's repository in another case: %v", err)
	}
}

// A proposal's answer and its errors never carry the target's current value,
// which only the person sees (Before).
func TestProposalAnswersCarryNoCurrentValue(t *testing.T) {
	f := newProposalFixture(t)
	f.page(agentcontext.PageContext{Page: agentcontext.PageSettings, Section: "prompts"})
	text, failed := (&readFixture{mcp: f.mcp}).callTool(t, f.token, "propose_prompt_reset", map[string]any{"id": "fix-conflicts"})
	if failed || strings.Contains(text, "SENTINEL") {
		t.Fatalf("%v %s", failed, text)
	}
	p := f.mcp.Proposals.List(f.id)[0]
	if !strings.Contains(string(p.Before), promptSentinel) {
		t.Errorf("the person does not see the current text: %s", p.Before)
	}
	// A guessed Before is refused without saying what the value is.
	_, err := f.mcp.Proposals.Propose(context.Background(), f.id, agentcontext.Proposal{Page: agentcontext.PageSettings, Target: agentcontext.TargetPrompt + "fix-conflicts",
		Before: json.RawMessage(`{"text":"a guess"}`), After: json.RawMessage(`{"text":null}`)})
	if !errors.Is(err, agentcontext.ErrNotRecorded) || strings.Contains(err.Error(), "SENTINEL") {
		t.Errorf("a guess: %v", err)
	}
	f.page(agentcontext.PageContext{Page: agentcontext.PageGroup, Group: f.group})
	text, failed = (&readFixture{mcp: f.mcp}).callTool(t, f.token, "propose_group", map[string]any{"remove": []int{2}})
	if !failed || strings.Contains(text, "Checkout") {
		t.Errorf("a refused group change: %s", text)
	}
}

// The person compares a saved inbox's change with the inbox as the Inbox
// shows it: an older inbox's fields folded into its query.
func TestInboxProposalsShowTheFoldedInbox(t *testing.T) {
	f := newProposalFixture(t)
	f.page(agentcontext.PageContext{Page: agentcontext.PageInbox})
	var state struct{ Inboxes []struct{ ID string } }
	_ = json.Unmarshal([]byte(f.must("GET", "/api/inboxes", "")), &state)
	p, err := f.propose("propose_inbox", map[string]any{"id": state.Inboxes[1].ID, "badge": false})
	if err != nil {
		t.Fatal(err)
	}
	var before map[string]any
	if err := json.Unmarshal(p.Before, &before); err != nil || before["query"] != "waiting=review org=owner risk>=4 is!=bot is!=draft" || before["what"] != "any" || before["no_bots"] != false {
		t.Errorf("before: %s %v", p.Before, err)
	}
}

// Removing an agent environment variable must change only that variable in
// the environment in force. With no variables of the app's own left, the
// config file's apply again, so such a removal is refused and nothing saved.
func TestAgentEnvironmentRemovalKeepsToTheName(t *testing.T) {
	for _, tc := range []struct {
		name string
		file map[string]string
	}{
		{"the file sets the name too", map[string]string{"ZED_TOKEN": "from the file", "FILE_ONLY": "x"}},
		{"the file sets others", map[string]string{"FILE_ONLY": "x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.AgentEnv = tc.file
			f := newProposalFixtureWith(t, cfg)
			f.page(agentcontext.PageContext{Page: agentcontext.PageSettings, Section: "computer"})
			before := f.state()
			_, err := f.propose("propose_agent_env", map[string]any{"name": "ZED_TOKEN", "action": "remove"})
			if err == nil || !strings.Contains(err.Error(), "config file") {
				t.Fatalf("a removal the file undoes: %v", err)
			}
			if strings.Contains(err.Error(), envSentinel) || strings.Contains(err.Error(), "from the file") || len(f.mcp.Proposals.List(f.id)) != 0 || f.state() != before {
				t.Errorf("refused with %v, leaving %d proposals", err, len(f.mcp.Proposals.List(f.id)))
			}
		})
	}
	// With another variable of the app's own left, the removal is the change
	// proposed.
	cfg := config.Default()
	cfg.AgentEnv = map[string]string{"ZED_TOKEN": "from the file"}
	f := newProposalFixtureWith(t, cfg)
	f.must("PUT", "/api/settings/agent-env", fmt.Sprintf(`{"agent_env":{"ZED_TOKEN":%q,"OTHER":"y"}}`, envSentinel))
	f.page(agentcontext.PageContext{Page: agentcontext.PageSettings, Section: "computer"})
	p, err := f.propose("propose_agent_env", map[string]any{"name": "ZED_TOKEN", "action": "remove"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.save(p, agentcontext.SaveInput{}); err != nil {
		t.Fatal(err)
	}
	if prefs := f.prefs(); strings.Contains(prefs, "ZED_TOKEN") || !strings.Contains(prefs, `"OTHER":"y"`) {
		t.Errorf("saved %s", prefs)
	}
}
