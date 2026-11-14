package agentcontext

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func preferenceServer(t *testing.T, row string) (*Server, string, string) {
	t.Helper()
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/repositories" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[` + row + `]`))
	})
	s := New(Options{API: api, Layouts: newFakeLayouts(t)})
	token, id := chat(t, s)
	return s, token, id
}

const preferencesRow = `{"repository":"acme/web","settings":{"automatic":null,"tiles":{"text":"Lead with the schema.","pins":["schema_change"]}},"clones":[]}`

// tile_preferences reads the open pull request's repository's preferences:
// empty without any, and only for that repository.
func TestTilePreferencesReadsTheOpenRepositorys(t *testing.T) {
	s, token, id := preferenceServer(t, preferencesRow)
	setPage(t, s, id, pullPage)
	text, failed := data(t, s, token, "tile_preferences", nil)
	if failed || text != `{"repository":"acme/web","text":"Lead with the schema.","pins":["schema_change"]}` {
		t.Fatalf("%v %s", failed, text)
	}
	if text, failed = data(t, s, token, "tile_preferences", map[string]any{"repository": "ACME/web"}); failed {
		t.Errorf("the page's repository by name: %s", text)
	}
	if text, failed = data(t, s, token, "tile_preferences", map[string]any{"repository": "acme/api"}); !failed || !strings.Contains(text, "tile preferences are for that repository") {
		t.Errorf("another repository: %v %s", failed, text)
	}
	none, token, id := preferenceServer(t, `{"repository":"acme/web","settings":{"automatic":null},"clones":[]}`)
	setPage(t, none, id, pullPage)
	if text, failed = data(t, none, token, "tile_preferences", nil); failed || text != `{"repository":"acme/web","text":"","pins":[]}` {
		t.Errorf("without preferences: %v %s", failed, text)
	}
	missing, token, id := preferenceServer(t, `{"repository":"acme/api","settings":{"automatic":null},"clones":[]}`)
	setPage(t, missing, id, pullPage)
	if text, failed = data(t, missing, token, "tile_preferences", nil); !failed || !strings.Contains(text, "not a repository baum watches") {
		t.Errorf("an unwatched repository: %v %s", failed, text)
	}
}

// propose_tile_preferences is the pull request page's, from a chat, and
// bound to the turn's own pull request as the layout tools are; it records a
// proposal of the fields given, and refuses a pin that is not a single tile
// type or a library type.
func TestProposeTilePreferencesIsPageBoundAndRefusesBadPins(t *testing.T) {
	rec := &takeAll{}
	s := New(Options{API: http.NotFoundHandler(), Proposers: map[string]Proposer{PagePullRequest: rec}})
	token, id := chat(t, s)
	setPage(t, s, id, PageContext{Page: PageInbox})
	good := map[string]any{"pins": []string{"schema_change"}}
	if text, failed := data(t, s, token, "propose_tile_preferences", good); !failed || !strings.Contains(text, "open a pull request's page first") {
		t.Errorf("off the pull request page: %v %s", failed, text)
	}
	setPage(t, s, id, pullPage)
	for _, tt := range []struct {
		name  string
		args  map[string]any
		after string
	}{
		{"pins", good, `{"pins":["schema_change"]}`},
		{"text and a cleared list", map[string]any{"text": "Lead with the schema.", "pins": []string{}}, `{"pins":[],"text":"Lead with the schema."}`},
		{"a cleared text", map[string]any{"text": nil}, `{"text":null}`},
		{"a library type by title and a single type", map[string]any{"pins": []string{"Schema changes", "risks"}}, `{"pins":["Schema changes","risks"]}`},
	} {
		text, failed := data(t, s, token, "propose_tile_preferences", tt.args)
		if failed {
			t.Errorf("%s: %s", tt.name, text)
			continue
		}
		list := s.Proposals.List(id)
		p := list[len(list)-1]
		if p.Page != PagePullRequest || p.Target != "acme/web:tile_preferences" || string(p.After) != tt.after {
			t.Errorf("%s recorded %s %s %s", tt.name, p.Page, p.Target, p.After)
		}
	}
	recorded := len(s.Proposals.List(id))
	for _, tt := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"a tile with many instances", map[string]any{"pins": []string{"change"}}, `"change" is not a tile type that can be pinned`},
		{"an unknown type", map[string]any{"pins": []string{"nope"}}, "one of overview, risks"},
		{"too many", map[string]any{"pins": slices.Repeat([]string{"risks"}, 9)}, "maxItems"},
		{"text over the limit", map[string]any{"text": strings.Repeat("x", 1001)}, "maxLength"},
		{"another repository", map[string]any{"text": "x", "repository": "acme/api"}, "tile preferences are for that repository"},
		{"nothing", map[string]any{}, "minProperties"},
	} {
		if text, failed := data(t, s, token, "propose_tile_preferences", tt.args); !failed || !strings.Contains(text, tt.want) {
			t.Errorf("%s: %v %s", tt.name, failed, text)
		}
	}
	if len(s.Proposals.List(id)) != recorded {
		t.Error("a refused proposal was recorded")
	}
	// A turn begun on another pull request cannot propose from this one.
	if err := s.Registry.SetTurnPage(id, PageContext{Page: PagePullRequest, Repository: "acme/web", PullRequest: 14}); err != nil {
		t.Fatal(err)
	}
	if text, failed := data(t, s, token, "propose_tile_preferences", good); !failed || !strings.Contains(text, "started about another page") {
		t.Errorf("from another turn page: %v %s", failed, text)
	}
	// An action sees neither tool.
	action, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 13})
	defer revoke()
	list := rpc(t, s, action, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if strings.Contains(string(list.Result), "tile_preferences") {
		t.Error("an action lists a tile preference tool")
	}
}

// The pull request page lists both tools; the propose tool says to edit the
// current page too.
func TestTilePreferenceToolsAreListedOnThePullRequestPage(t *testing.T) {
	s, token, id := preferenceServer(t, preferencesRow)
	setPage(t, s, id, pullPage)
	text, _ := data(t, s, token, "current_page", nil)
	var page struct {
		Tools []string `json:"tools"`
	}
	if err := json.Unmarshal([]byte(text), &page); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tile_preferences", "propose_tile_preferences"} {
		if !slices.Contains(page.Tools, name) {
			t.Errorf("the pull request page does not list %s", name)
		}
	}
	if d := toolsByName(s)["propose_tile_preferences"].Description; !strings.Contains(d, "also call edit_tiles") || !toolsByName(s)["propose_tile_preferences"].Proposes {
		t.Errorf("description %q", d)
	}
}
