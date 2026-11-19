package uiserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/claude"
	"github.com/baum-ai/baum/agent/codex"
	"github.com/baum-ai/baum/internal/addedtiles"
	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/daemon"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/tilechecks"
	"github.com/baum-ai/baum/internal/tiledrafts"
	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/internal/uiserver"
	"github.com/baum-ai/baum/pricing"
)

// TestChatTilesWithRealAgents asks the session panel's own agent (the
// person's signed-in Claude Code or Codex, with baum's MCP server) to make a
// tile for real saved reviews of a private dataset's calibration split, in a
// checkout of each pull request's head, and records whether the tile was
// added, the check rounds it took, whether its citations resolved, the time
// and the spend. It runs the real daemon, so a browser opened at its launch
// URL (credential.json in BAUM_APP_STATE_DIR) is the render check's runner;
// once every request ran it writes results.json there and waits up to ten
// minutes for screens-done, so the browser can take pictures of the tiles;
// current.json names the case being made, which the browser may follow.
// It runs on the host, where the CLI is signed in, and only when asked:
//
//	BAUM_APP_REAL_REVIEWS        the dataset directory
//	BAUM_APP_STATE_DIR           the daemon's state directory
//	BAUM_CHAT_TILE_EVAL_AGENT    claude or codex
//	BAUM_CHAT_TILE_EVAL_PICKS    the pull requests, repo#number, separated by commas
//	BAUM_CHAT_TILE_EVAL_PROMPT   what the person asks
//	BAUM_CHAT_TILE_EVAL_MODEL    the model; empty is the CLI's default
//	BAUM_CHAT_TILE_EVAL_PORT     the daemon's port (default 4171)
//
// It logs counts only, never a review's content.
func TestChatTilesWithRealAgents(t *testing.T) {
	root, agentName := os.Getenv(realReviewsEnv), os.Getenv("BAUM_CHAT_TILE_EVAL_AGENT")
	state, prompt := os.Getenv("BAUM_APP_STATE_DIR"), os.Getenv("BAUM_CHAT_TILE_EVAL_PROMPT")
	if root == "" || agentName == "" || state == "" || prompt == "" {
		t.Skip("set BAUM_APP_REAL_REVIEWS, BAUM_APP_STATE_DIR, BAUM_CHAT_TILE_EVAL_AGENT, BAUM_CHAT_TILE_EVAL_PICKS and BAUM_CHAT_TILE_EVAL_PROMPT")
	}
	picks := parseRealPicks(t, os.Getenv("BAUM_CHAT_TILE_EVAL_PICKS"))
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := os.Chmod(work, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	dataset := &realDataset{t: t, root: root, work: filepath.Join(work, "dataset"), stores: map[string]*store.Store{}}
	registry, err := repos.Open(filepath.Join(work, "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()

	// Each pick's latest saved version, as version 1 of a clone sharing the
	// mirror's objects, and a checkout of its head for the agent.
	type evalPick struct {
		repository string
		number     int
		checkout   string
	}
	var cases []evalPick
	var served []realPick
	heads, titles := map[string]string{}, map[string]string{}
	clones := map[string]repos.Clone{}
	for _, p := range picks {
		var pull realIndexPull
		for _, q := range dataset.pullsWhere(func(q realIndexPull) bool { return q.Repository == p.Repository && q.Number == p.Number }) {
			pull = q
		}
		machine := realBestMachine(pull)
		if machine == "" || pull.Split != "calibration" {
			t.Fatalf("pick %d is not a calibration pull request with a saved review", len(cases)+1)
		}
		key := store.Key{Repository: p.Repository, Number: p.Number}
		version, err := dataset.store(machine, p.Repository).Latest(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		clone, ok := clones[p.Repository]
		if !ok {
			parent := filepath.Join(work, "clones")
			if err := os.MkdirAll(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(parent, realSlug(p.Repository))
			assembletest.Git(t, parent, "-c", "safe.directory=*", "clone", "--quiet", "--shared", "--no-checkout", filepath.Join(root, "repos", realSlug(p.Repository)+".git"), dir)
			assembletest.Git(t, dir, "remote", "set-url", "origin", "https://github.com/"+p.Repository)
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
		if err := json.Unmarshal(version.Document, &document); err != nil {
			t.Fatal(err)
		}
		review := document["review"].(map[string]any)
		review["version"], review["previous_version"] = 1, nil
		encoded, _ := json.Marshal(document)
		first := store.Version{Metadata: version.Metadata, Document: encoded}
		first.Metadata.Version = 1
		if err := target.Put(ctx, key, store.Version{}, first); err != nil {
			t.Fatal(err)
		}
		checkout := filepath.Join(work, "checkouts", realSlug(p.Repository)+"-"+version.Metadata.Head[:12])
		assembletest.Git(t, clone.Path, "-c", "safe.directory=*", "worktree", "add", "--quiet", "--detach", checkout, version.Metadata.Head)
		cases = append(cases, evalPick{repository: clone.Repository, number: p.Number, checkout: checkout})
		facts := realFactsOf(version.Document)
		heads[p.id()], titles[p.id()] = facts.head, facts.title
		served = append(served, realPick{Repository: p.Repository, Number: p.Number, Version: 1, Versions: []int{1}, Clone: clone.ID, Title: facts.title})
	}

	var backend agent.Converser
	backendName := agent.BackendClaude
	switch agentName {
	case "claude":
		backend = &claude.Backend{Binary: "claude", Model: os.Getenv("BAUM_CHAT_TILE_EVAL_MODEL"), Profile: claude.User}
	case "codex":
		backend, backendName = &codex.Backend{Binary: "codex", Model: os.Getenv("BAUM_CHAT_TILE_EVAL_MODEL"), Profile: codex.Local}, agent.BackendCodex
	default:
		t.Fatalf("unknown agent %q", agentName)
	}

	port := 4171
	if n := envInt("BAUM_CHAT_TILE_EVAL_PORT", 0); n > 0 {
		port = n
	}
	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	ready := make(chan struct {
		server *uiserver.Server
		mcp    *agentcontext.Server
		api    uiserver.LocalAPI
		tiles  *countingTiles
	}, 1)
	home := filepath.Join(work, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	go func() {
		defer stop()
		env := <-ready
		// The browser opens the app at its launch URL and runs the checks.
		for deadline := time.Now().Add(5 * time.Minute); !env.server.Subscribed(); {
			if time.Now().After(deadline) {
				t.Error("no browser opened the app to run the render checks")
				return
			}
			time.Sleep(time.Second)
		}
		url := env.server.Origin() + "/api/mcp"

		type result struct {
			Clone    string  `json:"clone"`
			Number   int     `json:"number"`
			Tile     string  `json:"tile,omitempty"`
			Added    bool    `json:"added"`
			Ready    bool    `json:"ready"`
			Rounds   int     `json:"rounds"`
			Verdicts string  `json:"verdicts"`
			Codes    string  `json:"codes,omitempty"`
			Seconds  float64 `json:"seconds"`
			CostUSD  float64 `json:"cost_usd"`
			Turn     string  `json:"turn,omitempty"`
		}
		var results []result
		var total float64
		for i, c := range cases {
			token, id, revoke, err := env.mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Chat})
			if err != nil {
				t.Error(err)
				return
			}
			page := agentcontext.PageContext{Page: agentcontext.PagePullRequest, Repository: c.repository, PullRequest: c.number, Focus: json.RawMessage(`{"view":"tiles"}`)}
			if err := env.mcp.Registry.SetPage(id, page); err != nil {
				t.Error(err)
				return
			}
			if err := env.mcp.Registry.SetTurnPage(id, page); err != nil {
				t.Error(err)
				return
			}
			pr := addedtiles.Key(c.repository, c.number)
			before, _ := env.api.Added.List(ctx, pr)
			// The browser follows the case being made, to picture its card.
			current, _ := json.Marshal(map[string]any{"clone": clones[c.repository].ID, "number": c.number, "case": i + 1})
			_ = os.WriteFile(filepath.Join(state, "current.json"), current, 0o600)
			start := time.Now()
			turn, cancel := context.WithTimeout(ctx, 12*time.Minute)
			res, err := backend.Converse(turn, agent.Request{Directory: c.checkout, Prompt: prompt, MCP: &agent.MCPServer{URL: url, Token: token}}, agent.AccessAuto)
			cancel()
			env.mcp.Registry.EndTurn(id)
			revoke()
			out := result{Clone: clones[c.repository].ID, Number: c.number, Seconds: time.Since(start).Seconds()}
			// The answer stays in the state directory, beside the results.
			_ = os.WriteFile(filepath.Join(state, fmt.Sprintf("answer-%d.txt", i+1)), res.Document, 0o600)
			if err != nil {
				out.Turn = firstLine(err)
			}
			accounting := &agent.Accounting{}
			accounting.Add(res, err != nil)
			if usd, _ := pricing.Active().BackendAccountingCost(backendName, accounting); usd != nil {
				out.CostUSD = *usd
				total += *usd
			}
			tally := env.tiles.tally(id)
			out.Rounds = len(tally.verdicts)
			out.Verdicts = strings.Join(tally.verdicts, ",")
			out.Codes = strings.Join(tally.codes, ",")
			if after, err := env.api.Added.List(ctx, pr); err == nil {
				for _, a := range after {
					if a.Kind != addedtiles.KindCustom || slices.ContainsFunc(before, func(b addedtiles.Added) bool { return b.Tile == a.Tile }) {
						continue
					}
					// A tile made in chat is stored with the data its check
					// passed with: it is ready.
					out.Tile, out.Added, out.Ready = a.Tile, true, true
				}
			}
			t.Logf("case %d: added %v, ready %v, rounds %d (%s), codes [%s], %.0fs, $%.2f %s", i+1, out.Added, out.Ready, out.Rounds, out.Verdicts, out.Codes, out.Seconds, out.CostUSD, out.Turn)
			results = append(results, out)
		}
		t.Logf("agent %s: %d cases, %d added, $%.2f at API prices", agentName, len(results), len(slices.DeleteFunc(slices.Clone(results), func(r result) bool { return !r.Added })), total)
		encoded, _ := json.MarshalIndent(results, "", "  ")
		if err := os.WriteFile(filepath.Join(state, "results.json"), encoded, 0o600); err != nil {
			t.Error(err)
			return
		}
		for deadline := time.Now().Add(10 * time.Minute); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
			if _, err := os.Stat(filepath.Join(state, "screens-done")); err == nil {
				break
			}
		}
	}()
	serveRealReviewsUntil(runCtx, t, home, state, registry, videoDiscovery(realPullRequests(t, root, served, heads, titles)), port,
		func(server *uiserver.Server, local *uiserver.LocalAPI) func(http.Handler, *daemon.App) {
			added := &addedtiles.Service{Path: filepath.Join(work, "app.sqlite"), Changed: func(pr string) {
				_ = server.Publish("added_tiles", map[string]any{"pr": pr})
			}}
			checker, err := tilechecks.New(tilechecks.Options{Frames: tileview.FrameRegistry(), Publish: func(e tilechecks.Event) { _ = server.Publish("tile_check", e) }, Subscribed: server.Subscribed})
			if err != nil {
				t.Error(err)
				return nil
			}
			drafts := &tiledrafts.Store{Changed: func(pr string) {
				_ = server.Publish("added_tiles", map[string]any{"pr": pr})
			}}
			local.Added, local.Drafts, local.TileChecks = added, drafts, checker.Handler()
			return func(api http.Handler, app *daemon.App) {
				counted := &countingTiles{inner: &uiserver.ChatTiles{API: *local, Checks: checker, Drafts: drafts}, by: map[string]*chatTileTally{}}
				mcp := agentcontext.New(agentcontext.Options{API: api, Version: "eval", Added: uiserver.AddedTilesAPI{API: *local}, ChatTiles: counted})
				app.MCP = mcp
				ready <- struct {
					server *uiserver.Server
					mcp    *agentcontext.Server
					api    uiserver.LocalAPI
					tiles  *countingTiles
				}{server, mcp, *local, counted}
			}
		})
}

// countingTiles counts what the tile making tools did, per conversation.
type countingTiles struct {
	inner agentcontext.ChatTiles
	mu    sync.Mutex
	by    map[string]*chatTileTally
}

type chatTileTally struct {
	verdicts, codes []string
}

func (c *countingTiles) tally(owner string) chatTileTally {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t := c.by[owner]; t != nil {
		return *t
	}
	return chatTileTally{}
}

func (c *countingTiles) Start(ctx context.Context, owner, repository string, number int, start agentcontext.TileStart) (agentcontext.TileDraft, error) {
	return c.inner.Start(ctx, owner, repository, number, start)
}

func (c *countingTiles) Check(ctx context.Context, owner, repository string, number int, check agentcontext.TileCheck) (json.RawMessage, error) {
	out, err := c.inner.Check(ctx, owner, repository, number, check)
	var answer struct {
		Verdict string `json:"verdict"`
		Errors  []struct {
			Code string `json:"code"`
		} `json:"errors"`
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.by[owner]
	if t == nil {
		t = &chatTileTally{}
		c.by[owner] = t
	}
	if err != nil {
		t.verdicts = append(t.verdicts, "error")
		return out, err
	}
	_ = json.Unmarshal(out, &answer)
	t.verdicts = append(t.verdicts, answer.Verdict)
	for _, e := range answer.Errors {
		if !slices.Contains(t.codes, e.Code) {
			t.codes = append(t.codes, e.Code)
		}
	}
	return out, err
}

func (c *countingTiles) Add(ctx context.Context, owner, repository string, number int, add agentcontext.CustomTileAdd) (agentcontext.TileAdded, error) {
	return c.inner.Add(ctx, owner, repository, number, add)
}

func (c *countingTiles) TurnEnded(owner string) { c.inner.TurnEnded(owner) }

func envInt(name string, fallback int) int {
	if n, err := strconv.Atoi(os.Getenv(name)); err == nil && n > 0 {
		return n
	}
	return fallback
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
