package generate_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/reviewv2"
	"github.com/baum-ai/baum/sessions"
)

type fakeMCP struct {
	mu      sync.Mutex
	minted  []string
	revoked int
}

func (m *fakeMCP) Attach(repository string, pr int) (*agent.MCPServer, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.minted = append(m.minted, fmt.Sprintf("%s#%d", repository, pr))
	return &agent.MCPServer{URL: "http://127.0.0.1:4242/api/mcp", Token: "token"}, func() { m.mu.Lock(); m.revoked++; m.mu.Unlock() }, nil
}

// v2Replies answers each level's call: one logical section with every hunk
// and no tiles.
func v2Replies(t *testing.T) func(context.Context, agent.Request, int) (agent.Result, error) {
	return func(_ context.Context, r agent.Request, _ int) (agent.Result, error) {
		var reply string
		raw, err := os.ReadFile(filepath.Join(r.Directory, "bundle.json"))
		if err != nil {
			return agent.Result{}, err
		}
		var b struct {
			Diff []struct {
				ID string `json:"id"`
			} `json:"diff"`
		}
		if err := json.Unmarshal(raw, &b); err != nil {
			return agent.Result{}, err
		}
		var ids []string
		for _, h := range b.Diff {
			ids = append(ids, `"`+h.ID+`"`)
		}
		cite := `[{"type":"hunk","id":` + ids[len(ids)-1] + `,"lines":null}]`
		switch r.Stage {
		case "map":
			reply = `{"change_kind":"feature","sections":[{"id":"sec_all","title":"The whole change","kind":"logical","hunks":[` + strings.Join(ids, ",") +
				`],"gist":"It changes everything at once.","scope":null,"level":2,"level_reasons":[{"text":"It is small.","cites":[]}]}],"tiles":[]}`
		case "section-sec-all":
			reply = `{"summary":"It changes the code.","steps":[{"title":"First","text":"It does one thing.","cites":` + cite + `},{"title":"Then","text":"It does another.","cites":` + cite + `}],"risks":[],"tiles":[],"propose":null}`
		case "context":
			reply = `{"area":{"text":"The code.","files":[]},"reached_by":{"text":"","files":[]},"contracts":{"text":"","files":[]},"related":{"text":"","files":[]}}`
		case "overview":
			reply = `{"summary":"It changes the code. That is all.","verdict":{"decision":"approve","text":"Merge it.","points":[]},"risk_why":"It is small.","screenshots":[],"diagram":null,"intent":[]}`
		default:
			t.Errorf("an unexpected call: %s", r.Stage)
		}
		return agent.Result{Document: []byte(reply), Model: "stub-model", Calls: 1, Tokens: 100}, nil
	}
}

// With V2 a pull request's review is written in levels, in a git worktree
// of the clone at its head that is removed afterwards, with baum's MCP
// server, and its document is a baum.review/2 one.
func TestV2RunWritesLevelsInAWorktree(t *testing.T) {
	f := newFixture(t)
	f.backend.RunFunc = v2Replies(t)
	o := f.options(t)
	o.Budgets.MaxCalls = 0
	mcp := &fakeMCP{}
	var steps []string
	var drafts [][]byte
	var mu sync.Mutex
	o.V2 = &generate.V2{Clone: f.repo.Dir, MCP: mcp.Attach, Step: func(s string) { steps = append(steps, s) }}
	o.OnDraft = func(d generate.Draft) { mu.Lock(); drafts = append(drafts, d.Document); mu.Unlock() }
	result, err := generate.Run(context.Background(), o)
	if err != nil {
		t.Fatalf("%v\n%s", err, f.progress.String())
	}
	var doc reviewv2.Document
	if err := json.Unmarshal(result.Document, &doc); err != nil || doc.Schema != reviewv2.Name || result.Metadata.Schema != reviewv2.Name {
		t.Fatalf("document: %v %q %q", err, doc.Schema, result.Metadata.Schema)
	}
	if len(doc.Sections) != 1 || doc.Sections[0].Summary != "It changes the code." || doc.Summary.Text == "" || doc.Context == nil {
		t.Fatalf("section %+v, lede %q, context %v", doc.Sections, doc.Summary.Text, doc.Context != nil)
	}
	if result.Metadata.Version != 1 || result.Metadata.PromptVersion == "" || result.V2 == nil || result.V2.Levels["map"].Calls != 1 {
		t.Errorf("metadata %+v, outcome %+v", result.Metadata, result.V2)
	}
	for _, r := range f.backend.Requests {
		if filepath.Base(r.Directory) != "repo" || r.MCP == nil || r.MCP.Token != "token" {
			t.Errorf("%s ran in %s with MCP %v", r.Stage, r.Directory, r.MCP)
		}
		if !strings.HasPrefix(r.Prompt, sessions.GenerationRunMarker) {
			t.Errorf("%s's prompt lacks the marker", r.Stage)
		}
		if !strings.Contains(r.Prompt, "git worktree") || !strings.Contains(r.Prompt, "`pull_request`") {
			t.Errorf("%s's prompt does not name the worktree and the tools", r.Stage)
		}
	}
	if len(mcp.minted) != 1 || mcp.revoked != 1 {
		t.Errorf("minted %v, revoked %d", mcp.minted, mcp.revoked)
	}
	if out, _ := exec.Command("git", "-C", f.repo.Dir, "worktree", "list").Output(); strings.Count(string(out), "\n") != 1 {
		t.Errorf("the run's worktree was not removed:\n%s", out)
	}
	if left := f.leftovers(t); len(left) > 0 {
		t.Errorf("left behind: %v", left)
	}
	if len(steps) == 0 || steps[0] != "Mapping the pull request" || steps[len(steps)-1] != "Writing the overview" {
		t.Errorf("steps: %v", steps)
	}
	if len(drafts) == 0 || !strings.Contains(string(drafts[len(drafts)-1]), reviewv2.Name) {
		t.Error("no baum.review/2 draft")
	}
}

// A map that fails fails the run, as an outline does.
func TestV2RunFailsWithItsMap(t *testing.T) {
	f := newFixture(t)
	f.backend.RunFunc = func(context.Context, agent.Request, int) (agent.Result, error) {
		return agent.Result{Document: []byte(`{"change_kind":"feature","sections":[],"tiles":[]}`), Calls: 1}, nil
	}
	o := f.options(t)
	o.V2 = &generate.V2{}
	_, err := generate.Run(context.Background(), o)
	if generate.CategoryOf(err) != generate.CategoryValidation {
		t.Fatalf("err = %v (%s)", err, generate.CategoryOf(err))
	}
}
