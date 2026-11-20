package replay_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/replay"
)

// pullRevision adds a titled case of a pull request of acme/demo.
func (d *dataset) pullRevision(t *testing.T, number int, id string, extra map[string]any) {
	t.Helper()
	facts := map[string]any{"repository": "acme/demo", "number": number, "revision": id}
	for k, v := range extra {
		facts[k] = v
	}
	d.addTitled(t, id, facts)
}

// Capture times are compared as instants: 10:00+05:00 is before 06:00Z, though
// its text sorts after.
func TestChainOrderUsesParsedCaptureTimes(t *testing.T) {
	src := assembleSource(t)
	d := newDataset(t, src)
	d.pullRevision(t, 7, "late", map[string]any{"captured_at": "2026-01-01T06:00:00Z"})
	d.pullRevision(t, 7, "early", map[string]any{"captured_at": "2026-01-01T10:00:00+05:00"})
	d.pullRevision(t, 7, "unparsed", map[string]any{"captured_at": "yesterday"})
	ds, _ := replay.OpenDataset(d.dir)
	chains, err := ds.SelectChains(replay.Selection{})
	if err != nil || len(chains) != 1 || !slices.Equal(chains[0].IDs, []string{"unparsed", "early", "late"}) {
		t.Errorf("%+v %v", chains, err)
	}
}

// Of two primary cases of one revision the earliest captured is used, then the
// lowest id, whatever the index order; a revision without a previous version
// number that follows one with a number is flagged.
func TestChainsPickOnePrimaryPerRevisionDeterministically(t *testing.T) {
	src := assembleSource(t)
	d := newDataset(t, src)
	d.pullRevision(t, 7, "z-first", map[string]any{"revision": "rev1", "captured_at": "2026-01-01T00:00:00Z"})
	d.pullRevision(t, 7, "a-second-machine", map[string]any{"revision": "rev1", "captured_at": "2026-01-02T00:00:00Z"})
	d.pullRevision(t, 7, "b-tie", map[string]any{"revision": "rev1", "captured_at": "2026-01-01T00:00:00Z"})
	d.pullRevision(t, 7, "rev2", map[string]any{"revision": "rev2", "captured_at": "2026-01-03T00:00:00Z", "previous_version": 1})
	d.pullRevision(t, 7, "rev3", map[string]any{"revision": "rev3", "captured_at": "2026-01-04T00:00:00Z"})
	ds, _ := replay.OpenDataset(d.dir)
	chains, err := ds.SelectChains(replay.Selection{})
	if err != nil || len(chains) != 1 {
		t.Fatalf("%+v %v", chains, err)
	}
	// rev3 has no number: it sorts first as version 0, which is a guess.
	if !slices.Equal(chains[0].IDs, []string{"b-tie", "rev3", "rev2"}) && !slices.Equal(chains[0].IDs, []string{"rev3", "b-tie", "rev2"}) {
		t.Errorf("chain %v", chains[0].IDs)
	}
	if !slices.Contains(chains[0].IDs, "b-tie") || slices.Contains(chains[0].IDs, "z-first") || slices.Contains(chains[0].IDs, "a-second-machine") {
		t.Errorf("the primary of the first revision is %v", chains[0].IDs)
	}
	if !slices.ContainsFunc(chains[0].Warnings, func(w string) bool { return strings.Contains(w, "rev3") }) && !slices.ContainsFunc(chains[0].Warnings, func(w string) bool { return strings.Contains(w, "captured before") }) {
		t.Errorf("warnings %v", chains[0].Warnings)
	}
}

func TestChainLimitAndNamesAndMissingFacts(t *testing.T) {
	src := assembleSource(t)
	d := newDataset(t, src)
	d.pullRevision(t, 7, "a1", nil)
	d.pullRevision(t, 7, "a2", map[string]any{"previous_version": 1})
	d.pullRevision(t, 7, "a3", map[string]any{"previous_version": 2})
	ds, _ := replay.OpenDataset(d.dir)
	// A limit below every chain is an error that says why, not an empty selection.
	if _, err := ds.SelectChains(replay.Selection{Limit: 2}); err == nil || !strings.Contains(err.Error(), "shortest of the 1 selected chains") || !strings.Contains(err.Error(), "raise --limit") {
		t.Errorf("error %v", err)
	}
	_, _, err := execute(t, replay.Plan{Dataset: ds, Chain: true, DryRun: true, Selection: replay.Selection{Limit: 2}})
	if err == nil || !strings.Contains(err.Error(), "--limit") {
		t.Errorf("error %v", err)
	}
	// The cap on cases refers to chains.
	_, _, err = execute(t, replay.Plan{Dataset: ds, Chain: true, MaxCases: 2, Settings: staged(&generatetest.Backend{})})
	if err == nil || !strings.Contains(err.Error(), "3 cases in 1 chains") || !strings.Contains(err.Error(), "lower --limit") {
		t.Errorf("error %v", err)
	}
	// Two repositories whose names differ by a slash and a dash do not share a directory.
	names := map[string]bool{}
	for _, repo := range []string{"a/b", "a-b", "a/b-c", "a-b/c"} {
		names[replay.Chain{Repository: repo, Number: 1}.Name()] = true
	}
	if len(names) != 4 {
		t.Errorf("names %v", names)
	}
	// A filter on a fact the index does not have is refused, not read as zero.
	e := newDataset(t, src)
	e.addCase(t, "c1", map[string]any{"attachments": nil})
	es, _ := replay.OpenDataset(e.dir)
	yes := true
	if _, err := es.Select(replay.Selection{Images: &yes}); err == nil || !strings.Contains(err.Error(), "no attachments or threads count") {
		t.Errorf("error %v", err)
	}
	if ids, err := es.Select(replay.Selection{}); err != nil || len(ids) != 1 {
		t.Errorf("%v %v", ids, err)
	}
}

func TestChainDryRunCountsChains(t *testing.T) {
	src := assembleSource(t)
	d := newDataset(t, src)
	for _, id := range []string{"a1", "a2"} {
		d.pullRevision(t, 7, id, nil)
	}
	for _, id := range []string{"b1", "b2", "b3"} {
		d.pullRevision(t, 8, id, nil)
	}
	d.pullRevision(t, 9, "c1", nil)
	ds, _ := replay.OpenDataset(d.dir)
	_, printed, err := execute(t, replay.Plan{Dataset: ds, Chain: true, DryRun: true, Selection: replay.Selection{Limit: 3}})
	if err != nil || !strings.Contains(printed, "2 chain(s) with 5 revision(s) are eligible before --limit; 1 chain(s), 2 case(s) are selected") {
		t.Errorf("%v\n%s", err, printed)
	}
	if strings.Contains(printed, "pass the index filters") {
		t.Errorf("a chain dry run counts raw candidates:\n%s", printed)
	}
}

func TestDetectorReportWritesIDsToTheRunDirectoryOnly(t *testing.T) {
	src := assembleSource(t)
	d := newDataset(t, src)
	schema := bundleWith(t, src, "c1", []bundle.Hunk{hunk("db/migrations/1.sql", "+x")})
	d.addCaseJSON(t, "c1", nil, schema)
	d.addCaseJSON(t, "c2", nil, bundleWith(t, src, "c2", []bundle.Hunk{hunk("server/routes.go", `+	mux.HandleFunc("GET /x", h)`)}))
	d.addCaseJSON(t, "c3", nil, bundleWith(t, src, "c3", []bundle.Hunk{{ID: "h", Path: "x.go", Role: "code"}}))
	ds, _ := replay.OpenDataset(d.dir)
	before := snapshot(t, d.dir)
	outDir := t.TempDir()
	_, printed, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, DryRun: true, DetectorReport: true})
	if err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		raw, err := os.ReadFile(filepath.Join(outDir, "run", "detectors", name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return strings.TrimSpace(string(raw))
	}
	if read("schema-path.txt") != "c1" || read("api-route.txt") != "c2" || read("api-text-only.txt") != "c2" || read("blind.txt") != "c3" || read("none.txt") != "c3" {
		t.Error("the report lists other ids")
	}
	if !strings.Contains(printed, "wrote the detector report") || !strings.Contains(printed, "cases whose every hunk lacks patch text: 1") || !strings.Contains(printed, "heuristics") {
		t.Errorf("output:\n%s", printed)
	}
	unchanged(t, d.dir, before)
	if _, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, DryRun: true, DetectorReport: true, Selection: replay.Selection{IDs: []string{"c1"}}}); err == nil {
		t.Error("a report of named cases is accepted")
	}
}
