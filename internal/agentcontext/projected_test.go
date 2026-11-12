package agentcontext

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The exceptions to unreadable are these tool and route pairs and no others:
// adding one means changing this list on purpose. Step by step, a tool that
// reads settings, the GitHub connection or the raw diagnostics belongs here
// only as a typed projection.
var allowedProjectedReads = map[string][]string{
	"diagnostics_summary": {"/api/diagnostics"},
	"github_repositories": {"/api/github/repositories"},
}

func TestProjectedReadsAreExactlyTheAllowedPairs(t *testing.T) {
	s := New(Options{API: http.NotFoundHandler()})
	if got := s.ToolProjected(); !reflect.DeepEqual(got, allowedProjectedReads) {
		t.Errorf("tools declare projected routes %v, allowed %v", got, allowedProjectedReads)
	}
	if !reflect.DeepEqual(projectedReads, allowedProjectedReads) {
		t.Errorf("projectedReads is %v, allowed %v", projectedReads, allowedProjectedReads)
	}
	for name, tool := range toolsByName(s) {
		for _, route := range tool.Projected {
			// An exact route, under one that is otherwise unreadable.
			if !slices.ContainsFunc(unreadable, func(prefix string) bool { return strings.HasPrefix(route, prefix) }) {
				t.Errorf("%s: %s is not an unreadable route", name, route)
			}
			// Only a global tool reads one: an action never sees it.
			if tool.Scope != ScopeGlobal {
				t.Errorf("%s is not global", name)
			}
		}
		// No tool names an unreadable route among its ordinary reads.
		for _, route := range tool.Reads {
			for _, hidden := range unreadable {
				if strings.HasPrefix(route, hidden) || strings.HasPrefix(hidden, route) {
					t.Errorf("%s reads %s, under unreadable %s", name, route, hidden)
				}
			}
		}
	}
}

func toolsByName(s *Server) map[string]Tool {
	out := map[string]Tool{}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.tools {
		out[t.Name] = t
	}
	return out
}

func TestOnlyTheNamedToolMayDeclareAProjectedRoute(t *testing.T) {
	call := func(context.Context, CallContext, json.RawMessage) (string, error) { return "", nil }
	for _, tt := range []struct {
		why  string
		tool Tool
	}{
		{"another tool's name", Tool{Name: "my_diagnostics", Scope: ScopeGlobal, Projected: []string{"/api/diagnostics"}, Call: call}},
		{"a route it was not given", Tool{Name: "diagnostics_summary", Scope: ScopeGlobal, Projected: []string{"/api/settings"}, Call: call}},
		{"a prefix of its route", Tool{Name: "diagnostics_summary", Scope: ScopeGlobal, Projected: []string{"/api/diagnostics/x"}, Call: call}},
		{"settings", Tool{Name: "settings_all", Scope: ScopeGlobal, Projected: []string{"/api/settings"}, Call: call}},
		{"a repository scope", Tool{Name: "diagnostics_summary", Scope: ScopeRepository, Projected: []string{"/api/diagnostics"}, Call: call}},
		{"its route among its reads", Tool{Name: "diagnostics_summary", Scope: ScopeGlobal, Reads: []string{"/api/diagnostics"}, Call: call}},
		{"plan usage among reads", Tool{Name: "plan_usage", Scope: ScopeGlobal, Reads: []string{"/api/plan-usage"}, Call: call}},
		{"github among reads", Tool{Name: "github_repositories", Scope: ScopeGlobal, Reads: []string{"/api/github/repositories"}, Call: call}},
		{"the connection among reads", Tool{Name: "github_repositories", Scope: ScopeGlobal, Reads: []string{"/api/github-auth"}, Call: call}},
	} {
		if err := tt.tool.compile(); err == nil {
			t.Errorf("%s: the tool compiled", tt.why)
		}
	}
	ok := Tool{Name: "diagnostics_summary", Scope: ScopeGlobal, Projected: []string{"/api/diagnostics"}, Call: call}
	if err := ok.compile(); err != nil {
		t.Errorf("the allowed pair: %v", err)
	}
}

func TestProjectedRoutesAreReadByTheirToolInProcessOnly(t *testing.T) {
	var seen []string
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	reader := NewReader(api, nil)
	tool := Tool{Name: "diagnostics_summary", Scope: ScopeGlobal, Projected: []string{"/api/diagnostics"}}
	get := func(r *Reader, target string) error {
		_, err := r.Get(context.Background(), target)
		return err
	}
	chat := reader.forTool(tool, "", 0)
	if err := get(chat, "/api/diagnostics"); err != nil {
		t.Fatalf("its route: %v", err)
	}
	for _, target := range []string{
		"/api/diagnostics/x", "/api/diagnostics?refresh=1", "/api/settings", "/api/settings/time-limits", "/api/plan-usage",
		"/api/github/repositories", "/api/github-auth", "/api/debug/stats", "/api/events", "/api/mcp",
	} {
		if err := get(chat, target); err == nil {
			t.Errorf("%s was read through the projected route's tool", target)
		}
	}
	// An action's reader refuses the route whatever its tool declares.
	action := reader.forTool(tool, "acme/web", 7)
	if err := get(action, "/api/diagnostics"); err == nil {
		t.Error("an action read a projected route")
	}
	// A tool that declares no projected route has none.
	if err := get(reader.forTool(Tool{Name: "x", Reads: []string{"/api/jobs"}}, "", 0), "/api/diagnostics"); err == nil {
		t.Error("a tool without the route read it")
	}
	if !reflect.DeepEqual(seen, []string{"/api/diagnostics"}) {
		t.Errorf("the daemon saw %v", seen)
	}
}

// What a page offers: every page lists the common tools and the app's pages
// each list tools of their own.
func TestEveryPageListsTheCommonToolsAndItsOwn(t *testing.T) {
	s := New(Options{API: http.NotFoundHandler()})
	for name := range pageNames {
		spec, ok := s.page(name)
		if !ok {
			t.Errorf("page %s has no spec", name)
			continue
		}
		for _, tool := range commonToolNames {
			if !slices.Contains(spec.Tools, tool) {
				t.Errorf("page %s lacks the common tool %s", name, tool)
			}
		}
		for _, tool := range spec.Tools {
			if _, ok := toolsByName(s)[tool]; !ok {
				t.Errorf("page %s lists %s, which is no tool", name, tool)
			}
		}
	}
}

// A page the app has needs a page context here with at least one tool of its
// own, so a chat on it has more than the common tools to read. The pages and
// the pages that show one thing are read from the app's source.
func TestEveryAppPageHasItsOwnTools(t *testing.T) {
	pages, err := os.ReadFile("../../app/src/pages.ts")
	if err != nil {
		t.Fatal(err)
	}
	app, err := os.ReadFile("../../app/src/app.tsx")
	if err != nil {
		t.Fatal(err)
	}
	filter, err := os.ReadFile("../../app/src/timeline-filter.ts")
	if err != nil {
		t.Fatal(err)
	}
	sources := slices.Concat(app, filter)
	want := map[string]string{}
	for _, m := range regexp.MustCompile(`\bpage:\s*"([a-z]+)"`).FindAllStringSubmatch(string(pages), -1) {
		want[m[1]] = "the sidebar page " + m[1]
	}
	if len(want) < 8 {
		t.Fatalf("only %d sidebar pages found in pages.ts: the scan is broken", len(want))
	}
	// The pages that show one thing, by the address parameter that opens
	// each: a pull request (pr, with its repository), a review group, a
	// session, a job and a feature. A repository's page is the Repositories
	// page with a repository open.
	for param, page := range map[string]string{"pr": PagePullRequest, "group": PageGroup, "session": PageSession, "job": PageJob, "feature": PageFeature, "repository": PageRepositories} {
		// A feature is opened in the Timeline, whose filter code names it.
		if !regexp.MustCompile(`\.(get|delete)\("` + param + `"\)`).Match(sources) {
			t.Errorf("the app no longer reads the %q address parameter: update this list", param)
		}
		want[page] = "the page opened by " + param
	}
	s := New(Options{API: http.NotFoundHandler()})
	for page, why := range want {
		spec, ok := s.page(page)
		if !ok {
			t.Errorf("%s (%s) has no page registered in agentcontext", page, why)
			continue
		}
		own := slices.DeleteFunc(slices.Clone(spec.Tools), func(tool string) bool { return slices.Contains(commonToolNames, tool) })
		if len(own) == 0 {
			t.Errorf("%s (%s) lists only the common tools", page, why)
		}
	}
	// Every page a consumer can be on is one the app has.
	for page := range pageNames {
		if _, ok := want[page]; !ok {
			t.Errorf("agentcontext has a page %s the app does not", page)
		}
	}
}
