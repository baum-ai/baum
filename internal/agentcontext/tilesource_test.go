package agentcontext_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewgroups"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/internal/tileview/tileviewtest"
	"github.com/baum-ai/baum/internal/uiserver"
)

// draftExecutor reports one draft and runs until the test ends.
type draftExecutor struct {
	draft []byte
	done  chan struct{}
}

func (d *draftExecutor) Prepare(_ context.Context, item jobs.Item) (jobs.Prepared, string, error) {
	return jobs.Prepared{PriceVersion: "2026-10-01", Item: item, Repository: "acme/widgets", HeadSHA: "head", Backend: "claude", Model: "claude-sonnet-4-5"}, "2026-10-01", nil
}

func (d *draftExecutor) Execute(ctx context.Context, _ jobs.Prepared, progress func(jobs.Progress)) (*float64, error) {
	progress(jobs.Progress{Phase: "generating", Calls: 1, Draft: d.draft})
	select {
	case <-ctx.Done():
	case <-d.done:
	}
	return nil, nil
}

// What an agent reads of the three tile routes, served by the real handlers
// (a saved version, a group review and a running review's draft, each with
// the synthetic custom tiles), holds each custom tile's digest and none of
// its source.
func TestTileReadsHoldNoTileSource(t *testing.T) {
	t.Cleanup(tileview.SetFixture(tileviewtest.AddCustomTiles))
	ctx := context.Background()
	home := appdbtest.TempDir(t)
	registry, err := repos.Open(filepath.Join(home, "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	dir := appdbtest.TempDir(t)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"remote", "add", "origin", "https://github.com/acme/widgets"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(string(out), err)
		}
	}
	clone, err := registry.Add(ctx, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	versions, err := store.Open(clone.CommonDir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	review := func(version int, stack bool) []byte {
		doc := tileviewtest.Base(t)
		doc["review"].(map[string]any)["version"] = version
		delete(doc["review"].(map[string]any), "previous_version")
		if stack {
			doc["stack"] = map[string]any{"title": "Widget train", "members": []any{
				map[string]any{"number": 7, "head_sha": "aaaaaaa", "review_version": 1},
				map[string]any{"number": 8, "head_sha": "bbbbbbb", "review_version": 1},
			}}
		}
		return tileviewtest.Encode(t, doc)
	}
	if err := versions.Put(ctx, store.Key{Repository: "acme/widgets", Number: 42}, store.Version{}, store.Version{Metadata: store.Metadata{Version: 1}, Document: review(1, false)}); err != nil {
		t.Fatal(err)
	}

	groupStore, err := reviewgroups.Open(filepath.Join(home, "groups.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = groupStore.Close() })
	group, err := groupStore.Create(ctx, "acme/widgets", "Widget train", []int{7, 8})
	if err != nil {
		t.Fatal(err)
	}
	if err := groupStore.Put(ctx, group.ID, 0, store.Version{Metadata: store.Metadata{Version: 1, Model: "stub-model"}, Document: review(1, true)}); err != nil {
		t.Fatal(err)
	}

	executor := &draftExecutor{draft: tileviewtest.Encode(t, tileviewtest.DraftOutline(t)), done: make(chan struct{})}
	queue, err := jobs.Open(filepath.Join(appdbtest.TempDir(t), "jobs.json"), executor, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(queue.Close)
	t.Cleanup(func() { close(executor.done) })
	started, err := queue.Generate(ctx, []jobs.Item{{Clone: clone.ID, PR: 42}}, "single", false)
	if err != nil {
		t.Fatal(err)
	}

	api := uiserver.LocalAPI{Registry: registry, Config: config.Default(), Groups: (&reviewgroups.API{Store: groupStore, Registry: registry}).Handler(), Jobs: queue.Handler()}.Handler()
	t.Cleanup(api.Close)
	reader := agentcontext.NewReader(api, func(ctx context.Context, id string) (string, error) {
		return uiserver.RegisteredRepository(ctx, registry, id)
	})
	tool := agentcontext.ReaderWithin(reader, []string{"/api"}, "")
	action := agentcontext.ReaderWithin(reader, []string{"/api"}, "acme/widgets")

	draft := "/api/jobs/" + started[0].ID + "/draft?tiles=1"
	deadline := time.Now().Add(10 * time.Second)
	for {
		if response, err := tool.Get(ctx, draft); err == nil && response.Status == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the draft was never served")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, read := range []struct {
		name   string
		reader *agentcontext.Reader
		target string
	}{
		{"version", tool, "/api/tiles?clone=" + clone.ID + "&pr=42"},
		{"version, action", action, "/api/tiles?clone=" + clone.ID + "&pr=42"},
		{"group", tool, "/api/groups/" + group.ID + "/tiles"},
		{"draft", tool, draft},
	} {
		response, err := read.reader.Get(ctx, read.target)
		if err != nil || response.Status != 200 {
			t.Fatalf("%s: %v %d %s", read.name, err, response.Status, response.Body)
		}
		var answer struct {
			Tiles *struct {
				Tiles []struct {
					ID         string          `json:"id"`
					Definition json.RawMessage `json:"definition"`
				} `json:"tiles"`
			} `json:"tiles"`
		}
		if err := json.Unmarshal(response.Body, &answer); err != nil || answer.Tiles == nil {
			t.Fatalf("%s: %v %.300s", read.name, err, response.Body)
		}
		found := 0
		for _, tile := range answer.Tiles.Tiles {
			for _, id := range tileviewtest.CustomTileIDs {
				if tile.ID == id && strings.Contains(string(tile.Definition), tileviewtest.CustomDefinition(id).Digest) {
					found++
				}
			}
		}
		if found != len(tileviewtest.CustomTileIDs) {
			t.Errorf("%s: %d of the custom tiles with their digests", read.name, found)
		}
		body := string(response.Body)
		for _, source := range []string{"baum.tile(", "<script", `<script`, "outside.invalid/base", `"html":`} {
			if strings.Contains(body, source) {
				t.Errorf("%s: the answer holds tile source (%s)", read.name, source)
			}
		}
	}
}
