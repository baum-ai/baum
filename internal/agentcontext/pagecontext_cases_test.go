package agentcontext

import (
	"encoding/json"
	"os"
	"testing"
)

// Every page context the app derives from its address
// (testdata/page-contexts.json, which oss/app/src/page-context.test.ts reads
// too) is one the MCP server takes: SetPage accepts it, and link builds its
// page's address from it.
func TestAppPageContextsAreValid(t *testing.T) {
	raw, err := os.ReadFile("testdata/page-contexts.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases []struct {
			Query   string      `json:"query"`
			Context PageContext `json:"context"`
		} `json:"cases"`
		Picked []struct {
			Context PageContext `json:"context"`
			Tile    string      `json:"tile"`
		} `json:"picked"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range file.Cases {
		p := c.Context
		if err := p.Validate(); err != nil {
			t.Errorf("%q: %+v refused: %v", c.Query, p, err)
			continue
		}
		if _, err := linkOf(p.Page, p.Repository, p.PullRequest, p.Group, p.Session, p.Job, p.Feature, p.Section, "", p.Filters); err != nil {
			t.Errorf("%q: %+v has no link: %v", c.Query, p, err)
		}
	}
	// A tile picked by an Ask, or open in its detail, is the tile the layout
	// tools take "this" to mean.
	if len(file.Picked) == 0 {
		t.Fatal("no picked cases")
	}
	for _, c := range file.Picked {
		if err := c.Context.Validate(); err != nil {
			t.Errorf("%+v refused: %v", c.Context, err)
		}
		if got := focusOf(CallContext{Page: c.Context, HasPage: true}); got != c.Tile {
			t.Errorf("%+v: the focused tile is %q, want %q", c.Context, got, c.Tile)
		}
	}
}
