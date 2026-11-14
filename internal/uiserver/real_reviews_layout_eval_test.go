package uiserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/claude"
	"github.com/baum-ai/baum/agent/codex"
	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/tilelayout"
	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/internal/uiserver"
	"github.com/baum-ai/baum/pricing"
	"github.com/baum-ai/baum/tiles"
)

// TestLayoutRequestsWithRealAgents asks the session panel's own agent (the
// person's signed-in Claude Code or Codex, as the panel runs it, with baum's
// MCP server) to lay out the tiles of real saved reviews of a private
// dataset's calibration split, as people would ask, and scores whether the
// page ends as asked and the agent said so. It runs on the host, where the
// CLI is signed in (build it with go test -c in the oss-test container, as
// oss/cmd/replay is built), and only when asked:
//
//	BAUM_APP_REAL_REVIEWS     the dataset directory
//	BAUM_LAYOUT_EVAL_AGENT    claude or codex
//	BAUM_LAYOUT_EVAL_COUNT    how many reviews (default 12)
//	BAUM_LAYOUT_EVAL_PARALLEL how many reviews at once (default 3)
//	BAUM_LAYOUT_EVAL_MODEL    the model; empty is the CLI's default
//	BAUM_LAYOUT_EVAL_VERBOSE  1 also logs each failure's request and answer, 2 every answer
//
// It logs counts only (and, verbose, the failures), never a review's
// content; the dataset's stores are read from copies in a temporary
// directory removed at the end.
func TestLayoutRequestsWithRealAgents(t *testing.T) {
	root, agentName := os.Getenv(realReviewsEnv), os.Getenv("BAUM_LAYOUT_EVAL_AGENT")
	if root == "" || agentName == "" {
		t.Skip("set BAUM_APP_REAL_REVIEWS and BAUM_LAYOUT_EVAL_AGENT")
	}
	count := envInt("BAUM_LAYOUT_EVAL_COUNT", 12)
	parallel := envInt("BAUM_LAYOUT_EVAL_PARALLEL", 3)
	work := t.TempDir()
	if err := os.Chmod(work, 0o700); err != nil {
		t.Fatal(err)
	}
	dataset := &realDataset{t: t, root: root, work: filepath.Join(work, "dataset"), stores: map[string]*store.Store{}}
	registry, err := repos.Open(filepath.Join(work, "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	cases := seedEvalCases(t, dataset, registry, work, count)
	if len(cases) == 0 {
		t.Fatal("no calibration review could be laid out as tiles")
	}

	api := uiserver.LocalAPI{Registry: registry, Config: config.Default(), Layouts: &tilelayout.Service{Path: filepath.Join(work, "app.sqlite")}}
	handler := api.Handler()
	defer handler.Close()
	mcp := agentcontext.New(agentcontext.Options{API: handler, Version: "eval", Layouts: uiserver.LayoutEditor{API: api}})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if r.URL.Path != "/api/mcp" || r.Method != http.MethodPost || kind != "application/json" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		mcp.ServeHTTP(w, r)
	}), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer func() { _ = server.Close() }()
	url := "http://" + listener.Addr().String() + "/api/mcp"

	var backend agent.Converser
	backendName := agent.BackendClaude
	switch agentName {
	case "claude":
		backend = &claude.Backend{Binary: "claude", Model: os.Getenv("BAUM_LAYOUT_EVAL_MODEL"), Profile: claude.User}
	case "codex":
		backend, backendName = &codex.Backend{Binary: "codex", Model: os.Getenv("BAUM_LAYOUT_EVAL_MODEL"), Profile: codex.Local}, agent.BackendCodex
	default:
		t.Fatalf("unknown agent %q", agentName)
	}

	var mu sync.Mutex
	results := map[string][2]int{}
	failures := map[string]int{}
	var cost float64
	var priced, turns int
	var tokens int64
	var elapsed []time.Duration
	jobs := make(chan evalCase)
	var wg sync.WaitGroup
	for range parallel {
		wg.Go(func() {
			for c := range jobs {
				for _, outcome := range runEvalCase(t, mcp, api, backend, url, c) {
					mu.Lock()
					r := results[outcome.request]
					r[1]++
					if outcome.ok {
						r[0]++
						if os.Getenv("BAUM_LAYOUT_EVAL_VERBOSE") == "2" {
							t.Logf("ok %s: %.300s", outcome.request, outcome.answer)
						}
					} else {
						failures[outcome.request+": "+outcome.why]++
						if os.Getenv("BAUM_LAYOUT_EVAL_VERBOSE") != "" {
							t.Logf("FAIL %s (%s): %s\n  answer: %.400s", outcome.request, outcome.why, outcome.prompt, outcome.answer)
						}
					}
					results[outcome.request] = r
					accounting := &agent.Accounting{}
					accounting.Add(outcome.result, false)
					if usd, _ := pricing.Active().BackendAccountingCost(backendName, accounting); usd != nil {
						cost += *usd
						priced++
					}
					turns++
					tokens += outcome.result.Usage.Input + outcome.result.Usage.Output + outcome.result.Usage.CacheRead + outcome.result.Usage.CacheCreate
					elapsed = append(elapsed, outcome.took)
					mu.Unlock()
				}
			}
		})
	}
	for _, c := range cases {
		jobs <- c
	}
	close(jobs)
	wg.Wait()

	total, passed := 0, 0
	names := make([]string, 0, len(results))
	for name := range results {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		r := results[name]
		total += r[1]
		passed += r[0]
		t.Logf("%-28s %d/%d", name, r[0], r[1])
	}
	slices.Sort(elapsed)
	t.Logf("agent %s: %d reviews, %d requests, %d right (%.0f%%); %d turns, %d priced, $%.2f at API prices, %d tokens (cache included); turn p50 %v, max %v",
		agentName, len(cases), total, passed, 100*float64(passed)/float64(max(total, 1)), turns, priced, cost, tokens, elapsed[len(elapsed)/2].Round(time.Second), elapsed[len(elapsed)-1].Round(time.Second))
	failed := make([]string, 0, len(failures))
	for why, n := range failures {
		failed = append(failed, fmt.Sprintf("%s ×%d", why, n))
	}
	sort.Strings(failed)
	for _, line := range failed {
		t.Logf("failure: %s", line)
	}
}

func envInt(name string, fallback int) int {
	if n, err := strconv.Atoi(os.Getenv(name)); err == nil && n > 0 {
		return n
	}
	return fallback
}

// evalCase is one saved review the agent lays out.
type evalCase struct {
	repository string
	number     int
	// wide is a change tile the first screen shows that can grow; first is
	// the first change on the page.
	wide, first string
	media       bool
}

// seedEvalCases copies the latest saved version of calibration pull
// requests, spread over the split, into stores of registered clones, keeping
// those whose tiles have what the requests name.
func seedEvalCases(t *testing.T, dataset *realDataset, registry *repos.Registry, work string, count int) []evalCase {
	t.Helper()
	ctx := context.Background()
	pulls := dataset.pullsWhere(func(p realIndexPull) bool { return p.Split == "calibration" && len(p.ReviewVersions) > 0 })
	sort.Slice(pulls, func(i, j int) bool {
		if pulls[i].Repository != pulls[j].Repository {
			return pulls[i].Repository < pulls[j].Repository
		}
		return pulls[i].Number < pulls[j].Number
	})
	clones := map[string]repos.Clone{}
	var out []evalCase
	step := max(1, len(pulls)/(count*2))
	for i := 0; i < len(pulls) && len(out) < count; i += step {
		p := pulls[i]
		machine := realBestMachine(p)
		key := store.Key{Repository: p.Repository, Number: p.Number}
		version, err := dataset.store(machine, p.Repository).Latest(ctx, key)
		if err != nil {
			continue
		}
		built := tileview.Build(version.Document, nil, tiles.Options{})
		c, ok := evalCaseOf(built.Tiles)
		if !ok {
			continue
		}
		clone, found := clones[p.Repository]
		if !found {
			dir := filepath.Join(work, "clones", realSlug(p.Repository))
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"remote", "add", "origin", "https://github.com/" + p.Repository}} {
				cmd := exec.Command("git", args...)
				cmd.Dir = dir
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatal(string(output), err)
				}
			}
			if clone, err = registry.Add(ctx, dir, nil); err != nil {
				t.Fatal(err)
			}
			clones[p.Repository] = clone
		}
		target, err := store.Open(clone.CommonDir, "", true)
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if json.Unmarshal(version.Document, &document) != nil {
			continue
		}
		review := document["review"].(map[string]any)
		review["version"], review["previous_version"] = 1, nil
		encoded, _ := json.Marshal(document)
		first := store.Version{Metadata: store.Metadata{Version: 1, Head: version.Metadata.Head, GeneratedAt: version.Metadata.GeneratedAt, Backend: version.Metadata.Backend, Model: version.Metadata.Model}, Document: encoded}
		if err := target.Put(ctx, store.Key{Repository: clone.Repository, Number: p.Number}, store.Version{}, first); err != nil {
			continue
		}
		c.repository, c.number = clone.Repository, p.Number
		out = append(out, c)
	}
	return out
}

// evalCaseOf keeps a review whose tiles have the risks, evidence and context
// tiles, and a change on the landing page that can grow.
func evalCaseOf(doc *tiles.Document) (evalCase, bool) {
	if doc == nil {
		return evalCase{}, false
	}
	var c evalCase
	byID := map[string]tiles.Tile{}
	for _, tile := range doc.Tiles {
		byID[tile.ID] = tile
		c.media = c.media || tile.Type == tiles.TypeMedia
	}
	for _, id := range []string{tiles.TypeRisks, tiles.TypeEvidence, tiles.TypeContext, tiles.TypeOverview} {
		if _, ok := byID[id]; !ok {
			return evalCase{}, false
		}
	}
	visible := tiles.Visible(doc.Layout)
	for i, entry := range doc.Layout.Entries {
		tile := byID[entry.Tile]
		if tile.Type != tiles.TypeChange || entry.Group == tiles.GroupObsolete {
			continue
		}
		if c.first == "" {
			c.first = tile.ID
		}
		if c.wide == "" && visible[i] && entry.Size != tiles.SizeFull && tile.ID != c.first {
			c.wide = tile.ID
		}
	}
	return c, c.first != "" && c.wide != ""
}

// evalOutcome is one request's score.
type evalOutcome struct {
	request, prompt, answer, why string
	ok                           bool
	result                       agent.Result
	took                         time.Duration
}

// evalRequest is one request: what the person says, the tile their page
// focuses (none for ""), and whether the layout ends as asked and the
// answer says what it should, given the layout before and after.
type evalRequest struct {
	name, prompt, focus string
	check               func(before, after evalState, answer string) string
}

// evalState is the layout the page shows.
type evalState struct {
	edited  bool
	entries []tiles.Entry
	doc     *tiles.Document
}

func (s evalState) index(id string) int {
	return slices.IndexFunc(s.entries, func(e tiles.Entry) bool { return e.Tile == id })
}

func (s evalState) entry(id string) tiles.Entry {
	if i := s.index(id); i >= 0 {
		return s.entries[i]
	}
	return tiles.Entry{}
}

func (s evalState) hidden(id string) bool { return s.entry(id).Group == tiles.GroupHidden }

func (s evalState) same(other evalState) bool {
	return slices.Equal(s.entries, other.entries)
}

func (s evalState) row(id string) int {
	for k, row := range tiles.Rows(s.doc.Layout) {
		for _, i := range row.Entries {
			if s.doc.Layout.Entries[i].Tile == id {
				return k
			}
		}
	}
	return -1
}

func wider(a, b tiles.Size) bool {
	return b.Units() > a.Units()
}

func mentions(answer string, words ...string) bool {
	lower := strings.ToLower(strings.ReplaceAll(answer, "’", "'"))
	for _, w := range words {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

// evalRequests are the requests, in the order a person might make them in
// one conversation.
func evalRequests(c evalCase) []evalRequest {
	media := evalRequest{name: "media: I don't need the media", prompt: "I don't need the media", check: func(before, after evalState, answer string) string {
		if !after.hidden(tiles.TypeMedia) {
			return "media not hidden"
		}
		return ""
	}}
	if !c.media {
		media = evalRequest{name: "media: none on the review", prompt: "I don't need the media", check: func(before, after evalState, answer string) string {
			if !before.same(after) {
				return "the layout changed"
			}
			if !mentions(answer, "media", "no ") {
				return "did not say there is none"
			}
			return ""
		}}
	}
	var undone evalState
	track := func(r evalRequest) evalRequest {
		check := r.check
		r.check = func(before, after evalState, answer string) string {
			if !before.same(after) {
				undone = before
			}
			return check(before, after, answer)
		}
		return r
	}
	requests := []evalRequest{
		{name: "ambiguous: the change bigger", prompt: "make the change bigger", check: func(before, after evalState, answer string) string {
			if !before.same(after) {
				return "changed a tile without asking which"
			}
			if !strings.Contains(answer, "?") {
				return "did not ask which change"
			}
			return ""
		}},
		{name: "move risks to the top", prompt: "Move the risks to the top", check: func(before, after evalState, answer string) string {
			if after.index(tiles.TypeRisks) > after.index(tiles.TypeOverview) || after.index(tiles.TypeRisks) > 1 {
				return "risks not first"
			}
			return ""
		}},
		{name: "make this wider (focus)", prompt: "make this wider", focus: c.wide, check: func(before, after evalState, answer string) string {
			if !wider(before.entry(c.wide).Size, after.entry(c.wide).Size) {
				return "focused tile not wider"
			}
			return ""
		}},
		{name: "remove this (focus)", prompt: "remove this", focus: c.wide, check: func(before, after evalState, answer string) string {
			if !after.hidden(c.wide) {
				return "focused tile not hidden"
			}
			return ""
		}},
		{name: "evidence next to overview", prompt: "put the evidence next to the overview", check: func(before, after evalState, answer string) string {
			if after.hidden(tiles.TypeEvidence) || after.row(tiles.TypeEvidence) != after.row(tiles.TypeOverview) {
				return "evidence not beside the overview"
			}
			return ""
		}},
		{name: "hide the context tile", prompt: "hide the context tile", check: func(before, after evalState, answer string) string {
			if !after.hidden(tiles.TypeContext) {
				return "context not hidden"
			}
			return ""
		}},
		media,
		{name: "first change full width", prompt: "make the first change full width", check: func(before, after evalState, answer string) string {
			if after.entry(c.first).Size != tiles.SizeFull {
				return "first change not full"
			}
			return ""
		}},
		{name: "undo that", prompt: "undo that", check: func(before, after evalState, answer string) string {
			// The newest change; the one before the first change's when that
			// changed nothing (it was full width already).
			if !after.same(undone) {
				return "not back to before the last change"
			}
			return ""
		}},
		{name: "impossible: hide the overview", prompt: "hide the overview", check: func(before, after evalState, answer string) string {
			if after.hidden(tiles.TypeOverview) || !before.same(after) {
				return "the layout changed"
			}
			if !mentions(answer, "can't", "cannot", "can not", "not possible", "always", "isn't possible", "unable", "won't", "not allowed", "doesn't allow", "stays") {
				return "did not say it cannot"
			}
			return ""
		}},
		{name: "reset the layout", prompt: "reset the layout", check: func(before, after evalState, answer string) string {
			if after.edited {
				return "still edited"
			}
			return ""
		}},
	}
	// Every request but the undo records the layout before a change it made.
	for i, r := range requests {
		if r.name != "undo that" {
			requests[i] = track(r)
		}
	}
	return requests
}

// runEvalCase runs the requests on one review in one conversation.
func runEvalCase(t *testing.T, mcp *agentcontext.Server, api uiserver.LocalAPI, backend agent.Converser, url string, c evalCase) []evalOutcome {
	ctx := context.Background()
	token, id, revoke, err := mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Chat})
	if err != nil {
		t.Error(err)
		return nil
	}
	defer revoke()
	editor := uiserver.LayoutEditor{API: api}
	state := func() evalState {
		shown, err := editor.Shown(ctx, c.repository, c.number)
		if err != nil {
			return evalState{}
		}
		return evalState{edited: shown.Edited, entries: slices.Clone(shown.Document.Layout.Entries), doc: shown.Document}
	}
	directory, err := os.MkdirTemp("", "layout-eval-")
	if err != nil {
		t.Error(err)
		return nil
	}
	defer func() { _ = os.RemoveAll(directory) }()
	session := ""
	var out []evalOutcome
	for _, request := range evalRequests(c) {
		focus := `{"view":"tiles"}`
		if request.focus != "" {
			before := state()
			title := ""
			if before.doc != nil {
				title = tiles.TileTitle(before.doc, request.focus)
			}
			encoded, _ := json.Marshal(map[string]string{"view": "tiles", "kind": "tile", "id": request.focus, "title": title})
			focus = string(encoded)
		}
		if err := mcp.Registry.SetPage(id, agentcontext.PageContext{Page: agentcontext.PagePullRequest, Repository: c.repository, PullRequest: c.number, Focus: json.RawMessage(focus)}); err != nil {
			t.Error(err)
			return out
		}
		before := state()
		start := time.Now()
		turn, cancel := context.WithTimeout(ctx, 4*time.Minute)
		result, err := backend.Converse(turn, agent.Request{Directory: directory, Prompt: request.prompt, ResumeSession: session, MCP: &agent.MCPServer{URL: url, Token: token}}, agent.AccessAuto)
		cancel()
		took := time.Since(start)
		if result.SessionID != "" {
			session = result.SessionID
		}
		answer := string(result.Document)
		outcome := evalOutcome{request: request.name, prompt: request.prompt, answer: answer, result: result, took: took}
		switch {
		case err != nil:
			outcome.why = "the turn failed: " + firstLine(err)
		case strings.TrimSpace(answer) == "":
			outcome.why = "no answer"
		default:
			outcome.why = request.check(before, state(), answer)
		}
		outcome.ok = outcome.why == ""
		out = append(out, outcome)
	}
	return out
}

func firstLine(err error) string {
	text := err.Error()
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	if len(text) > 120 {
		text = text[:120]
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		text += " (exit " + strconv.Itoa(exit.ExitCode()) + ")"
	}
	return text
}
