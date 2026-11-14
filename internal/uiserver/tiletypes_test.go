package uiserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/tilelibrary"
	"github.com/baum-ai/baum/internal/uiserver"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// fakeChecks is the render checker's lookup, with results the test wrote.
type fakeChecks map[string]tilelibrary.CheckResult

func (f fakeChecks) Result(_ context.Context, id string) (tilelibrary.CheckResult, bool) {
	r, ok := f[id]
	return r, ok
}

// typeChange is a type written as the built-in schema_change type is, under
// another name, with its synthetic samples.
func typeChange(t testing.TB, name, note string) tilelibrary.Change {
	t.Helper()
	entry, _ := library.Get("schema_change")
	def := entry.Definition
	def.Name, def.Title, def.Fill = name, "Saved "+name, def.Fill+note
	encoded, err := library.Seal(&def)
	if err != nil {
		t.Fatal(err)
	}
	c := tilelibrary.Change{Definition: encoded, Glance: entry.Glance, ReviewChecks: entry.ReviewChecks, AppliesWith: entry.AppliesWith, Origin: tilelibrary.OriginChat}
	for _, s := range entry.Samples {
		if tilelibrary.IsSynthetic(s) {
			c.Samples = append(c.Samples, s)
		}
	}
	return c
}

func passedCheck(t testing.TB, c tilelibrary.Change) tilelibrary.CheckResult {
	t.Helper()
	var def tiles.TypeDefinition
	if err := json.Unmarshal(c.Definition, &def); err != nil {
		t.Fatal(err)
	}
	r := tilelibrary.CheckResult{ID: "chk_ok", Verdict: tilelibrary.VerdictPass, Digest: def.Digest, Definition: c.Definition}
	for _, s := range c.Samples {
		r.Samples = append(r.Samples, tilelibrary.CheckedSample{Sample: s, Passed: true, Synthetic: true})
	}
	return r
}

type tileTypeFixture struct {
	store  *tilelibrary.Store
	checks fakeChecks
	mcp    *agentcontext.Server
	token  string
	id     string
	read   *readFixture
}

func newTileTypeFixture(t *testing.T) *tileTypeFixture {
	t.Helper()
	f := &tileTypeFixture{store: &tilelibrary.Store{Path: filepath.Join(appdbtest.TempDir(t), "app.sqlite")}, checks: fakeChecks{}}
	proposers := &uiserver.Proposers{TileTypes: f.store}
	f.mcp = agentcontext.New(agentcontext.Options{API: http.NotFoundHandler(), Version: "test", Proposers: proposers.Pages(),
		TileTypes: uiserver.TileTypes{Store: f.store, Checks: f.checks}})
	token, id, revoke, err := f.mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Chat})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(revoke)
	f.token, f.id, f.read = token, id, &readFixture{mcp: f.mcp}
	return f
}

func (f *tileTypeFixture) page(t *testing.T, p agentcontext.PageContext) {
	t.Helper()
	if err := f.mcp.Registry.SetPage(f.id, p); err != nil {
		t.Fatal(err)
	}
}

func (f *tileTypeFixture) call(t *testing.T, tool string, args any) (string, bool) {
	t.Helper()
	return f.read.callTool(t, f.token, tool, args)
}

var (
	prPage       = agentcontext.PageContext{Page: agentcontext.PagePullRequest, Repository: "acme/web", PullRequest: 13}
	tileSettings = agentcontext.PageContext{Page: agentcontext.PageSettings, Section: agentcontext.SectionTiles}
)

func (f *tileTypeFixture) proposal(t *testing.T) agentcontext.Proposal {
	t.Helper()
	list := f.mcp.Proposals.List(f.id)
	if len(list) == 0 {
		t.Fatal("no proposal")
	}
	return list[len(list)-1]
}

// A chat on the pull request page proposes a type from a render check, the
// person saves it, and it is in the library; the agent learns a receipt only.
func TestProposeTileTypeAndSave(t *testing.T) {
	f := newTileTypeFixture(t)
	f.page(t, prPage)
	c := typeChange(t, "env_vars", "")
	f.checks["chk_ok"] = passedCheck(t, c)

	text, failed := f.call(t, "propose_tile_type", map[string]any{"from": map[string]any{"tile": "env_vars"}, "check": "chk_ok",
		"glance": "New variables with their defaults.", "paths": []string{"**/.env*"}})
	if failed {
		t.Fatal(text)
	}
	var receipt map[string]string
	if err := json.Unmarshal([]byte(text), &receipt); err != nil || receipt["target"] != "tile_type:env_vars" || receipt["page"] != "pull_request" {
		t.Fatalf("receipt: %s %v", text, err)
	}
	if len(receipt) != 5 {
		t.Errorf("the answer is more than a receipt: %s", text)
	}
	if list, _ := f.store.List(context.Background(), true); len(list) != 0 {
		t.Fatal("a proposal saved a type")
	}
	p := f.proposal(t)
	if len(p.Before) != 0 && string(p.Before) != "null" {
		t.Errorf("a new type has a current value: %s", p.Before)
	}
	if err := f.mcp.Proposals.Save(context.Background(), f.id, p.ID, agentcontext.SaveInput{}); err != nil {
		t.Fatal(err)
	}
	saved, err := f.store.Get(context.Background(), "env_vars")
	if err != nil || saved.Entry.Glance != "New variables with their defaults." || saved.Paths[0] != "**/.env*" || saved.Origin != "chat" {
		t.Fatalf("saved: %+v %v", saved.Paths, err)
	}
	if problems := library.Check(saved.Entry); len(problems) > 0 {
		t.Errorf("library.Check: %v", problems)
	}

	// An edit: the proposal's Before is the type now, for the person only.
	edit := typeChange(t, "env_vars", " Also list removed variables.")
	f.checks["chk_ok"] = passedCheck(t, edit)
	if text, failed := f.call(t, "propose_tile_type", map[string]any{"from": map[string]any{"name": "env_vars"}, "check": "chk_ok"}); failed {
		t.Fatal(text)
	}
	p = f.proposal(t)
	var before struct{ Name, Glance string }
	if err := json.Unmarshal(p.Before, &before); err != nil || before.Name != "env_vars" {
		t.Errorf("before: %s %v", p.Before, err)
	}
	if err := f.mcp.Proposals.Save(context.Background(), f.id, p.ID, agentcontext.SaveInput{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.store.Get(context.Background(), "env_vars"); len(got.Versions) != 2 || !strings.Contains(got.Entry.Definition.Fill, "removed variables") {
		t.Errorf("after the edit: %d versions", len(got.Versions))
	}
}

// A proposal read before the type changed is stale when the person saves.
func TestTileTypeProposalGoesStale(t *testing.T) {
	f := newTileTypeFixture(t)
	f.page(t, prPage)
	ctx := context.Background()
	if _, err := f.store.Create(ctx, typeChange(t, "env_vars", ""), nil); err != nil {
		t.Fatal(err)
	}
	edit := typeChange(t, "env_vars", " Agent's version.")
	f.checks["chk_ok"] = passedCheck(t, edit)
	if text, failed := f.call(t, "propose_tile_type", map[string]any{"from": map[string]any{"name": "env_vars"}, "check": "chk_ok"}); failed {
		t.Fatal(text)
	}
	p := f.proposal(t)
	// The person edits it meanwhile.
	if _, err := f.store.Update(ctx, typeChange(t, "env_vars", " The person's."), nil); err != nil {
		t.Fatal(err)
	}
	if err := f.mcp.Proposals.Save(ctx, f.id, p.ID, agentcontext.SaveInput{}); !errors.Is(err, agentcontext.ErrStale) {
		t.Fatalf("save: %v", err)
	}
	if got, _ := f.store.Get(ctx, "env_vars"); strings.Contains(got.Entry.Definition.Fill, "Agent's") || len(got.Versions) != 2 {
		t.Error("a stale proposal was saved")
	}

	// A new type whose name was taken meanwhile is stale too.
	f.checks["chk_ok"] = passedCheck(t, typeChange(t, "fresh", ""))
	if text, failed := f.call(t, "propose_tile_type", map[string]any{"from": map[string]any{"tile": "x"}, "check": "chk_ok", "glance": "A glance."}); failed {
		t.Fatal(text)
	}
	p = f.proposal(t)
	if _, err := f.store.Create(ctx, typeChange(t, "fresh", " taken"), nil); err != nil {
		t.Fatal(err)
	}
	if err := f.mcp.Proposals.Save(ctx, f.id, p.ID, agentcontext.SaveInput{}); !errors.Is(err, agentcontext.ErrStale) {
		t.Fatalf("save of a taken name: %v", err)
	}
	// And so is one for a type deleted meanwhile.
	if err := f.store.Delete(ctx, "env_vars"); err != nil {
		t.Fatal(err)
	}
}

func TestTileTypeToolRefusals(t *testing.T) {
	f := newTileTypeFixture(t)
	c := typeChange(t, "env_vars", "")
	f.checks["chk_ok"] = passedCheck(t, c)
	failed := passedCheck(t, c)
	failed.Verdict = "fail"
	f.checks["chk_fail"] = failed
	args := func(from map[string]any, extra map[string]any) map[string]any {
		m := map[string]any{"from": from, "check": "chk_ok", "glance": "A glance."}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	tile := map[string]any{"tile": "env_vars"}

	for _, tool := range []string{"propose_tile_type", "tile_type"} {
		a := args(tile, nil)
		if tool == "tile_type" {
			a = map[string]any{"type": "schema_change"}
		}
		// No page open.
		if text, bad := f.call(t, tool, a); !bad || !strings.Contains(text, "page") {
			t.Errorf("%s with no page: %v %s", tool, bad, text)
		}
		for _, p := range []agentcontext.PageContext{{Page: agentcontext.PageInbox}, {Page: agentcontext.PageSettings, Section: "general"}, {Page: agentcontext.PageSettings}, {Page: agentcontext.PageRepositories}} {
			f.page(t, p)
			if text, bad := f.call(t, tool, a); !bad || !strings.Contains(text, "Tile library") {
				t.Errorf("%s on %s/%s: %v %s", tool, p.Page, p.Section, bad, text)
			}
		}
	}

	f.page(t, prPage)
	for name, a := range map[string]map[string]any{
		"a failed check":       args(tile, map[string]any{"check": "chk_fail"}),
		"an unknown check":     args(tile, map[string]any{"check": "chk_nope"}),
		"a built-in name":      args(tile, map[string]any{"name": "schema_change"}),
		"a built-in edited":    args(map[string]any{"name": "schema_change"}, map[string]any{"name": "schema_change"}),
		"a reserved prefix":    args(tile, map[string]any{"name": "table_env"}),
		"an unknown source":    args(map[string]any{"name": "nothing"}, nil),
		"no source":            {"check": "chk_ok", "from": map[string]any{}},
		"two sources":          args(map[string]any{"tile": "a", "name": "b"}, nil),
		"a malformed check id": args(tile, map[string]any{"check": "not-a-check"}),
		"an unknown argument":  args(tile, map[string]any{"html": "<b>"}),
	} {
		text, bad := f.call(t, "propose_tile_type", a)
		if !bad {
			t.Errorf("%s: proposed: %s", name, text)
		}
	}
	if n := len(f.mcp.Proposals.List(f.id)); n != 0 {
		t.Errorf("%d proposals after only refusals", n)
	}
	// A tile is on a pull request's page: on Settings only types start one.
	f.page(t, tileSettings)
	if text, bad := f.call(t, "propose_tile_type", args(tile, nil)); !bad || !strings.Contains(text, "pull request") {
		t.Errorf("a tile on Settings: %v %s", bad, text)
	}
	// An action never sees the tools.
	token, _, revoke, err := f.mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Action, Repository: "acme/web", PullRequest: 13})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	r := httptest.NewRequest("POST", "/api/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"tile_type","arguments":{"type":"schema_change"}}}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	f.mcp.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "unknown tool") {
		t.Errorf("an action read a type: %s", w.Body.String())
	}
}

// On Settings' Tile library section the tools work without a pull request.
func TestTileTypeToolsOnTheLibraryPage(t *testing.T) {
	f := newTileTypeFixture(t)
	f.page(t, tileSettings)
	ctx := context.Background()
	if _, err := f.store.Create(ctx, typeChange(t, "env_vars", ""), nil); err != nil {
		t.Fatal(err)
	}
	edit := typeChange(t, "env_vars", " More.")
	f.checks["chk_ok"] = passedCheck(t, edit)
	if text, bad := f.call(t, "propose_tile_type", map[string]any{"from": map[string]any{"name": "env_vars"}, "check": "chk_ok", "title": "Environment variables"}); bad {
		t.Fatal(text)
	}
	p := f.proposal(t)
	if p.Page != agentcontext.PageSettings || p.Target != "tile_type:env_vars" {
		t.Errorf("proposal: %s %s", p.Page, p.Target)
	}
	if err := f.mcp.Proposals.Save(ctx, f.id, p.ID, agentcontext.SaveInput{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.store.Get(ctx, "env_vars"); got.Entry.Definition.Title != "Environment variables" {
		t.Errorf("title %q", got.Entry.Definition.Title)
	}
	// The person moved to another section: the proposal is not theirs to save.
	if text, bad := f.call(t, "propose_tile_type", map[string]any{"from": map[string]any{"name": "env_vars"}, "check": "chk_ok", "title": "Again"}); bad {
		t.Fatal(text)
	}
	p = f.proposal(t)
	f.page(t, agentcontext.PageContext{Page: agentcontext.PageSettings, Section: "general"})
	if err := f.mcp.Proposals.Save(ctx, f.id, p.ID, agentcontext.SaveInput{}); err == nil {
		t.Error("a tile type was saved from another section")
	}
}

// tile_type is the one way an agent reads a type's source, built-in or
// saved.
func TestTileTypeReadsTheSource(t *testing.T) {
	f := newTileTypeFixture(t)
	f.page(t, prPage)
	if _, err := f.store.Create(context.Background(), typeChange(t, "env_vars", ""), nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"env_vars", "api_change"} {
		text, bad := f.call(t, "tile_type", map[string]any{"type": name})
		if bad {
			t.Fatal(text)
		}
		var got struct {
			Name       string
			Builtin    bool
			Definition struct {
				Source struct{ HTML string }
				Fill   string
			}
			Glance   string
			Samples  []json.RawMessage
			Versions []tilelibrary.Version
		}
		if err := json.Unmarshal([]byte(text), &got); err != nil {
			t.Fatalf("%s: %v %.200s", name, err, text)
		}
		if got.Name != name || got.Builtin != (name == "api_change") || !strings.Contains(got.Definition.Source.HTML, "<script") ||
			got.Definition.Fill == "" || got.Glance == "" || len(got.Samples) == 0 || len(got.Versions) != 1 {
			t.Errorf("%s: %+v", name, got)
		}
	}
	if text, bad := f.call(t, "tile_type", map[string]any{"type": "nothing_here"}); !bad || !strings.Contains(text, "no tile type") {
		t.Errorf("an unknown type: %v %s", bad, text)
	}
	// A deleted type is gone for the agent too.
	if err := f.store.Delete(context.Background(), "env_vars"); err != nil {
		t.Fatal(err)
	}
	if _, bad := f.call(t, "tile_type", map[string]any{"type": "env_vars"}); !bad {
		t.Error("a deleted type was read")
	}
}

// Without a render checker wired, nothing is proposed.
func TestProposeNeedsTheRenderChecker(t *testing.T) {
	store := &tilelibrary.Store{Path: filepath.Join(appdbtest.TempDir(t), "app.sqlite")}
	mcp := agentcontext.New(agentcontext.Options{API: http.NotFoundHandler(), Proposers: (&uiserver.Proposers{TileTypes: store}).Pages(), TileTypes: uiserver.TileTypes{Store: store}})
	token, id, revoke, err := mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Chat})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	if err := mcp.Registry.SetPage(id, prPage); err != nil {
		t.Fatal(err)
	}
	if text, bad := (&readFixture{mcp: mcp}).callTool(t, token, "propose_tile_type", map[string]any{"from": map[string]any{"tile": "x"}, "check": "chk_ok"}); !bad || !strings.Contains(text, "not available") {
		t.Errorf("%v %s", bad, text)
	}
}
