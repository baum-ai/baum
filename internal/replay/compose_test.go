package replay_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/replay"
	"github.com/baum-ai/baum/schema"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/compose"
)

// completing is a backend that also completes: it answers a composition with
// the template's entries, the changes out of their group at their sizes, the
// first with a reason (the host's guard keeps a layout that shows fewer changes on
// the first screen than the template).
type completing struct {
	*generatetest.Backend
	calls atomic.Int32
}

// CanComplete overrides the stub's, which completes only with a CompleteFunc.
func (c *completing) CanComplete() bool { return true }

func (c *completing) Complete(_ context.Context, r agent.CompleteRequest) (agent.Completion, error) {
	c.calls.Add(1)
	var inv compose.Inventory
	if err := json.Unmarshal([]byte(r.User[strings.Index(r.User, "\n")+1:]), &inv); err != nil {
		return agent.Completion{}, err
	}
	reply := compose.Reply{Entries: []compose.ReplyEntry{}, Promote: []compose.ReplyPromotion{}, Moves: []tiles.Move{}}
	led := false
	why := "the riskiest change"
	for _, entry := range inv.Template.Entries {
		e := compose.ReplyEntry{Tile: entry.Tile, Size: entry.Size}
		if entry.Group != "" {
			if !led {
				led, e.Why = true, &why
			}
		}
		reply.Entries = append(reply.Entries, e)
	}
	raw, err := json.Marshal(reply)
	return agent.Completion{Document: raw, Model: "claude-opus-5-5", Usage: agent.Usage{Input: 5000, Output: 500}}, err
}

func composeSettings(t *testing.T, backend generate.CheckedBackend, options map[string]string) replay.Settings {
	t.Helper()
	g, err := replay.NewGenerator("compose", replay.GeneratorConfig{Options: options})
	if err != nil {
		t.Fatal(err)
	}
	return replay.Settings{Generator: g, Backends: generate.Backends{Claude: func(string) generate.CheckedBackend { return backend }}}
}

// earlierRun writes a run directory whose case c1 generated the schema's
// example review.
func earlierRun(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "cases", "c1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cases", "c1", "document.json"), schema.Example, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readRecord(t *testing.T, dir string) replay.ComposeRecord {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "document.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record replay.ComposeRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestComposeOnlyComposesAnEarlierRunsReviews(t *testing.T) {
	d := newDataset(t, assembleSource(t))
	d.addCase(t, "c1", nil)
	ds, _ := replay.OpenDataset(d.dir)
	backend := &completing{Backend: &generatetest.Backend{Version: "1.0.0"}}
	outDir := t.TempDir()
	summary, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Settings: composeSettings(t, backend, map[string]string{"source": replay.ComposeFromRun, "run": earlierRun(t)})})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Totals.OK != 1 || backend.calls.Load() != 1 || len(backend.Requests) != 0 {
		t.Fatalf("%+v, %d completions, %d runs", summary.Totals, backend.calls.Load(), len(backend.Requests))
	}
	caseDir := filepath.Join(outDir, "run", "cases", "c1")
	out, err := replay.ReadOutcome(caseDir)
	if err != nil || !out.DocumentValid || out.CostUSD == nil || out.Generator != "compose" || len(out.CallLog) != 1 || !strings.Contains(out.CallLog[0].Name, "compose") {
		t.Fatalf("%v %+v", err, out)
	}
	for _, name := range []string{"composition.json", "template.json", "tiles.json", "template-tiles.json", "inventory.txt", "reply.json"} {
		if _, err := os.Stat(filepath.Join(caseDir, name)); err != nil {
			t.Error(err)
		}
	}
	record := readRecord(t, caseDir)
	delete(record.Fixes, tiles.FixRowFill) // the host's row fill is not a repair
	if record.Fallback || !record.DiffersFromTemplate || record.Featured < 1 || record.Review.Match != "run" || len(record.Fixes) != 0 {
		t.Errorf("record %+v", record)
	}
	metrics, err := replay.ReadMetrics(filepath.Join(outDir, "run"))
	if err != nil {
		t.Fatal(err)
	}
	stats := metrics.All.Stats
	if stats[replay.MetricComposeFallback].Sum != 0 || stats[replay.MetricComposeDiffers].Sum != 1 || stats[replay.MetricComposeFixes].N != 1 {
		t.Errorf("metrics %+v", stats)
	}
}

func TestComposeFromStoreReadsTheCasesSavedReview(t *testing.T) {
	d := newDataset(t, assembleSource(t))
	d.addCase(t, "c1", nil)
	raw, err := os.ReadFile(filepath.Join(d.dir, "cases", "c1", "bundle.json"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(strings.TrimSuffix(string(raw), "\n")))
	var b struct {
		Repository  string `json:"repository"`
		PullRequest struct {
			Number int `json:"number"`
		} `json:"pull_request"`
	}
	_ = json.Unmarshal(raw, &b)
	store := filepath.Join(d.dir, "reviews", "machine", "repo.versions.sqlite")
	if err := os.MkdirAll(filepath.Dir(store), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: filepath.ToSlash(store)}).String())
	if err != nil {
		t.Fatal(err)
	}
	// One connection, in WAL mode, kept open through the run: the rows stay
	// in the write-ahead log, as a running app's store holds them.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE versions (repository TEXT NOT NULL, pr INTEGER NOT NULL, version INTEGER NOT NULL, metadata BLOB NOT NULL, document BLOB NOT NULL, PRIMARY KEY(repository,pr,version))`); err != nil {
		t.Fatal(err)
	}
	for version, digest := range map[int]string{1: hex.EncodeToString(sum[:]), 2: "other"} {
		document := schema.Example
		if version == 2 {
			document = []byte(`not the matched version`)
		}
		meta, _ := json.Marshal(map[string]string{"bundle_digest": digest})
		if _, err := db.Exec(`INSERT INTO versions VALUES (?, ?, ?, ?, ?)`, b.Repository, b.PullRequest.Number, version, meta, document); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { _ = db.Close() }()
	if _, err := os.Stat(store + "-wal"); err != nil {
		t.Fatalf("the store has no write-ahead log: %v", err)
	}
	before := snapshot(t, filepath.Join(d.dir, "reviews"))
	// The copies go to the temporary directory and are removed with the run.
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	ds, _ := replay.OpenDataset(d.dir)
	backend := &completing{Backend: &generatetest.Backend{Version: "1.0.0"}}
	outDir := t.TempDir()
	summary, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Settings: composeSettings(t, backend, map[string]string{"source": replay.ComposeFromStore, "dataset": d.dir})})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Totals.OK != 1 {
		t.Fatalf("%+v", summary)
	}
	record := readRecord(t, filepath.Join(outDir, "run", "cases", "c1"))
	if record.Review.Match != "bundle" || record.Review.Version != 1 || record.Fallback {
		t.Errorf("record %+v", record)
	}
	if snapshot(t, filepath.Join(d.dir, "reviews")) != before {
		t.Error("the run wrote into the dataset's review stores")
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "baum-replay-reviews-*")); len(left) != 0 {
		t.Errorf("the run left its copies of the stores: %v", left)
	}
}

func TestComposeFallsBackWhenTheBackendCannotComplete(t *testing.T) {
	d := newDataset(t, assembleSource(t))
	d.addCase(t, "c1", nil)
	ds, _ := replay.OpenDataset(d.dir)
	backend := &generatetest.Backend{Version: "1.0.0"}
	outDir := t.TempDir()
	summary, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Settings: composeSettings(t, backend, map[string]string{"source": replay.ComposeFromRun, "run": earlierRun(t)})})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Totals.OK != 1 {
		t.Fatalf("%+v", summary)
	}
	caseDir := filepath.Join(outDir, "run", "cases", "c1")
	record := readRecord(t, caseDir)
	if !record.Fallback || record.FallbackReason != agent.FallbackNoCompleter || record.DiffersFromTemplate {
		t.Errorf("record %+v", record)
	}
	out, _ := replay.ReadOutcome(caseDir)
	if !out.DocumentValid || len(out.Notes) != 1 || out.Notes[0] != "fallback" {
		t.Errorf("%+v", out)
	}
}

func TestComposeGeneratorRefusesWhatItCannotUse(t *testing.T) {
	for _, c := range []struct {
		options   map[string]string
		overrides map[string]string
	}{
		{options: map[string]string{}},
		{options: map[string]string{"source": replay.ComposeFromRun}},
		{options: map[string]string{"source": replay.ComposeFromStore}},
		{options: map[string]string{"source": replay.ComposeFromRun, "run": "x", "other": "y"}},
		{options: map[string]string{"source": replay.ComposeFromRun, "run": "x"}, overrides: map[string]string{"review-staged-outline": "x"}},
	} {
		if _, err := replay.NewGenerator("compose", replay.GeneratorConfig{Options: c.options, PromptOverrides: c.overrides}); err == nil {
			t.Errorf("%v %v accepted", c.options, c.overrides)
		}
	}
}
