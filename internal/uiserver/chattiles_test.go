package uiserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/tilechecks"
	"github.com/baum-ai/baum/internal/tiledrafts"
	"github.com/baum-ai/baum/internal/tilelayout"
	"github.com/baum-ai/baum/internal/tilelibrary"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/kit"
	"github.com/baum-ai/baum/tiles/library"
)

// fakeChecker is the render checker without a runner: it records what it is
// sent and answers the verdict set, keeping each result as the checker does.
type fakeChecker struct {
	mu        sync.Mutex
	verdict   string
	reason    string
	sent      []tilechecks.Candidate
	kept      map[string]tilelibrary.CheckResult
	idCounter int
}

func (f *fakeChecker) Submit(_ context.Context, c tilechecks.Candidate) (tilechecks.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, c)
	f.idCounter++
	def := c.Definition
	encoded, err := library.Seal(&def)
	if err != nil {
		return tilechecks.Result{}, err
	}
	verdict := f.verdict
	if verdict == "" {
		verdict = tilechecks.VerdictPass
	}
	id := fmt.Sprintf("chk_%04d", f.idCounter)
	if f.kept == nil {
		f.kept = map[string]tilelibrary.CheckResult{}
	}
	f.kept[id] = tilelibrary.CheckResult{ID: id, Verdict: verdict, Digest: def.Digest, Definition: encoded}
	return tilechecks.Result{Check: id, Digest: def.Digest, Kit: def.Kit, Verdict: verdict, Reason: f.reason, Errors: []tilechecks.Problem{}, Warnings: []tilechecks.Problem{}}, nil
}

func (f *fakeChecker) Result(_ context.Context, id string) (tilelibrary.CheckResult, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.kept[id]
	return r, ok
}

func (f *fakeChecker) last() tilechecks.Candidate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sent[len(f.sent)-1]
}

// chatTilesFixture is a saved review of acme/widgets#42 with layouts, the
// fake checker and a chat on its tiles page.
type chatTilesFixture struct {
	*layoutFixture
	filler  *fakeFiller
	checker *fakeChecker
	drafts  *tiledrafts.Store
	chat    *ChatTiles
	mcp     *agentcontext.Server
	token   string
	id      string
	now     time.Time
	t       *testing.T
}

func newChatTilesFixture(t *testing.T) *chatTilesFixture {
	t.Helper()
	f := &chatTilesFixture{t: t, checker: &fakeChecker{}, now: time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)}
	f.drafts = &tiledrafts.Store{}
	added, filler := newAddedFixture(t)
	f.layoutFixture, f.filler = added, filler
	f.api.Drafts = f.drafts
	// The handler was built before Drafts was set: build it again.
	f.handler = f.api.Handler()
	t.Cleanup(f.handler.Close)
	f.put(t, titled(t, "First title"))
	f.chat = &ChatTiles{API: *f.api, Checks: f.checker, Drafts: f.drafts, Now: func() time.Time { return f.now }}
	f.mcp = agentcontext.New(agentcontext.Options{API: f.handler, Layouts: LayoutEditor{API: *f.api}, ChatTiles: f.chat})
	token, id, revoke, err := f.mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Chat})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(revoke)
	f.token, f.id = token, id
	f.page(`{"view":"tiles"}`)
	return f
}

// page puts the chat on the pull request's tiles page with focus.
func (f *chatTilesFixture) page(focus string) {
	f.t.Helper()
	if err := f.mcp.Registry.SetPage(f.id, agentcontext.PageContext{Page: agentcontext.PagePullRequest, Repository: "acme/widgets", PullRequest: 42, Focus: json.RawMessage(focus)}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *chatTilesFixture) call(name string, args any) (string, bool) {
	f.t.Helper()
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
	r := httptest.NewRequest("POST", "/api/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+string(params)+`}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+f.token)
	w := httptest.NewRecorder()
	f.mcp.ServeHTTP(w, r)
	var reply struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || len(reply.Result.Content) != 1 {
		f.t.Fatalf("%s: %s", name, w.Body.String())
	}
	return reply.Result.Content[0].Text, reply.Result.IsError
}

// unfenced is a tool's text between the envelope's fences.
func unfenced(text string) string {
	var inside []string
	in := false
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "BEGIN UNTRUSTED DATA"):
			in = true
		case strings.HasPrefix(line, "END UNTRUSTED DATA"):
			in = false
		case in:
			inside = append(inside, line)
		}
	}
	return strings.Join(inside, "\n")
}

// checkArgs are check_tile's arguments for the kit's starter under name,
// with data citing hunk (none without one).
func checkArgs(t *testing.T, name, fill string, data any) map[string]any {
	t.Helper()
	e, err := library.Load(kit.Starter())
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{
		"type":        map[string]any{"name": name, "title": "New environment variables", "use_when": e.Definition.UseWhen, "glance": e.Glance, "sizes": e.Definition.Sizes, "kit": e.Definition.Kit, "fill": fill},
		"data_schema": e.Definition.DataSchema,
		"html":        e.Definition.Source.HTML,
	}
	if data != nil {
		args["data"] = data
	}
	return args
}

func envData(hunk, title string) map[string]any {
	return map[string]any{"summary": "Adds one variable.", "items": []any{map[string]any{"title": title, "severity": "low", "text": nil,
		"cites": []any{map[string]any{"type": "hunk", "id": hunk}}}}}
}

func (f *chatTilesFixture) firstHunk() string {
	f.t.Helper()
	diff := library.DiffOf(f.added(f.t, "").Tiles)
	if len(diff) == 0 {
		f.t.Fatal("the review has no hunks")
	}
	return diff[0].ID
}

type checkAnswer struct {
	Check   string               `json:"check"`
	Verdict string               `json:"verdict"`
	Errors  []tilechecks.Problem `json:"errors"`
	Next    string               `json:"next"`
}

func (f *chatTilesFixture) check(args map[string]any) checkAnswer {
	f.t.Helper()
	text, failed := f.call("check_tile", args)
	if failed {
		f.t.Fatalf("check_tile: %s", text)
	}
	var out checkAnswer
	if err := json.Unmarshal([]byte(unfenced(text)), &out); err != nil {
		f.t.Fatalf("check_tile: %v %s", err, text)
	}
	return out
}

type tilesWithDrafts struct {
	Tiles  *tiles.Document    `json:"tiles"`
	Drafts []tiledrafts.Draft `json:"drafts"`
}

func (f *chatTilesFixture) served(query string) tilesWithDrafts {
	f.t.Helper()
	w := f.get("/api/tiles", query, "")
	if w.Code != 200 {
		f.t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var body tilesWithDrafts
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		f.t.Fatal(err)
	}
	return body
}

// A tile made in chat: the pending card shows on the page where the tile
// will stand, counts its check rounds, and becomes the tile when the check
// that passed with data is added: a ready custom tile of the agent's type
// and data, right after the overview, announced as "Added <title>" and
// taken back by an undo. The turn's end leaves an unfinished card behind.
func TestATileMadeInChat(t *testing.T) {
	f := newChatTilesFixture(t)
	text, failed := f.call("tile_guide", map[string]any{"part": "starter"})
	if failed || !strings.Contains(text, "tile.html") || !strings.Contains(text, "type.json") {
		t.Fatalf("tile_guide starter: %.300s", text)
	}
	if text, failed = f.call("tile_guide", nil); failed || !strings.Contains(text, "Making a type in chat") {
		t.Fatalf("tile_guide: %.200s", text)
	}
	if text, failed = f.call("tile_guide", map[string]any{"part": "manifest"}); failed || !strings.Contains(text, "kit-truncate") {
		t.Fatalf("tile_guide manifest: %.300s", text)
	}

	text, failed = f.call("start_tile", map[string]any{"title": "New environment variables", "size": "half"})
	if failed || !strings.Contains(text, `"tile":"draft_1"`) {
		t.Fatalf("start_tile: %s", text)
	}
	body := f.served("")
	if len(body.Drafts) != 1 || body.Drafts[0].Phase != tiledrafts.PhaseWriting || body.Drafts[0].To == nil || body.Drafts[0].To.After != tiles.TypeOverview {
		t.Fatalf("the pending card: %+v", body.Drafts)
	}
	if body := f.served("&version=1"); len(body.Drafts) != 0 {
		t.Errorf("an earlier version shows the card: %+v", body.Drafts)
	}

	hunk := f.firstHunk()
	// Round 1 cites a hunk the review does not have.
	answer := f.check(checkArgs(t, "env_vars", "List each new environment variable.", envData("h_000000000000", "PORT")))
	if answer.Verdict != tilechecks.VerdictFail || len(answer.Errors) != 1 || answer.Errors[0].Code != tilechecks.CodeUnresolvedCitation || answer.Errors[0].Hint == "" {
		t.Fatalf("an unresolved citation: %+v", answer)
	}
	if text, failed := f.call("add_custom_tile", map[string]any{"check": answer.Check}); !failed || !strings.Contains(text, "did not pass") {
		t.Errorf("adding a failed check: %s", text)
	}
	if d := f.served("").Drafts; len(d) != 1 || d[0].Phase != tiledrafts.PhaseChecking || d[0].Round != 1 {
		t.Errorf("after round 1: %+v", d)
	}
	// Round 2 passes.
	answer = f.check(checkArgs(t, "env_vars", "List each new environment variable.", envData(hunk, "RETRY_LIMIT, default 10")))
	if answer.Verdict != tilechecks.VerdictPass || answer.Check == "" || !strings.Contains(answer.Next, "add_custom_tile") {
		t.Fatalf("round 2: %+v", answer)
	}
	if sent := f.checker.last(); sent.Context == nil || sent.Context.Version != 1 || sent.Context.PR != 42 {
		t.Errorf("the check's context: %+v", sent.Context)
	}
	if d := f.served("").Drafts; len(d) != 1 || d[0].Round != 2 {
		t.Errorf("after round 2: %+v", d)
	}

	text, failed = f.call("add_custom_tile", map[string]any{"check": answer.Check})
	if failed || !strings.Contains(text, `"tile":"env_vars"`) || !strings.Contains(text, `"state":"ready"`) || !strings.Contains(text, "Added New environment variables") {
		t.Fatalf("add_custom_tile: %s", text)
	}
	body = f.served("")
	if len(body.Drafts) != 0 {
		t.Errorf("the card stays after the add: %+v", body.Drafts)
	}
	tile := tileByID(body.Tiles, "env_vars")
	if tile == nil || tile.Type != tiles.TypeCustom || tile.State != tiles.StateReady || !strings.Contains(string(tile.Data), "RETRY_LIMIT") || strings.Contains(string(tile.Definition), `"builtin":true`) {
		t.Fatalf("the tile: %+v", tile)
	}
	if at, entry := entryOf(body.Tiles, "env_vars"); at != 1 || entry.Size != tiles.SizeHalf {
		t.Errorf("the tile is at %d, %s", at, entry.Size)
	}
	summary := layoutOf(t, f.get("/api/tiles/layout", "", ""))
	if summary.Last == nil || summary.Last.By != "chat" || summary.Last.Summary[0] != "Added New environment variables" {
		t.Errorf("the batch: %+v", summary.Last)
	}
	if text, failed = f.call("add_custom_tile", map[string]any{"check": answer.Check}); !failed || !strings.Contains(text, "already on the review") {
		t.Errorf("adding it twice: %s", text)
	}

	// Undo takes it away.
	if text, failed = f.call("undo_tiles", nil); failed || !strings.Contains(text, "Added New environment variables") {
		t.Fatalf("undo: %s", text)
	}
	if tileByID(f.served("").Tiles, "env_vars") != nil {
		t.Error("the undone tile is still shown")
	}

	// A new card left at the turn's end says it is not finished.
	f.call("start_tile", map[string]any{"title": "Call path"})
	f.mcp.Registry.EndTurn(f.id)
	if d := f.served("").Drafts; len(d) != 1 || d[0].Phase != tiledrafts.PhaseUnfinished {
		t.Errorf("after the turn: %+v", d)
	}
	if w := f.delete("draft_2"); w.Code != 204 {
		t.Errorf("remove: %d %s", w.Code, w.Body.String())
	}
	if w := f.delete("draft_2"); w.Code != 404 {
		t.Errorf("remove again: %d", w.Code)
	}
	if d := f.served("").Drafts; len(d) != 0 {
		t.Errorf("after the remove: %+v", d)
	}
}

func (f *chatTilesFixture) delete(tile string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("DELETE", "http://local/api/tiles/drafts/"+tile+"?clone="+f.clone.ID+"&pr=42", nil)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

// The data is masked for secrets before it is checked or kept, and data that
// cites nothing of the review fails the check.
func TestATileMadeInChatIsMaskedAndCitesTheReview(t *testing.T) {
	f := newChatTilesFixture(t)
	hunk := f.firstHunk()
	// A made-up Stripe key in the provider's format, in pieces so the source
	// trips no scanner.
	secret := "sk_" + "live_" + "oHBvRPOIvGrv5iFlbCBFNOgmBjMtpsia"
	answer := f.check(checkArgs(t, "env_vars", "", envData(hunk, "TOKEN="+secret)))
	if answer.Verdict != tilechecks.VerdictPass {
		t.Fatalf("check: %+v", answer)
	}
	if sent := string(f.checker.last().Data); strings.Contains(sent, secret) {
		t.Errorf("the checker was sent the secret: %s", sent)
	}
	if text, failed := f.call("add_custom_tile", map[string]any{"check": answer.Check}); failed {
		t.Fatalf("add: %s", text)
	}
	fills, err := f.api.Layouts.Fills(context.Background(), "acme/widgets#42")
	if err != nil || len(fills) != 1 || fills[0].Origin != tilelayout.OriginChat {
		t.Fatalf("fills: %+v %v", fills, err)
	}
	if strings.Contains(string(fills[0].Data), secret) || strings.Contains(string(tileByID(f.served("").Tiles, "env_vars").Data), secret) {
		t.Errorf("the secret was kept: %s", fills[0].Data)
	}

	answer = f.check(checkArgs(t, "env_other", "", map[string]any{"summary": "Nothing changes.", "items": []any{}}))
	if answer.Verdict != tilechecks.VerdictFail || len(answer.Errors) != 1 || answer.Errors[0].Code != tilechecks.CodeUncited || answer.Errors[0].Sample != tilechecks.KindData {
		t.Errorf("data citing nothing: %+v", answer)
	}
	answer = f.check(checkArgs(t, "api_change", "", envData(hunk, "PORT")))
	if answer.Verdict != tilechecks.VerdictFail || len(answer.Errors) != 1 || answer.Errors[0].Code != tilechecks.CodeDefinitionInvalid {
		t.Errorf("a built-in type's name: %+v", answer)
	}
}

// An add is refused for a check without data, an unknown one, one older
// than an hour, one made for an earlier version, and past the review's 8
// library tiles.
func TestAddingATileMadeInChatIsRefused(t *testing.T) {
	f := newChatTilesFixture(t)
	hunk := f.firstHunk()
	noData := f.check(checkArgs(t, "env_vars", "", nil))
	if text, failed := f.call("add_custom_tile", map[string]any{"check": noData.Check}); !failed || !strings.Contains(text, "had no data") {
		t.Errorf("a check without data: %s", text)
	}
	if text, failed := f.call("add_custom_tile", map[string]any{"check": "chk_unknown"}); !failed || !strings.Contains(text, "No check chk_unknown") {
		t.Errorf("an unknown check: %s", text)
	}
	old := f.check(checkArgs(t, "env_vars", "", envData(hunk, "PORT")))
	f.now = f.now.Add(time.Hour)
	if text, failed := f.call("add_custom_tile", map[string]any{"check": old.Check}); !failed || !strings.Contains(text, "older than an hour") {
		t.Errorf("an hour-old check: %s", text)
	}
	f.now = f.now.Add(-59 * time.Minute)
	if text, failed := f.call("add_custom_tile", map[string]any{"check": old.Check}); failed {
		t.Errorf("a check of 59 minutes: %s", text)
	}
	if text, failed := f.call("undo_tiles", nil); failed {
		t.Fatal(text)
	}

	stale := f.check(checkArgs(t, "env_vars", "", envData(hunk, "PORT")))
	f.put(t, titled(t, "Second title"))
	if text, failed := f.call("add_custom_tile", map[string]any{"check": stale.Check}); !failed || !strings.Contains(text, "new version") {
		t.Errorf("a check of an earlier version: %s", text)
	}

	// Eight custom tiles fill the review's library; the ninth is refused.
	for i := range tiles.MaxLibraryTiles {
		answer := f.check(checkArgs(t, fmt.Sprintf("kind_%d", i), "", envData(hunk, "PORT")))
		if text, failed := f.call("add_custom_tile", map[string]any{"check": answer.Check}); failed {
			t.Fatalf("tile %d: %s", i, text)
		}
	}
	answer := f.check(checkArgs(t, "kind_9", "", envData(hunk, "PORT")))
	if text, failed := f.call("add_custom_tile", map[string]any{"check": answer.Check}); !failed || !strings.Contains(text, "already has 8 library tiles") {
		t.Errorf("a ninth tile: %s", text)
	}
}

// The tools work only for the pull request the turn was sent from, only on
// a page that mounts tile frames, and only on a pull request with a saved
// review; a check the app could not run fails the card with its reason.
func TestChatTilesRefusals(t *testing.T) {
	f := newChatTilesFixture(t)
	f.page(`{"view":"tiles","frames":"off"}`)
	for _, tool := range []string{"start_tile", "check_tile", "add_custom_tile"} {
		args := map[string]any{"title": "x"}
		switch tool {
		case "check_tile":
			args = checkArgs(t, "env_vars", "", nil)
		case "add_custom_tile":
			args = map[string]any{"check": "chk_0001"}
		}
		if text, failed := f.call(tool, args); !failed || !strings.Contains(text, "Custom tiles aren't available in this version of the desktop app yet") {
			t.Errorf("%s without frames: %s", tool, text)
		}
	}
	f.page(`{"view":"tiles"}`)
	if err := f.mcp.Registry.SetTurnPage(f.id, agentcontext.PageContext{Page: agentcontext.PagePullRequest, Repository: "acme/widgets", PullRequest: 7}); err != nil {
		t.Fatal(err)
	}
	if text, failed := f.call("start_tile", map[string]any{"title": "x"}); !failed || !strings.Contains(text, "started about another page") {
		t.Errorf("a turn from another pull request: %s", text)
	}
	if err := f.mcp.Registry.SetTurnPage(f.id, agentcontext.PageContext{Page: agentcontext.PagePullRequest, Repository: "acme/widgets", PullRequest: 99}); err != nil {
		t.Fatal(err)
	}
	if err := f.mcp.Registry.SetPage(f.id, agentcontext.PageContext{Page: agentcontext.PagePullRequest, Repository: "acme/widgets", PullRequest: 99}); err != nil {
		t.Fatal(err)
	}
	if text, failed := f.call("start_tile", map[string]any{"title": "x"}); !failed || !strings.Contains(text, "no saved review yet") {
		t.Errorf("no saved review: %s", text)
	}
	if err := f.mcp.Registry.SetPage(f.id, agentcontext.PageContext{Page: agentcontext.PageInbox}); err != nil {
		t.Fatal(err)
	}
	if text, failed := f.call("start_tile", map[string]any{"title": "x"}); !failed || !strings.Contains(text, "open a pull request's page first") {
		t.Errorf("off the pull request page: %s", text)
	}

	g := newChatTilesFixture(t)
	g.call("start_tile", map[string]any{"title": "Env", "to": map[string]any{"after": "nope"}})
	if d := g.served("").Drafts; len(d) != 0 {
		t.Errorf("a card placed after no tile: %+v", d)
	}
	g.call("start_tile", map[string]any{"title": "Env"})
	g.checker.verdict, g.checker.reason = tilechecks.VerdictUnavailable, "Open the baum app to check tiles"
	answer := g.check(checkArgs(t, "env_vars", "", envData(g.firstHunk(), "PORT")))
	if answer.Verdict != tilechecks.VerdictUnavailable || !strings.Contains(answer.Next, "Open the baum app") {
		t.Errorf("an unavailable check: %+v", answer)
	}
	if d := g.served("").Drafts; len(d) != 1 || d[0].Phase != tiledrafts.PhaseFailed || d[0].Reason != "Open the baum app to check tiles" {
		t.Errorf("the card after an unavailable check: %+v", d)
	}
}

// A tile made in chat on a later version: with fill instructions it is
// filled again as an added library type is (by its kept definition), showing
// its earlier data meanwhile; without them it shows its data marked as from
// the version it was made for.
func TestATileMadeInChatOnALaterVersion(t *testing.T) {
	for _, fill := range []string{"List each new environment variable with its default.", ""} {
		f := newChatTilesFixture(t)
		answer := f.check(checkArgs(t, "env_vars", fill, envData(f.firstHunk(), "PORT")))
		if text, failed := f.call("add_custom_tile", map[string]any{"check": answer.Check}); failed {
			t.Fatal(text)
		}
		f.put(t, titled(t, "Second title"))
		body := f.added(t, "")
		tile := tileByID(body.Tiles, "env_vars")
		if tile == nil || tile.State != tiles.StateReady || len(body.Added) != 1 || body.Added[0].From != 1 {
			t.Fatalf("fill %q: %+v %+v", fill, tile, body.Added)
		}
		asked := f.filler.asked()
		if fill != "" && (!body.Added[0].Filling || len(asked) != 1 || asked[0].Type != "env_vars" || asked[0].Digest == "" || asked[0].Version != 2) {
			t.Errorf("a fillable tile: %+v %+v", body.Added, asked)
		}
		if fill == "" && (body.Added[0].Filling || len(asked) != 0) {
			t.Errorf("a tile without fill instructions: %+v %+v", body.Added, asked)
		}
	}
}
