package uiserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/replay"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/tiles"
)

// With BAUM_APP_REPLAY_RUN naming a replay run directory (of the compose or
// tiles generator), the real-reviews fixture serves that run's layouts
// instead of picks from the dataset: each case's review stored twice, once
// with the run's composition and once with the template's, each as the
// latest version of the pull request in a clone of its own (a variant), so
// the page shows both through the app's own store and routes. The
// replay-screens spec photographs them. Its environment, besides the
// dataset's:
//
//	BAUM_APP_REPLAY_RUN       the run directory
//	BAUM_APP_REPLAY_ONLY      only the cases whose id contains this text
//	BAUM_APP_REPLAY_LIMIT     at most this many cases
//	BAUM_APP_REPLAY_VARIANTS  the layouts to serve, separated by commas (default composed,template)
//
// replay-cases.json in BAUM_APP_STATE_DIR lists every case of the run with
// why it is not served, if it is not.
const (
	replayRunEnv       = "BAUM_APP_REPLAY_RUN"
	replayCasesName    = "replay-cases.json"
	replayVariantOwn   = "composed"
	replayVariantTempl = "template"
)

func seedReplayRun(t *testing.T, dataset *realDataset, registry *repos.Registry, state, run string) ([]realPick, map[string]string, map[string]string) {
	t.Helper()
	ctx := context.Background()
	cases, err := replay.ScreenInputs(ctx, dataset.root, run)
	if err != nil {
		t.Fatal(err)
	}
	only := os.Getenv("BAUM_APP_REPLAY_ONLY")
	limit, _ := strconv.Atoi(os.Getenv("BAUM_APP_REPLAY_LIMIT"))
	variants := []string{replayVariantOwn, replayVariantTempl}
	if v := os.Getenv("BAUM_APP_REPLAY_VARIANTS"); v != "" {
		variants = strings.Split(v, ",")
	}
	var kept []replay.ScreenCase
	for _, c := range cases {
		if !strings.Contains(c.ID, only) || (limit > 0 && len(kept) >= limit) {
			continue
		}
		kept = append(kept, c)
	}
	raw, _ := json.MarshalIndent(kept, "", "  ")
	if err := os.WriteFile(filepath.Join(state, replayCasesName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	type cloneKey struct {
		variant, repository string
		slot                int
	}
	clones := map[cloneKey]repos.Clone{}
	cloneOf := func(key cloneKey) repos.Clone {
		if c, ok := clones[key]; ok {
			return c
		}
		parent := filepath.Join(state, "clones", fmt.Sprintf("%s-%d", key.variant, key.slot))
		if err := os.MkdirAll(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(parent, realSlug(key.repository))
		mirror := filepath.Join(dataset.root, "repos", realSlug(key.repository)+".git")
		assembletest.Git(t, parent, "-c", "safe.directory=*", "clone", "--quiet", "--shared", "--no-checkout", mirror, dir)
		assembletest.Git(t, dir, "remote", "set-url", "origin", "https://github.com/"+key.repository)
		c, err := registry.Add(ctx, dir, nil)
		if err != nil {
			t.Fatalf("registering a clone of %s: %v", key.repository, err)
		}
		clones[key] = c
		return c
	}
	var picks []realPick
	heads, titles := map[string]string{}, map[string]string{}
	// A pull request with several cases in the run gets a clone per case.
	slots := map[string]int{}
	targets := map[string]*store.Store{}
	for _, c := range kept {
		if c.Problem != "" {
			t.Logf("case %s is not served: %s", c.ID, c.Problem)
			continue
		}
		pr := fmt.Sprintf("%s#%d", c.Repository, c.Number)
		slot := slots[pr]
		slots[pr]++
		facts := realFactsOf(c.Document)
		for _, variant := range variants {
			layout := c.Composition
			if variant == replayVariantTempl {
				layout = c.Template
			}
			clone := cloneOf(cloneKey{variant, c.Repository, slot})
			target := targets[clone.ID]
			if target == nil {
				var err error
				if target, err = store.Open(clone.CommonDir, "", true); err != nil {
					t.Fatal(err)
				}
				targets[clone.ID] = target
			}
			if err := storeReplayCase(ctx, dataset, target, c, layout); err != nil {
				t.Fatalf("case %s, %s: %v", c.ID, variant, err)
			}
			machine, _, _ := strings.Cut(c.Store, "/")
			versions := make([]int, c.Version)
			for i := range versions {
				versions[i] = i + 1
			}
			picks = append(picks, realPick{Repository: c.Repository, Number: c.Number, Title: facts.title, Version: c.Version, Versions: versions,
				Clone: clone.ID, Case: c.ID, Variant: variant, Size: c.Size, machine: machine})
		}
		heads[pr], titles[pr] = facts.head, facts.title
	}
	raw, _ = json.MarshalIndent(picks, "", "  ")
	if err := os.WriteFile(filepath.Join(state, realPicksFileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return picks, heads, titles
}

// storeReplayCase puts a case's review into target as the pull request's
// latest version with layout as its composition, after the versions before
// it from the dataset's saved store.
func storeReplayCase(ctx context.Context, dataset *realDataset, target *store.Store, c replay.ScreenCase, layout json.RawMessage) error {
	composition, err := tiles.ParseComposition(layout)
	if err != nil {
		return err
	}
	key := store.Key{Repository: c.Repository, Number: c.Number}
	var previous store.Version
	last := c.Version - 1
	if c.Saved {
		last = c.Version
	}
	for n := 1; n <= last; n++ {
		source := dataset.storeAt(c.Store)
		v, err := source.Get(ctx, key, n)
		if err != nil {
			return fmt.Errorf("saved version %d: %w", n, err)
		}
		for _, id := range realAttachmentIDs(v.Document) {
			if a, err := source.Attachment(ctx, key, n, id); err == nil {
				v.Attachments = append(v.Attachments, a)
			}
		}
		if n == c.Version {
			v.Composition, v.CompositionGenerator = layout, composition.Generator
		}
		if err := target.Put(ctx, key, previous, v); err != nil {
			return fmt.Errorf("version %d: %w", n, err)
		}
		previous = v
	}
	if c.Saved {
		return nil
	}
	v := store.Version{Metadata: c.Metadata, Document: c.Document, Attachments: c.Attachments, Composition: layout, CompositionGenerator: composition.Generator}
	return target.Put(ctx, key, previous, v)
}
